package rpcutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
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
	"google.golang.org/grpc/status"
)

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
