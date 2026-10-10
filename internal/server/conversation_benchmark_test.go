package server

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
)

func BenchmarkConversationHTTPS(b *testing.B) {
	key := b.TempDir() + "/token"
	if err := os.WriteFile(key, []byte("benchmark-token-012345678901234567890123456789"), 0600); err != nil {
		b.Fatal(err)
	}
	profile := config.ConversationProfile{ID: "bench", PublicModel: "m", ProjectID: "p"}
	s := &Server{cfg: config.Server{ConversationJobs: config.ConversationJobs{Enabled: true, Profiles: []config.ConversationProfile{profile}}}}
	auth, err := rpcutil.NewAuth([]config.Identity{{TokenFile: key, Owner: "bench", Projects: []string{"p"}, ConversationProfile: "bench", Scopes: []string{"conversations:submit", "conversations:read"}}}, nil)
	if err != nil {
		b.Fatal(err)
	}
	s.auth = auth
	ts := httptest.NewUnstartedServer(s.conversationHTTP(false))
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	ts.StartTLS()
	defer ts.Close()
	for _, cold := range []bool{true, false} {
		name := "reused"
		if cold {
			name = "cold"
		}
		b.Run(name, func(b *testing.B) {
			tr := ts.Client().Transport.(*http.Transport).Clone()
			tr.DisableKeepAlives = cold
			client := &http.Client{Transport: tr}
			defer tr.CloseIdleConnections()
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					req, _ := http.NewRequest(http.MethodGet, ts.URL+"/agent/v1/models", nil)
					req.Header.Set("Authorization", "Bearer benchmark-token-012345678901234567890123456789")
					res, err := client.Do(req)
					if err != nil {
						b.Error(err)
						return
					}
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						b.Errorf("status=%d", res.StatusCode)
						return
					}
				}
			})
		})
	}
}
