package rpcutil

import (
	"context"
	"fmt"
	"net"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"google.golang.org/grpc"
)

// Connector opens an opaque byte stream. gRPC still performs the inner TLS
// handshake against the original Server address and attaches the existing token.
// A connector must honor cancellation and never replay application bytes.
type Connector interface {
	Connect(context.Context, string) (net.Conn, error)
}
type ConnectorFunc func(context.Context, string) (net.Conn, error)

func (f ConnectorFunc) Connect(ctx context.Context, address string) (net.Conn, error) {
	return f(ctx, address)
}

// DialWithConnector is the opt-in transport seam. Alternate paths never permit
// plaintext, even when their outer connection terminates on a loopback address.
func DialWithConnector(address, token string, t config.TLS, c Connector) (*grpc.ClientConn, error) {
	if c == nil {
		return nil, fmt.Errorf("transport connector required")
	}
	if t.InsecureLoopback {
		return nil, fmt.Errorf("alternate transport requires inner TLS")
	}
	return dial(address, token, t, grpc.WithContextDialer(c.Connect))
}
