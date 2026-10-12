package rpcutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	hp "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type admissionTestStream struct{ ctx context.Context }

func (s admissionTestStream) SetHeader(metadata.MD) error  { return nil }
func (s admissionTestStream) SendHeader(metadata.MD) error { return nil }
func (s admissionTestStream) SetTrailer(metadata.MD)       {}
func (s admissionTestStream) Context() context.Context     { return s.ctx }
func (s admissionTestStream) SendMsg(any) error            { return nil }
func (s admissionTestStream) RecvMsg(any) error            { return nil }

func TestWorkerStreamAdmissionLimit(t *testing.T) {
	tokenFile := testutil.Token(t, "worker")
	auth, err := NewAuth(nil, []config.Identity{{TokenFile: tokenFile, WorkerID: "w1"}})
	if err != nil {
		t.Fatal(err)
	}
	auth.SetWorkerStreamLimit(1)
	token, err := config.Token(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	stream := admissionTestStream{ctx: ctx}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- auth.Stream(nil, stream, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error { close(entered); <-release; return nil })
	}()
	<-entered
	err = auth.Stream(nil, stream, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error { return nil })
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second stream error=%v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = auth.Stream(nil, stream, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
}

type mtlsTestFiles struct{ ca, serverCert, serverKey, workerCert, workerKey, wrongSANCert, wrongSANKey, revokedCert, revokedKey, unknownCert, unknownKey, expiredCert, expiredKey string }

func mtlsFiles(t *testing.T) mtlsTestFiles {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeCert := func(name string, tmpl *x509.Certificate, parent *x509.Certificate, signer *ecdsa.PrivateKey) (string, string) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		priv, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPath, keyPath := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
		if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600); err != nil {
			t.Fatal(err)
		}
		return certPath, keyPath
	}
	caPath := filepath.Join(dir, "ca.crt")
	if err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	serverCert, serverKey := writeCert("server", serverTemplate, caCert, caKey)
	makeWorker := func(name, workerID string, notBefore, notAfter time.Time, parent *x509.Certificate, signer *ecdsa.PrivateKey) (string, string) {
		uri, _ := url.Parse("spiffe://computecloud/workers/" + workerID)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(int64(len(name) + 200)), Subject: pkix.Name{CommonName: name}, NotBefore: notBefore, NotAfter: notAfter, URIs: []*url.URL{uri}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		return writeCert(name, tmpl, parent, signer)
	}
	workerCert, workerKey := makeWorker("worker", "w1", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), caCert, caKey)
	wrongSANCert, wrongSANKey := makeWorker("wrong-san", "w2", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), caCert, caKey)
	revokedCert, revokedKey := makeWorker("revoked", "w1", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), caCert, caKey)
	unknownKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	unknownTemplate := &x509.Certificate{SerialNumber: big.NewInt(300), Subject: pkix.Name{CommonName: "unknown-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	unknownDER, err := x509.CreateCertificate(rand.Reader, unknownTemplate, unknownTemplate, &unknownKey.PublicKey, unknownKey)
	if err != nil {
		t.Fatal(err)
	}
	unknownCA, err := x509.ParseCertificate(unknownDER)
	if err != nil {
		t.Fatal(err)
	}
	unknownCert, unknownWorkerKey := makeWorker("unknown", "w1", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), unknownCA, unknownKey)
	expiredCert, expiredKey := makeWorker("expired", "w1", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour), caCert, caKey)
	return mtlsTestFiles{caPath, serverCert, serverKey, workerCert, workerKey, wrongSANCert, wrongSANKey, revokedCert, revokedKey, unknownCert, unknownWorkerKey, expiredCert, expiredKey}
}

func certFingerprint(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func mtlsHealthCode(t *testing.T, files mtlsTestFiles, mode, certPath, keyPath, tokenFile string) codes.Code {
	t.Helper()
	workerToken := testutil.Token(t, "worker")
	userToken := testutil.Token(t, "user")
	workerID := "w1"
	if tokenFile == "user" {
		tokenFile = userToken
	} else {
		tokenFile = workerToken
	}
	token, err := config.Token(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	identity := config.Identity{TokenFile: workerToken, WorkerID: workerID, ClientCertSHA256: []string{certFingerprint(t, files.workerCert)}}
	users := []config.Identity{{TokenFile: userToken, Owner: "user", Projects: []string{"p"}}}
	auth, err := NewAuth(users, []config.Identity{identity})
	if err != nil {
		t.Fatal(err)
	}
	auth.SetWorkerMTLSMode(mode)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	option, err := ServerTLS(listener.Addr().String(), config.TLS{CertFile: files.serverCert, KeyFile: files.serverKey, ClientCAFile: files.ca, WorkerMTLSMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(option, grpc.UnaryInterceptor(auth.Unary), grpc.StreamInterceptor(auth.Stream))
	hp.RegisterHealthServer(g, health.NewServer())
	done := make(chan error, 1)
	go func() { done <- g.Serve(listener) }()
	defer func() { g.Stop(); <-done }()
	clientTLS := config.TLS{CAFile: files.ca, CertFile: certPath, KeyFile: keyPath}
	conn, err := Dial(listener.Addr().String(), token, clientTLS)
	if err != nil {
		return codes.Unavailable
	}
	defer conn.Close()
	_, err = hp.NewHealthClient(conn).Check(context.Background(), &hp.HealthCheckRequest{})
	if err != nil {
		t.Logf("mTLS test RPC error: %v", err)
	}
	return status.Code(err)
}

func TestWorkerRequiresVerifiedClientCertificate(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "optional", "", "", "worker"); got != codes.Unauthenticated {
		t.Fatalf("worker without cert code=%v", got)
	}
}

func TestWorkerCertificateMustMatchIdentity(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.wrongSANCert, f.wrongSANKey, "worker"); got != codes.PermissionDenied {
		t.Fatalf("wrong SAN code=%v", got)
	}
}

func TestWorkerCertificateMatchesIdentity(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.workerCert, f.workerKey, "worker"); got != codes.OK {
		t.Fatalf("matching Worker certificate code=%v", got)
	}
}

func TestWorkerCertificateRevocationFingerprint(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.revokedCert, f.revokedKey, "worker"); got != codes.PermissionDenied {
		t.Fatalf("unlisted (revoked) Worker certificate code=%v", got)
	}
}

