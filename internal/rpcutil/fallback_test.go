package rpcutil

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDirectFirstFallbackAndCancellation(t *testing.T) {
	t.Run("direct-wins", func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		fallback := false
		c, e := NewDirectFirstConnector(ConnectorFunc(func(context.Context, string) (net.Conn, error) { return a, nil }), ConnectorFunc(func(context.Context, string) (net.Conn, error) { fallback = true; return nil, errors.New("unexpected") }), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		conn, e := c.Connect(context.Background(), "server")
		if e != nil || fallback {
			t.Fatalf("direct=%v fallback=%v", e, fallback)
		}
		conn.Close()
		if c.Stats().DirectConnections != 1 {
			t.Fatal(c.Stats())
		}
	})
	t.Run("timeout-falls-back-once", func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		fallback := 0
		c, e := NewDirectFirstConnector(ConnectorFunc(func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }), ConnectorFunc(func(context.Context, string) (net.Conn, error) { fallback++; return a, nil }), 10*time.Millisecond)
		if e != nil {
			t.Fatal(e)
		}
		conn, e := c.Connect(context.Background(), "server")
		if e != nil || fallback != 1 {
			t.Fatalf("fallback=%d %v", fallback, e)
		}
		conn.Close()
		if c.Stats().FallbackAttempts != 1 {
			t.Fatal(c.Stats())
		}
	})
	t.Run("caller-cancellation-never-falls-back", func(t *testing.T) {
		fallback := false
		c, e := NewDirectFirstConnector(ConnectorFunc(func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }), ConnectorFunc(func(context.Context, string) (net.Conn, error) { fallback = true; return nil, errors.New("unexpected") }), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, e = c.Connect(ctx, "server"); !errors.Is(e, context.DeadlineExceeded) || fallback {
			t.Fatalf("canceled=%v fallback=%v", e, fallback)
		}
	})
	t.Run("alternate-error-is-final", func(t *testing.T) {
		calls := 0
		c, e := NewDirectFirstConnector(ConnectorFunc(func(context.Context, string) (net.Conn, error) { return nil, errors.New("direct unavailable") }), ConnectorFunc(func(context.Context, string) (net.Conn, error) { calls++; return nil, errors.New("relay unavailable") }), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if conn, e := c.Connect(context.Background(), "server"); e == nil || conn != nil || calls != 1 {
			t.Fatalf("unbounded fallback=%d %v", calls, e)
		}
		if c.Stats().FailedConnections != 1 {
			t.Fatal(c.Stats())
		}
	})
}
