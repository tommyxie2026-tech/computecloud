package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/maintenance"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/server"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/worker"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var version = "0.3.1"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:]); e != nil {
		slog.Error("computecloud failed", "error", e)
		os.Exit(1)
	}
}
func printJSON(m proto.Message) error {
	b, e := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if e != nil {
		return e
	}
	_, e = fmt.Fprintln(os.Stdout, string(b))
	return e
}
func printEvent(ev *pb.Event) error {
	b, e := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
	if e != nil {
		return e
	}
	var out map[string]json.RawMessage
	if e = json.Unmarshal(b, &out); e != nil {
		return e
	}
	delete(out, "payload_json")
	if len(ev.PayloadJson) > 0 {
		out["payload"] = json.RawMessage(ev.PayloadJson)
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: computecloud server|worker|job|task|workers|artifact|templates|backup|version")
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Printf("computecloud %s (%s, %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}
	group := args[0]
	op := ""
	args = args[1:]
	if group == "task" || group == "artifact" || group == "job" {
		if len(args) == 0 {
			return fmt.Errorf("subcommand required")
		}
		op = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet(group, flag.ContinueOnError)
	cfgFile := fs.String("config", "computecloud.yaml", "configuration file")
	file := fs.String("file", "", "task JSON file")
	id := fs.String("id", "", "task ID")
	after := fs.Int64("after", 0, "event cursor")
	control := fs.String("control-id", "", "cancel idempotency key")
	artifact := fs.String("artifact", "", "artifact ID")
	out := fs.String("out", "", "output file (must not exist)")
	dataDir := fs.String("data-dir", "", "offline server data directory")
	key := fs.String("key", "", "Job submission idempotency key")
	cursor := fs.String("cursor", "", "Job task/artifact page cursor")
	limit := fs.Int("limit", 50, "Job page size")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if group == "backup" {
		if *dataDir == "" || *out == "" {
			return fmt.Errorf("backup requires --data-dir and --out")
		}
		return maintenance.Backup(ctx, *dataDir, *out)
	}
	c, e := config.Load(*cfgFile)
	if e != nil {
		return e
	}
	if group == "templates" {
		if e = c.Worker.Validate(); e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"templates": config.Templates(c.Worker)})
	}
	if group == "job" {
		return runJob(ctx, c.Client, jobOptions{op: op, file: *file, id: *id, key: *key, control: *control, artifact: *artifact, out: *out, cursor: *cursor, after: *after, limit: *limit})
	}
	switch group {
	case "server":
		s, e := server.New(c.Server)
		if e != nil {
			return e
		}
		defer s.Close()
		l, e := net.Listen("tcp", c.Server.Listen)
		if e != nil {
			return e
		}
		defer l.Close()
		slog.Info("server listening", "address", l.Addr(), "version", version)
		return s.Serve(ctx, l)
	case "worker":
		w, e := worker.New(c.Worker)
		if e != nil {
			return e
		}
		defer w.Close()
		return w.Run(ctx)
	}
	token, e := config.Token(c.Client.TokenFile)
	if e != nil {
		return e
	}
	conn, e := rpcutil.Dial(c.Client.Address, token, c.Client.TLS)
	if e != nil {
		return e
	}
	defer conn.Close()
	client := pb.NewRuntimeServiceClient(conn)
	if group+":"+op != "task:watch" && group+":"+op != "artifact:download" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	switch group + ":" + op {
	case "workers:":
		r, e := client.ListWorkers(ctx, &pb.Empty{})
		if e != nil {
			return e
		}
		return printJSON(r)
	case "task:submit":
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		r := new(pb.TaskSpec)
		if e = protojson.Unmarshal(b, r); e != nil {
			return e
		}
		t, e := client.SubmitTask(ctx, r)
		if e != nil {
			return e
		}
		return printJSON(t)
	case "task:get":
		t, e := client.GetTask(ctx, &pb.TaskRef{TaskId: *id})
		if e != nil {
			return e
		}
		return printJSON(t)
	case "task:cancel":
		if *control == "" {
			*control = store.ID()
		}
		t, e := client.CancelTask(ctx, &pb.CancelRequest{TaskId: *id, ControlId: *control, Reason: "requested by CLI"})
		if e != nil {
			return e
		}
		return printJSON(t)
	case "task:watch":
		stream, e := client.WatchEvents(ctx, &pb.WatchRequest{TaskId: *id, AfterSeq: *after})
		if e != nil {
			return e
		}
		for {
			event, e := stream.Recv()
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
			if e = printEvent(event); e != nil {
				return e
			}
		}
	case "artifact:list":
		r, e := client.ListArtifacts(ctx, &pb.TaskRef{TaskId: *id})
		if e != nil {
			return e
		}
		return printJSON(r)
	case "artifact:download":
		if *out == "" {
			return fmt.Errorf("--out required")
		}
		stream, e := client.DownloadArtifact(ctx, &pb.ArtifactRef{TaskId: *id, ArtifactId: *artifact})
		if e != nil {
			return e
		}
		f, e := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		ok := false
		defer func() {
			f.Close()
			if !ok {
				os.Remove(*out)
			}
		}()
		for {
			c, e := stream.Recv()
			if e == io.EOF {
				if e = f.Sync(); e != nil {
					return e
				}
				ok = true
				return nil
			}
			if e != nil {
				return e
			}
			if _, e = f.Write(c.Data); e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("unknown subcommand %s %s", group, op)
	}
}
