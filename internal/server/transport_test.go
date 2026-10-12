package server

import (
	"context"
	"net"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTransportTunnelRejectsPlaintext(t *testing.T) {
	s, _, _, _ := offlineJobServer(t)
	defer s.Close()
	if err := s.ServeTunnel(context.Background(), nil); err == nil {
		t.Fatal("plaintext tunnel accepted")
	}
}

func TestWorkerSessionAdmissionLimit(t *testing.T) {
	s := &Server{cfg: config.Server{WorkerAdmission: config.WorkerAdmission{MaxSessions: 1}}, peers: map[string]*session{}, workerReservations: map[string]bool{}}
	release, err := s.reserveWorkerSession("worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.reserveWorkerSession("worker-b"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over-capacity worker error=%v", err)
	}
	release()
	s.peers["worker-a"] = &session{}
	reconnectRelease, err := s.reserveWorkerSession("worker-a")
	if err != nil {
		t.Fatalf("same worker reconnect rejected: %v", err)
	}
	reconnectRelease()
}

func TestDirectAndTunnelShareServerSessions(t *testing.T) {
	certFile, keyFile := testutil.TLSFiles(t)
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	tunnel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tunnel.Close() })
	transport := &jobTestTransport{
		ServerTLS: config.TLS{CertFile: certFile, KeyFile: keyFile}, WorkerTLS: config.TLS{CAFile: certFile},
		Listener: direct, TunnelListener: tunnel,
		Connector: func(workerID string) rpcutil.Connector {
			address := direct.Addr().String()
			if workerID == "w2" {
				address = tunnel.Addr().String()
			}
			return rpcutil.ConnectorFunc(func(ctx context.Context, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", address)
			})
		},
	}
	h := newJobHarnessWithTransport(t, true, transport)
	h.s.mu.Lock()
	_, directWorker := h.s.peers["w1"]
	_, tunnelWorker := h.s.peers["w2"]
	h.s.mu.Unlock()
	if !directWorker || !tunnelWorker {
		t.Fatal("both transports did not register in one Server session registry")
	}
}
