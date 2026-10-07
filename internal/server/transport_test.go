package server

import (
	"context"
	"net"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

func TestTransportTunnelRejectsPlaintext(t *testing.T) {
	s, _, _, _ := offlineJobServer(t)
	defer s.Close()
	if err := s.ServeTunnel(context.Background(), nil); err == nil {
		t.Fatal("plaintext tunnel accepted")
	}
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
