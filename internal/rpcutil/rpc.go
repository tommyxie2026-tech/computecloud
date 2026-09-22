package rpcutil

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Principal struct {
	Identity config.Identity
	Worker   bool
}
type contextKey struct{}
type Auth struct{ identities map[[32]byte]Principal }

func NewAuth(users, workers []config.Identity) (*Auth, error) {
	a := &Auth{identities: make(map[[32]byte]Principal)}
	for kind, ids := range [][]config.Identity{users, workers} {
		for _, id := range ids {
			token, e := config.Token(id.TokenFile)
			if e != nil {
				return nil, e
			}
			key := sha256.Sum256([]byte(token))
			if _, ok := a.identities[key]; ok {
				return nil, fmt.Errorf("duplicate token identity")
			}
			if kind == 0 && (id.Owner == "" || len(id.Projects) == 0) {
				return nil, fmt.Errorf("user owner/projects required")
			}
			if kind == 1 && id.WorkerID == "" {
				return nil, fmt.Errorf("worker_id required")
			}
			a.identities[key] = Principal{Identity: id, Worker: kind == 1}
		}
	}
	return a, nil
}
func (a *Auth) Context(ctx context.Context) (context.Context, error) {
	m, _ := metadata.FromIncomingContext(ctx)
	v := m.Get("authorization")
	if len(v) != 1 || !strings.HasPrefix(v[0], "Bearer ") {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	p, ok := a.identities[sha256.Sum256([]byte(strings.TrimPrefix(v[0], "Bearer ")))]
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	return context.WithValue(ctx, contextKey{}, p), nil
}
func User(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	if !ok || p.Worker {
		return p, status.Error(codes.PermissionDenied, "user identity required")
	}
	return p, nil
}
func Worker(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	if !ok || !p.Worker {
		return p, status.Error(codes.PermissionDenied, "worker identity required")
	}
	return p, nil
}
func (a *Auth) Unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	ctx, e := a.Context(ctx)
	if e != nil {
		return nil, e
	}
	return next(ctx, req)
}

type authStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authStream) Context() context.Context { return s.ctx }
func (a *Auth) Stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	ctx, e := a.Context(ss.Context())
	if e != nil {
		return e
	}
	return next(srv, &authStream{ServerStream: ss, ctx: ctx})
}
func Loopback(address string) bool {
	h, _, e := net.SplitHostPort(address)
	if e != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
func ServerTLS(address string, t config.TLS) (grpc.ServerOption, error) {
	if t.InsecureLoopback {
		if !Loopback(address) {
			return nil, fmt.Errorf("plaintext allowed only on literal loopback address")
		}
		return grpc.Creds(insecure.NewCredentials()), nil
	}
	cert, e := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if e != nil {
		return nil, e
	}
	return grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})), nil
}

type tokenCred struct {
	token  string
	secure bool
}

func (c tokenCred) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}
func (c tokenCred) RequireTransportSecurity() bool { return c.secure }
func Dial(address, token string, t config.TLS) (*grpc.ClientConn, error) {
	var transport credentials.TransportCredentials
	if t.InsecureLoopback {
		if !Loopback(address) {
			return nil, fmt.Errorf("plaintext allowed only on literal loopback address")
		}
		transport = insecure.NewCredentials()
	} else {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if t.CAFile != "" {
			pem, e := os.ReadFile(t.CAFile)
			if e != nil {
				return nil, e
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid CA certificate")
			}
			tc.RootCAs = pool
		}
		transport = credentials.NewTLS(tc)
	}
	return grpc.NewClient(address, grpc.WithTransportCredentials(transport), grpc.WithPerRPCCredentials(tokenCred{token: token, secure: !t.InsecureLoopback}), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20), grpc.MaxCallSendMsgSize(8<<20)))
}
