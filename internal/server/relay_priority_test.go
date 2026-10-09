package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/relay"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"golang.org/x/net/http2"
)

// TestRelayControlProgressDuringBulkTransfer pins the transport property used by
// gRPC: a blocked Bulk HTTP/2 stream cannot consume the whole opaque Relay
// connection or prevent an independent control stream from making progress.
func TestRelayControlProgressDuringBulkTransfer(t *testing.T) {
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
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("test CA rejected")
	}
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1", NextProtos: []string{"h2"}}

	limits := relay.DefaultLimits()
	limits.BytesPerSecond = 1 << 30
	key := bytes.Repeat([]byte{19}, 32)
	broker, err := relay.New(key, limits)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- broker.Serve(ctx, listener, serverTLS) }()
	t.Cleanup(func() {
		cancel()
		if err := <-brokerDone; err != nil && err != context.Canceled {
			t.Error(err)
		}
	})

	now := time.Now()
	claim := relay.Ticket{RelayEpoch: broker.Epoch(), PairID: store.ID(), ServerID: "server", WorkerID: "worker", Epoch: store.ID(), Role: "server", IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(relay.MaxTicketTTL).UnixMilli()}
	serverTicket, err := relay.Sign(key, claim, now)
	if err != nil {
		t.Fatal(err)
	}
	claim.Role = "worker"
	workerTicket, err := relay.Sign(key, claim, now)
	if err != nil {
		t.Fatal(err)
	}
	type dialResult struct {
		conn net.Conn
		err  error
	}
	serverResult := make(chan dialResult, 1)
	go func() {
		conn, err := relay.Connect(ctx, listener.Addr().String(), serverTicket, clientTLS)
		serverResult <- dialResult{conn: conn, err: err}
	}()
	workerConn, err := relay.Connect(ctx, listener.Addr().String(), workerTicket, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workerConn.Close() })
	serverSide := <-serverResult
	if serverSide.err != nil {
		t.Fatal(serverSide.err)
	}
	t.Cleanup(func() { serverSide.conn.Close() })

	bulkStarted := make(chan struct{})
	releaseBulk := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBulk) }) }
	t.Cleanup(release)
	serverDone := make(chan struct{})
	serverHandshake := make(chan error, 1)
	go func() {
		defer close(serverDone)
		inner := tls.Server(serverSide.conn, serverTLS)
		if err := inner.HandshakeContext(ctx); err != nil {
			serverHandshake <- err
			return
		}
		serverHandshake <- nil
		(&http2.Server{}).ServeConn(inner, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/bulk":
				var one [1]byte
				if _, err := io.ReadFull(r.Body, one[:]); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				close(bulkStarted)
				<-releaseBulk
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusNoContent)
			case "/control":
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		})})
	}()

	innerClient := tls.Client(workerConn, clientTLS)
	if err := innerClient.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverHandshake; err != nil {
		t.Fatal(err)
	}
	clientConn, err := (&http2.Transport{}).NewClientConn(innerClient)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientConn.Close()
		workerConn.Close()
		serverSide.conn.Close()
		<-serverDone
	})

	reader, writer := io.Pipe()
	bulkResponse := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://relay.test/bulk", reader)
		resp, err := clientConn.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		bulkResponse <- err
	}()
	bulkWriteDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(writer, io.LimitReader(zeroReader{}, 16<<20))
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		bulkWriteDone <- err
	}()
	select {
	case <-bulkStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Bulk stream did not reach Relay-backed server")
	}
	select {
	case err := <-bulkWriteDone:
		t.Fatalf("Bulk body was buffered without backpressure: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	controlCtx, controlCancel := context.WithTimeout(ctx, 2*time.Second)
	defer controlCancel()
	controlReq, _ := http.NewRequestWithContext(controlCtx, http.MethodPost, "https://relay.test/control", nil)
	started := time.Now()
	controlResp, err := clientConn.RoundTrip(controlReq)
	if err != nil {
		t.Fatalf("control stream blocked by Bulk: %v", err)
	}
	controlResp.Body.Close()
	if controlResp.StatusCode != http.StatusNoContent || time.Since(started) >= 2*time.Second {
		t.Fatalf("control status=%d duration=%s", controlResp.StatusCode, time.Since(started))
	}
	release()
	if err := <-bulkWriteDone; err != nil {
		t.Fatal(err)
	}
	if err := <-bulkResponse; err != nil {
		t.Fatal(err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
