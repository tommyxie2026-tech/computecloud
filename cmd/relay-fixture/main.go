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
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	issuerListen := flag.String("issuer-listen", "", "optional TLS ticket issuer listener")
	issuerServerID := flag.String("issuer-server-id", "", "Server identity permitted to issue pairs")
	issuerServerToken := flag.String("issuer-server-token-file", "", "private Server issuer token file")
	issuerWorkerTokens := flag.String("issuer-worker-tokens-file", "", "private JSON map of Worker IDs to issuer tokens")
	metricsInterval := flag.Duration("metrics-interval", 30*time.Second, "structured transport metrics interval (1s-1h)")
	flag.Parse()
	if *metricsInterval < time.Second || *metricsInterval > time.Hour {
		return fmt.Errorf("metrics interval must be between 1s and 1h")
	}
	key, err := readPrivate(*signingFile)
	if err != nil {
		return fmt.Errorf("ticket signing key: %w", err)
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
	var issuerServer *http.Server
	var issuerDone chan error
	if *issuerListen != "" {
		serverToken, err := readPrivate(*issuerServerToken)
		if err != nil {
			return fmt.Errorf("issuer Server token: %w", err)
		}
		workerRaw, err := readPrivate(*issuerWorkerTokens)
		if err != nil {
			return fmt.Errorf("issuer Worker tokens: %w", err)
		}
		var workerStrings map[string]string
		if err := json.Unmarshal(workerRaw, &workerStrings); err != nil {
			return fmt.Errorf("invalid issuer Worker token map: %w", err)
		}
		workerTokens := make(map[string][]byte, len(workerStrings))
		for id, token := range workerStrings {
			workerTokens[id] = []byte(token)
		}
		issuer, err := relay.NewIssuer(broker, *issuerServerID, []byte(strings.TrimSpace(string(serverToken))), workerTokens)
		if err != nil {
			return err
		}
		issuerListener, err := net.Listen("tcp", *issuerListen)
		if err != nil {
			return err
		}
		issuerServer = &http.Server{Handler: issuer, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
		issuerDone = make(chan error, 1)
		go func() {
			err := issuerServer.Serve(tls.NewListener(issuerListener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))
			if err != nil && err != http.ErrServerClosed {
				cancel()
			}
			issuerDone <- err
		}()
	} else if *issuerServerID != "" || *issuerServerToken != "" || *issuerWorkerTokens != "" {
		return fmt.Errorf("issuer listener required when issuer credentials are configured")
	}
	slog.Info("experimental relay fixture listening", "address", listener.Addr().String(), "relay_epoch", broker.Epoch())
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		ticker := time.NewTicker(*metricsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				logMetrics(broker, false)
			case <-ctx.Done():
				logMetrics(broker, true)
				return
			}
		}
	}()
	err = broker.Serve(ctx, listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	if ctx.Err() == nil {
		cancel()
	}
	<-metricsDone
	if issuerServer != nil {
		_ = issuerServer.Close()
		issuerErr := <-issuerDone
		if issuerErr != nil && issuerErr != http.ErrServerClosed {
			return issuerErr
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func logMetrics(broker *relay.Broker, final bool) {
	m := broker.Stats()
	slog.Info("relay transport metrics",
		"final", final,
		"active_connections", m.ActiveConnections,
		"accepted_pairs", m.AcceptedPairs,
		"bytes_in", m.BytesIn,
		"bytes_out", m.BytesOut,
		"backpressure_total", m.BackpressureTotal,
		"rejected_invalid", m.RejectedInvalid,
		"rejected_replay", m.RejectedReplay,
		"rejected_capacity", m.RejectedCapacity,
		"rejected_quota", m.RejectedQuota,
		"rejected_timeout", m.RejectedTimeout,
		"rejected_not_ready", m.RejectedNotReady,
	)
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private regular file with mode 0600 required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 32 {
		return nil, fmt.Errorf("credential requires at least 32 bytes")
	}
	return b, nil
}
