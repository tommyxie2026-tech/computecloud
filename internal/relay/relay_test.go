package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func signed(t *testing.T, key []byte, pair, role string, epochs ...string) string {
	t.Helper()
	now := time.Now()
	epoch := "relay-1"
	if len(epochs) > 0 {
		epoch = epochs[0]
	}
	token, err := Sign(key, Ticket{RelayEpoch: epoch, PairID: pair, ServerID: "server", WorkerID: "worker", Epoch: "epoch-1", Role: role, IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(time.Minute).UnixMilli()}, now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func TestTicketSecurity(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	now := time.Now()
	token := signed(t, key, "pair", "worker")
	if _, err := Verify(key, token, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{token + "x", strings.Repeat("x", MaxTicketBytes+1), "", "not.signed"} {
		if _, err := Verify(key, bad, now); err == nil {
			t.Fatal("bad token accepted")
		}
	}
	if _, err := Verify(bytes.Repeat([]byte{8}, 32), token, now); err == nil {
		t.Fatal("wrong key")
	}
	if _, err := Verify(key, token, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired ticket")
	}
	for _, edit := range []func(*Ticket){func(x *Ticket) { x.Role = "planner" }, func(x *Ticket) { x.WorkerID = "../worker" }, func(x *Ticket) { x.ExpiresMS = now.Add(2 * time.Minute).UnixMilli() }, func(x *Ticket) { x.IssuedMS = now.Add(time.Second).UnixMilli() }} {
		claim := Ticket{RelayEpoch: "relay-1", PairID: "p", ServerID: "s", WorkerID: "w", Epoch: "e", Role: "server", IssuedMS: now.UnixMilli(), ExpiresMS: now.Add(time.Minute).UnixMilli()}
		edit(&claim)
		if _, err := Sign(key, claim, now); err == nil {
			t.Fatal("invalid claim signed")
		}
	}
}
func TestPairFencingReplayAndQuota(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	limits := DefaultLimits()
	limits.PairsPerWorker = 1
	b, err := New(key, limits)
	if err != nil {
		t.Fatal(err)
	}
	a, aa := net.Pipe()
	defer a.Close()
	defer aa.Close()
	c, cc := net.Pipe()
	defer c.Close()
	defer cc.Close()
	server, err := Verify(key, signed(t, key, "pair", "server", b.Epoch()), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, first, err := b.attach(a, server)
	if err != nil || !first {
		t.Fatal(err)
	}
	defer b.release(server, p)
	if _, _, err = b.attach(c, server); err == nil {
		t.Fatal("nonce replay")
	}
	restarted, e := New(key, limits)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = restarted.attach(c, server); e == nil {
		t.Fatal("ticket survived relay restart")
	}
	wrong := server
	wrong.Nonce = "new"
	wrong.Role = "worker"
	wrong.Epoch = "old"
	if _, _, err = b.attach(c, wrong); err == nil {
		t.Fatal("epoch mismatch")
	}
	other := server
	other.PairID = "other"
	other.Nonce = "quota"
	if _, _, err = b.attach(c, other); err == nil {
		t.Fatal("worker quota")
	}
	worker, err := Verify(key, signed(t, key, "pair", "worker", b.Epoch()), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, first, err = b.attach(c, worker); err != nil || first {
		t.Fatal(err)
	}
	replay := worker
	replay.Nonce = "fresh-nonce"
	if _, _, err = b.attach(c, replay); err == nil {
		t.Fatal("completed pair reused")
	}
}
func relayTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay-fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kp, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{kp}}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}
}
func TestRelayOpaqueInnerTLSAndShutdown(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	serverTLS, clientTLS := relayTLS(t)
	b, err := New(key, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Serve(ctx, listener, serverTLS) }()
	callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
	defer callCancel()
	type result struct {
		conn net.Conn
		err  error
	}
	left := make(chan result, 1)
	serverTicket := signed(t, key, "pair", "server", b.Epoch())
	workerTicket := signed(t, key, "pair", "worker", b.Epoch())
	go func() {
		conn, e := Connect(callCtx, listener.Addr().String(), serverTicket, clientTLS)
		left <- result{conn, e}
	}()
	right, err := Connect(callCtx, listener.Addr().String(), workerTicket, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	lhs := <-left
	if lhs.err != nil {
		t.Fatal(lhs.err)
	}
	defer lhs.conn.Close()
	callCancel() // Dial cancellation after success must not tear down the live stream.
	innerServer := tls.Server(lhs.conn, serverTLS)
	innerClient := tls.Client(right, clientTLS)
	payload := []byte("opaque inner TLS: Worker bearer remains inside this channel")
	echoed := make(chan error, 1)
	go func() {
		buf := make([]byte, len(payload))
		if _, e := io.ReadFull(innerServer, buf); e != nil {
			echoed <- e
			return
		}
		_, e := innerServer.Write(buf)
		echoed <- e
	}()
	innerClient.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = innerClient.Write(payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err = io.ReadFull(innerClient, buf); err != nil || !bytes.Equal(buf, payload) {
		t.Fatalf("echo=%s %v", buf, err)
	}
	if err = <-echoed; err != nil {
		t.Fatal(err)
	}
	replayCtx, replayCancel := context.WithTimeout(ctx, time.Second)
	defer replayCancel()
	if conn, e := Connect(replayCtx, listener.Addr().String(), workerTicket, clientTLS); e == nil {
		conn.Close()
		t.Fatal("used ticket replay succeeded")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay shutdown leaked")
	}
	b.mu.Lock()
	if len(b.pairs) != 0 || len(b.workers) != 0 {
		b.mu.Unlock()
		t.Fatal("pair quota leaked")
	}
	b.mu.Unlock()
	stats := b.Stats()
	if stats.ActiveConnections != 0 || stats.AcceptedPairs != 1 || stats.BytesIn < uint64(2*len(payload)) || stats.BytesOut != stats.BytesIn || stats.RejectedReplay == 0 {
		t.Fatalf("relay stats=%+v", stats)
	}
}
func TestRelayRejectsUnverifiedTLS(t *testing.T) {
	ctx := context.Background()
	if conn, err := Connect(ctx, "127.0.0.1:1", "ticket", &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}); err == nil {
		conn.Close()
		t.Fatal("unverified outer TLS")
	}
	b, err := New(bytes.Repeat([]byte{7}, 32), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Serve(ctx, nil, nil); err == nil {
		t.Fatal("plaintext relay listener")
	}
}
