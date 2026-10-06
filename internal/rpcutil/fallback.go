package rpcutil

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// DirectFirstConnector makes one direct byte-connection attempt, then at most
// one alternate attempt. TLS/identity authentication happens after this method
// inside gRPC, so an authentication failure can never trigger fallback here.
// It is opt-in; the ordinary Dial path and its resolver/proxy behavior are unchanged.
type DirectFirstConnector struct {
	direct    Connector
	alternate Connector
	timeout   time.Duration
	directOK  atomic.Uint64
	fallback  atomic.Uint64
	failed    atomic.Uint64
}
type TransportStats struct {
	DirectConnections uint64 `json:"direct_connections"`
	FallbackAttempts  uint64 `json:"fallback_attempts"`
	FailedConnections uint64 `json:"failed_connections"`
}

func NewDirectFirstConnector(direct, alternate Connector, timeout time.Duration) (*DirectFirstConnector, error) {
	if direct == nil || alternate == nil || timeout < time.Millisecond || timeout > 10*time.Second {
		return nil, errors.New("explicit connectors and bounded direct timeout required")
	}
	return &DirectFirstConnector{direct: direct, alternate: alternate, timeout: timeout}, nil
}
func (c *DirectFirstConnector) Connect(ctx context.Context, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directCtx, cancel := context.WithTimeout(ctx, c.timeout)
	conn, err := c.direct.Connect(directCtx, address)
	cancel()
	if err == nil && conn != nil {
		if e := ctx.Err(); e != nil {
			conn.Close()
			return nil, e
		}
		c.directOK.Add(1)
		return conn, nil
	}
	if conn != nil {
		conn.Close()
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	c.fallback.Add(1)
	conn, err = c.alternate.Connect(ctx, address)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		c.failed.Add(1)
		return nil, err
	}
	if conn == nil {
		c.failed.Add(1)
		return nil, errors.New("alternate connector returned no connection")
	}
	if e := ctx.Err(); e != nil {
		conn.Close()
		return nil, e
	}
	return conn, nil
}
func (c *DirectFirstConnector) Stats() TransportStats {
	return TransportStats{c.directOK.Load(), c.fallback.Load(), c.failed.Load()}
}
