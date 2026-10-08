package relay

import (
	"context"
	"net"
	"sync"
)

// ConnectionListener hands already-paired opaque byte streams to gRPC. It
// never inspects their contents or owns Worker/Job state.
type ConnectionListener struct {
	addr        net.Addr
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func NewConnectionListener(addr net.Addr) *ConnectionListener {
	return &ConnectionListener{addr: addr, connections: make(chan net.Conn), closed: make(chan struct{})}
}
func (l *ConnectionListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *ConnectionListener) Offer(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return net.ErrClosed
	}
	select {
	case <-l.closed:
		_ = conn.Close()
		return net.ErrClosed
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	case l.connections <- conn:
		return nil
	}
}
func (l *ConnectionListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *ConnectionListener) Addr() net.Addr { return l.addr }
