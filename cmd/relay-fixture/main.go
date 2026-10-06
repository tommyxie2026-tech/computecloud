// relay-fixture is experimental operator tooling, excluded from stable packages.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/relay"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relay fixture stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:7445", "outer TLS listener")
	certFile := flag.String("cert", "", "outer TLS certificate")
	certKey := flag.String("key", "", "outer TLS private key")
	signingFile := flag.String("ticket-key-file", "", "32+ byte private ticket signing key file")
	mint := flag.Bool("mint-ticket", false, "print one short-lived fixture ticket instead of serving")
	epoch := flag.String("relay-epoch", "", "current Relay boot epoch for ticket issuance")
	pair := flag.String("pair", "", "pair identity")
	server := flag.String("server", "", "inner Server identity")
	worker := flag.String("worker", "", "Worker identity")
	connectionEpoch := flag.String("connection-epoch", "", "connection epoch")
	role := flag.String("role", "", "server or worker")
	flag.Parse()
	info, err := os.Stat(*signingFile)
	if err != nil {
		return fmt.Errorf("ticket signing key unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("ticket signing key must be a private regular file")
	}
	key, err := os.ReadFile(*signingFile)
	if err != nil || len(key) < 32 {
		return fmt.Errorf("ticket signing key requires at least 32 bytes")
	}
	if *mint {
		now := time.Now()
		ticket, e := relay.Sign(key, relay.Ticket{RelayEpoch: *epoch, PairID: *pair, ServerID: *server, WorkerID: *worker, Epoch: *connectionEpoch, Role: *role, IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(relay.MaxTicketTTL).UnixMilli()}, now)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"ticket": ticket})
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *certKey)
	if err != nil {
		return fmt.Errorf("outer TLS certificate unavailable: %w", err)
	}
	broker, err := relay.New(key, relay.DefaultLimits())
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	slog.Info("experimental relay fixture listening", "address", listener.Addr().String(), "relay_epoch", broker.Epoch())
	err = broker.Serve(ctx, listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
