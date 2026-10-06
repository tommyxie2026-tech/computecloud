package server

import (
	"context"
	"testing"
)

func TestTransportTunnelRejectsPlaintext(t *testing.T) {
	s, _, _, _ := offlineJobServer(t)
	defer s.Close()
	if err := s.ServeTunnel(context.Background(), nil); err == nil {
		t.Fatal("plaintext tunnel accepted")
	}
}
