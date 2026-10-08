package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/relay"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

func TestConfiguredRelayDirectAndFallback(t *testing.T) {
	certFile, keyFile := testutil.TLSFiles(t)
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := relay.New(bytes.Repeat([]byte{9}, 32), relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	brokerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	brokerDone := make(chan error, 1)
	go func() {
		brokerDone <- broker.Serve(ctx, brokerListener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-brokerDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})

	writeToken := func(name string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(strings.Repeat(name, 32)), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	serverToken, workerToken := writeToken("server"), writeToken("worker")
	issuer, err := relay.NewIssuer(broker, "server", []byte(strings.Repeat("server", 32)), map[string][]byte{"w1": []byte(strings.Repeat("worker1", 32)), "w2": []byte(strings.Repeat("worker", 32))})
	if err != nil {
		t.Fatal(err)
	}
	issuerHTTP := httptest.NewUnstartedServer(issuer)
	_ = issuerHTTP.Listener.Close()
	issuerHTTP.Listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	issuerHTTP.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	issuerHTTP.StartTLS()
	t.Cleanup(issuerHTTP.Close)

	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverRelay := config.RelayTransport{Mode: "direct_then_relay", RelayAddress: brokerListener.Addr().String(), IssuerURL: issuerHTTP.URL, TokenFile: serverToken, CAFile: certFile, ServerName: "127.0.0.1"}
	workerRelay := serverRelay
	workerRelay.TokenFile = workerToken
	h := newJobHarnessWithTransport(t, true, &jobTestTransport{
		ServerTLS: config.TLS{CertFile: certFile, KeyFile: keyFile},
		WorkerTLS: config.TLS{CAFile: certFile}, Listener: direct, ConfiguredRelay: true,
		WorkerTransports: map[string]config.RelayTransport{"w2": workerRelay},
		WorkerAddresses:  map[string]string{"w2": "127.0.0.1:1"},
	}, func(c *config.Server) { c.Transport = serverRelay })
	j, err := h.s.SubmitJob(h.ctx, "relay-fallback", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.wait(t, j.ID); got.State != "SUCCEEDED" {
		t.Fatalf("relay job state: %s", got.State)
	}
}