func TestUserGRPCCanConnectWithoutClientCertificate(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "optional", "", "", "user"); got != codes.OK {
		t.Fatalf("user without cert code=%v", got)
	}
}

func TestUserCertificateRejectedByDefault(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.workerCert, f.workerKey, "user"); got != codes.PermissionDenied {
		t.Fatalf("user client certificate code=%v", got)
	}
}

func TestWorkerCertificateRejectsUnknownCA(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.unknownCert, f.unknownKey, "worker"); got == codes.OK {
		t.Fatalf("unknown CA code=%v", got)
	}
}

func TestWorkerCertificateRejectsExpiredCertificate(t *testing.T) {
	f := mtlsFiles(t)
	if got := mtlsHealthCode(t, f, "required", f.expiredCert, f.expiredKey, "worker"); got == codes.OK {
		t.Fatalf("expired certificate code=%v", got)
	}
}

func certificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "computecloud-test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for file, b := range map[string][]byte{certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})} {
		if err = os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile
}

func TestTLSAndTokenAuthentication(t *testing.T) {
	cert, key := certificate(t)
	tokenFile := testutil.Token(t, "user")
	token, err := config.Token(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth([]config.Identity{{TokenFile: tokenFile, Owner: "owner", Projects: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	option, err := ServerTLS(listener.Addr().String(), config.TLS{CertFile: cert, KeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(option, grpc.UnaryInterceptor(auth.Unary), grpc.StreamInterceptor(auth.Stream))
	hp.RegisterHealthServer(server, health.NewServer())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { server.Stop(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name, token, ca string
		want            codes.Code
	}{{"trusted", token, cert, codes.OK}, {"bad-token", "wrong-token", cert, codes.Unauthenticated}, {"untrusted-ca", token, "", codes.Unavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			conn, e := Dial(listener.Addr().String(), tc.token, config.TLS{CAFile: tc.ca})
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close()
			_, e = hp.NewHealthClient(conn).Check(ctx, &hp.HealthCheckRequest{})
			if status.Code(e) != tc.want {
				t.Fatalf("got %v, want %v", e, tc.want)
			}
		})
	}
	t.Run("connector-inner-auth", func(t *testing.T) {
		var calls atomic.Int32
		connector := ConnectorFunc(func(ctx context.Context, address string) (net.Conn, error) {
			calls.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, "tcp", address)
		})
		for _, tc := range []struct {
			token, ca string
			want      codes.Code
		}{{token, cert, codes.OK}, {"wrong-token", cert, codes.Unauthenticated}, {token, "", codes.Unavailable}} {
			conn, e := DialWithConnector(listener.Addr().String(), tc.token, config.TLS{CAFile: tc.ca}, connector)
			if e != nil {
				t.Fatal(e)
			}
			_, e = hp.NewHealthClient(conn).Check(ctx, &hp.HealthCheckRequest{})
			conn.Close()
			if status.Code(e) != tc.want {
				t.Fatalf("connector auth=%v want=%v", e, tc.want)
			}
		}
		if calls.Load() < 3 {
			t.Fatal("connector was not used")
		}
		if conn, e := DialWithConnector(listener.Addr().String(), token, config.TLS{InsecureLoopback: true}, connector); e == nil {
			conn.Close()
			t.Fatal("plaintext alternate path accepted")
		}
		if conn, e := DialWithConnector(listener.Addr().String(), token, config.TLS{}, nil); e == nil {
			conn.Close()
			t.Fatal("nil connector accepted")
		}
	})
	for _, address := range []string{"0.0.0.0:7443", "10.1.1.1:7443", "localhost:7443"} {
		if _, e := ServerTLS(address, config.TLS{InsecureLoopback: true}); e == nil {
			t.Fatalf("plaintext accepted: %s", address)
		}
		if conn, e := Dial(address, token, config.TLS{InsecureLoopback: true}); e == nil {
			conn.Close()
			t.Fatalf("plaintext dial accepted: %s", address)
		}
	}
}

func TestConnectorCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	connector := ConnectorFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	conn, err := DialWithConnector("127.0.0.1:7443", "token", config.TLS{}, connector)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = hp.NewHealthClient(conn).Check(ctx, &hp.HealthCheckRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("cancellation=%v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connector not started")
	}
	conn.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("connector context leaked")
	}
}
