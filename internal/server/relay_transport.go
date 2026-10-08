package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/relay"
)

// ServeWithConfiguredRelay is the explicit opt-in Server path. The ticket
// issuer and byte broker only provide routing; ServeWithTunnel keeps a single
// authoritative gRPC Server and execution store.
func (s *Server) ServeWithConfiguredRelay(parent context.Context, direct net.Listener) error {
	if !s.cfg.Transport.Enabled() {
		return errors.New("Relay transport is not enabled")
	}
	if direct == nil {
		return errors.New("direct listener required")
	}
	client, err := relay.NewTicketClient(s.cfg.Transport.RelayAddress, s.cfg.Transport.IssuerURL,
		s.cfg.Transport.TokenFile, s.cfg.Transport.CAFile, s.cfg.Transport.ServerName)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	tunnel := relay.NewConnectionListener(direct.Addr())
	defer tunnel.Close()
	var wg sync.WaitGroup
	for _, identity := range s.cfg.Workers {
		id := identity.WorkerID
		wg.Add(1)
		go func() { defer wg.Done(); s.relayPeerLoop(ctx, id, client, tunnel) }()
	}
	err = s.ServeWithTunnel(ctx, direct, tunnel)
	cancel()
	_ = tunnel.Close()
	wg.Wait()
	return err
}

func (s *Server) relayPeerLoop(ctx context.Context, workerID string, client *relay.TicketClient, tunnel *relay.ConnectionListener) {
	for ctx.Err() == nil {
		s.mu.Lock()
		active := s.peers[workerID] != nil
		s.mu.Unlock()
		if active {
			_ = wait(ctx, 1000)
			continue
		}
		ticket, err := client.Issue(ctx, workerID)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Relay pair issue failed", "worker", workerID, "error", err)
			}
			_ = wait(ctx, 2000)
			continue
		}
		conn, err := client.Connect(ctx, ticket)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Relay pair connection failed", "worker", workerID, "error", err)
			}
			_ = wait(ctx, 1000)
			continue
		}
		if err := tunnel.Offer(ctx, conn); err != nil {
			return
		}
		// Give the inner authenticated Worker stream time to register. If it
		// never does, issue a fresh short-lived pair instead of assuming a
		// successful outer connection proves Worker identity.
		deadline := time.Now().Add(3 * time.Second)
		for ctx.Err() == nil && time.Now().Before(deadline) {
			s.mu.Lock()
			active = s.peers[workerID] != nil
			s.mu.Unlock()
			if active {
				break
			}
			_ = wait(ctx, 100)
		}
		if !active {
			_ = conn.Close()
		}
	}
}
