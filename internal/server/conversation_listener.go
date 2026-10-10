package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

func (s *Server) serveConversationHTTP(ctx context.Context, l net.Listener) error {
	h := &http.Server{Handler: s.conversationHTTP(true), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	h.BaseContext = func(net.Listener) context.Context { return ctx }
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = h.Shutdown(shutdown)
			_ = h.Close()
		case <-done:
		}
	}()
	defer func() { close(done); <-stopped }()
	err := h.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
