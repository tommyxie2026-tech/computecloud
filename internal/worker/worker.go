package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type active struct {
	cancel  context.CancelFunc
	ready   chan struct{}
	granted bool
	until   time.Time
}
type Worker struct {
	cfg    config.Worker
	db     *store.DB
	client pb.RuntimeServiceClient
	conn   *grpc.ClientConn
	mu     sync.Mutex
	runs   map[string]*active
	wg     sync.WaitGroup
	hello  *pb.WorkerHello
}

func enc(m proto.Message) []byte {
	b, e := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if e != nil {
		panic(e)
	}
	return b
}
func dec(b []byte, m proto.Message) error { return protojson.Unmarshal(b, m) }
func New(c config.Worker) (*Worker, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	token, e := config.Token(c.TokenFile)
	if e != nil {
		return nil, e
	}
	conn, e := rpcutil.Dial(c.Address, token, c.TLS)
	if e != nil {
		return nil, e
	}
	d, e := store.Open(c.DataDir, store.WorkerSchema)
	if e != nil {
		conn.Close()
		return nil, e
	}
	return &Worker{cfg: c, db: d, conn: conn, client: pb.NewRuntimeServiceClient(conn), runs: map[string]*active{}}, nil
}
func (w *Worker) Close() error { return errors.Join(w.conn.Close(), w.db.Close()) }
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func (w *Worker) probe(ctx context.Context) error {
	h := &pb.WorkerHello{WorkerId: w.cfg.ID, Epoch: store.ID(), Slots: int32(w.cfg.Slots)}
	for _, profile := range keys(w.cfg.Runtimes) {
		r := w.cfg.Runtimes[profile]
		if profile != "codex_exec" && profile != "claude_print" {
			return fmt.Errorf("unsupported profile %s", profile)
		}
		if r.Version == "" || len(r.Models) == 0 || len(r.Credentials) == 0 {
			return fmt.Errorf("runtime %s needs pinned version, models and credentials", profile)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(probeCtx, r.Executable, "--version")
		cmd.WaitDelay = time.Second
		b, e := cmd.Output()
		cancel()
		if e != nil {
			return fmt.Errorf("probe %s: %w", profile, e)
		}
		if strings.TrimSpace(string(b)) != r.Version {
			return fmt.Errorf("runtime %s version mismatch: configured %q, observed %q", profile, r.Version, strings.TrimSpace(string(b)))
		}
		digests := map[string]string{}
		for _, tmpl := range config.Templates(w.cfg) {
			if tmpl.RuntimeProfile == profile {
				digests[tmpl.Key()] = tmpl.Digest
			}
		}
		caps := []string{"event_stream", "cancel", "job_io_v1", "artifact_inputs_v1"}
		if profile == "codex_exec" {
			caps = append(caps, "gateway_inference_v1")
		}
		h.Runtimes = append(h.Runtimes, &pb.Runtime{Profile: profile, Version: r.Version, Models: r.Models, Credentials: r.Credentials, Capabilities: caps, Repositories: keys(w.cfg.Repositories), Policies: keys(w.cfg.Policies), Verifiers: keys(w.cfg.Verifiers), TemplateDigests: digests})
	}
	w.hello = h
	return nil
}
func (w *Worker) Run(ctx context.Context) error {
	if e := w.recover(ctx); e != nil {
		return e
	}
	if e := w.bootstrapWorkspaceInventory(ctx); e != nil {
		return e
	}
	if e := w.reconcileWorkspaceLifecycle(ctx); e != nil {
		return e
	}
	if e := w.probe(ctx); e != nil {
		return e
	}
	loopsCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var loops sync.WaitGroup
	loops.Add(3)
	go func() { defer loops.Done(); w.flushLoop(loopsCtx) }()
	go func() { defer loops.Done(); w.leaseLoop(loopsCtx) }()
	go func() { defer loops.Done(); w.workspaceLoop(loopsCtx) }()
	for ctx.Err() == nil {
		if e := w.connect(ctx); e != nil && ctx.Err() == nil {
			slog.Warn("worker disconnected", "error", e)
		}
		if e := pause(ctx, time.Second); e != nil {
			break
		}
	}
	w.mu.Lock()
	for _, r := range w.runs {
		r.cancel()
	}
	w.mu.Unlock()
	w.wg.Wait()
	cancel()
	loops.Wait()
	// Best effort final delivery; undelivered records remain in the local state.db.
	finalCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = w.flush(finalCtx)
	return nil
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (w *Worker) connect(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stream, e := w.client.ConnectWorker(ctx)
	if e != nil {
		return e
	}
	var sendMu sync.Mutex
	send := func(f *pb.WorkerFrame) error { sendMu.Lock(); defer sendMu.Unlock(); return stream.Send(f) }
	if e = send(&pb.WorkerFrame{Body: &pb.WorkerFrame_Hello{Hello: w.hello}}); e != nil {
		return e
	}
	go func() {
		for {
			refs, e := w.refs(ctx)
			if e == nil {
				e = send(&pb.WorkerFrame{Body: &pb.WorkerFrame_Renew{Renew: &pb.Renew{Attempts: refs}}})
			}
			if e != nil {
				cancel()
				return
			}
			if pause(ctx, time.Second) != nil {
				return
			}
		}
	}()
	for {
		f, e := stream.Recv()
		if e != nil {
			return e
		}
		if grant := f.GetLease(); grant != nil {
			w.mu.Lock()
			if r := w.runs[grant.AttemptId]; r != nil {
				if !grant.Valid || grant.TtlMs < 1000 {
					r.cancel()
				} else {
					r.until = time.Now().Add(time.Duration(grant.TtlMs)*time.Millisecond - time.Second/2)
					if !r.granted {
						r.granted = true
						close(r.ready)
					}
				}
			}
			w.mu.Unlock()
			continue
		}
		cmd := f.GetCommand()
		if cmd == nil {
			return errors.New("unknown server frame")
		}
		launch, e := w.accept(ctx, cmd)
		if e != nil {
			return e
		}
		if launch {
			w.start(parent, cmd.Assignment)
		}
		if e = send(&pb.WorkerFrame{Body: &pb.WorkerFrame_Ack{Ack: &pb.CommandAck{CommandId: cmd.CommandId, State: "RECEIVED"}}}); e != nil {
			return e
		}
		if cmd.Kind == "stop" {
			w.mu.Lock()
			if r := w.runs[cmd.Assignment.AttemptId]; r != nil {
				r.cancel()
			}
			w.mu.Unlock()
		}
	}
}
func (w *Worker) start(ctx context.Context, a *pb.Assignment) {
	w.mu.Lock()
	if _, ok := w.runs[a.AttemptId]; ok {
		w.mu.Unlock()
		return
	}
	if len(w.runs) >= w.cfg.Slots {
		w.mu.Unlock()
		_ = w.failUnstarted(context.Background(), a, "RESOURCE_LIMIT", "worker slots full")
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &active{cancel: cancel, ready: make(chan struct{}), until: time.Now().Add(10 * time.Second)}
	w.runs[a.AttemptId] = r
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		defer cancel()
		defer func() { w.mu.Lock(); delete(w.runs, a.AttemptId); w.mu.Unlock() }()
		select {
		case <-runCtx.Done():
			_ = w.failUnstarted(context.Background(), a, "LEASE_LOST", "no valid execution lease")
			return
		case <-r.ready:
		}
		w.execute(runCtx, a)
	}()
}
func (w *Worker) leaseLoop(ctx context.Context) {
	for pause(ctx, 100*time.Millisecond) == nil {
		w.mu.Lock()
		for _, r := range w.runs {
			if !time.Now().Before(r.until) {
				r.cancel()
			}
		}
		w.mu.Unlock()
	}
}
func (w *Worker) refs(ctx context.Context) ([]*pb.AttemptRef, error) {
	rows, e := w.db.SQL.QueryContext(ctx, "SELECT assignment FROM runs WHERE completed=0")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []*pb.AttemptRef
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		a := new(pb.Assignment)
		if e = dec(b, a); e != nil {
			return nil, e
		}
		out = append(out, ref(a))
	}
	return out, rows.Err()
}
func ref(a *pb.Assignment) *pb.AttemptRef {
	return &pb.AttemptRef{AttemptId: a.AttemptId, Generation: a.Generation, LeaseToken: a.LeaseToken}
}
