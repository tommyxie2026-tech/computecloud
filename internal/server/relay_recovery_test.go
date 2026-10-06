package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/relay"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

type relayTestListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *relayTestListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.connections:
		return c, nil
	}
}
func (l *relayTestListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (l *relayTestListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7443}
}
func TestRelayJobRestartRecovery(t *testing.T) {
	certFile, keyFile := testutil.TLSFiles(t)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	outerServer := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	outerClient := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}
	key := bytes.Repeat([]byte{9}, 32)
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	listener := &relayTestListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	type route struct {
		broker  *relay.Broker
		address string
		cancel  context.CancelFunc
		done    chan error
	}
	var mu sync.RWMutex
	var active route
	restart := func() {
		t.Helper()
		b, e := relay.New(key, relay.DefaultLimits())
		if e != nil {
			t.Fatal(e)
		}
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(rootCtx)
		next := route{b, l.Addr().String(), cancel, make(chan error, 1)}
		go func() { next.done <- b.Serve(ctx, l, outerServer) }()
		mu.Lock()
		old := active
		active = next
		mu.Unlock()
		if old.cancel != nil {
			old.cancel()
			<-old.done
		}
	}
	restart()
	t.Cleanup(func() {
		rootCancel()
		mu.RLock()
		current := active
		mu.RUnlock()
		current.cancel()
		<-current.done
	})
	var connectorsMu sync.Mutex
	connectors := []*rpcutil.DirectFirstConnector{}
	factory := func(workerID string) rpcutil.Connector {
		alternate := rpcutil.ConnectorFunc(func(ctx context.Context, _ string) (net.Conn, error) {
			mu.RLock()
			r := active
			mu.RUnlock()
			pair := store.ID()
			now := time.Now()
			claim := relay.Ticket{RelayEpoch: r.broker.Epoch(), PairID: pair, ServerID: "server", WorkerID: workerID, Epoch: store.ID(), Role: "server", IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(relay.MaxTicketTTL).UnixMilli()}
			serverTicket, e := relay.Sign(key, claim, now)
			if e != nil {
				return nil, e
			}
			claim.Role = "worker"
			workerTicket, e := relay.Sign(key, claim, now)
			if e != nil {
				return nil, e
			}
			go func() {
				conn, e := relay.Connect(rootCtx, r.address, serverTicket, outerClient)
				if e != nil {
					return
				}
				select {
				case listener.connections <- conn:
				case <-listener.closed:
					conn.Close()
				case <-rootCtx.Done():
					conn.Close()
				}
			}()
			return relay.Connect(ctx, r.address, workerTicket, outerClient)
		})
		direct := rpcutil.ConnectorFunc(func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("fixture direct path unavailable")
		})
		connector, e := rpcutil.NewDirectFirstConnector(direct, alternate, 100*time.Millisecond)
		if e != nil {
			t.Fatal(e)
		}
		connectorsMu.Lock()
		connectors = append(connectors, connector)
		connectorsMu.Unlock()
		return connector
	}
	h := newJobHarnessWithTransport(t, true, &jobTestTransport{ServerTLS: config.TLS{CertFile: certFile, KeyFile: keyFile}, WorkerTLS: config.TLS{CAFile: certFile}, Listener: listener, Connector: factory}, func(c *config.Server) { c.LeaseSeconds = 30 })
	spec := h.spec("single")
	spec.Input.Text = "long-job"
	j, err := h.s.SubmitJob(h.ctx, "relay-restart", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	for {
		var n int
		if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=? AND t.state='RUNNING'", j.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("no relay assignment")
		case <-time.After(20 * time.Millisecond):
		}
	}
	restart()
	if done := h.wait(t, j.ID); done.State != "SUCCEEDED" {
		t.Fatalf("relay restart job=%+v", done)
	}
	var attempts int
	if err = h.s.db.SQL.QueryRow("SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?", j.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("relay duplicated attempt=%d %v", attempts, err)
	}
	mapped, err := h.s.SubmitJob(h.ctx, "relay-map-reduce", job.JSON(h.spec("report_merge_v1")))
	if err != nil {
		t.Fatal(err)
	}
	if done := h.wait(t, mapped.ID); done.State != "SUCCEEDED" {
		t.Fatalf("relay map/reduce=%+v", done)
	}
	connectorsMu.Lock()
	defer connectorsMu.Unlock()
	var fallbacks uint64
	for _, c := range connectors {
		fallbacks += c.Stats().FallbackAttempts
	}
	if fallbacks < 3 {
		t.Fatalf("reconnection not exercised: %d", fallbacks)
	}
}
