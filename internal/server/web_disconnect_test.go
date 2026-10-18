package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWebConcurrentCanceledSnapshotsReleaseDatabase(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) {
		c.HTTP.RateLimit = config.RequestLimit{RatePerMinute: 1000000, Burst: 100000, MaxEntries: 10000, IdleTimeoutSeconds: 600}
	})
	b, _ := json.Marshal(h.spec("single"))
	job, err := h.s.SubmitJob(h.ctx, "disconnect-db", b)
	if err != nil {
		t.Fatal(err)
	}
	a := testWebAuth(t)
	a.identity = h.s.cfg.Users[0]
	h.s.webAuth = a
	_, cookie, csrf := loginWeb(t, a, "admin")
	hs := httptest.NewServer(h.s.HTTPHandler())
	defer hs.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		for _, suffix := range []string{"", "/tasks", "/sessions", "/approvals", "/artifacts", "/goal", "/events?after_seq=0&limit=500"} {
			wg.Add(1)
			go func(i int, suffix string) {
				defer wg.Done()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r, _ := http.NewRequestWithContext(ctx, "GET", hs.URL+"/v1/jobs/"+job.ID+suffix, nil)
				r.AddCookie(cookie)
				if i%2 == 0 {
					time.AfterFunc(time.Duration(i%3+1)*time.Millisecond, cancel)
				}
				response, e := hs.Client().Do(r)
				if e == nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			}(i, suffix)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("requests stalled: %+v", h.s.db.SQL.Stats())
	}
	request := httptest.NewRequest("POST", "https://control.example/v1/jobs/"+job.ID+"/control-lease", bytes.NewBufferString(`{"holder_id":"after-disconnect"}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://control.example")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder := httptest.NewRecorder()
	h.s.cfg.Users[0].Scopes = append(h.s.cfg.Users[0].Scopes, "jobs:control")
	a.identity = h.s.cfg.Users[0]
	h.s.HTTPHandler().ServeHTTP(recorder, request)
	if recorder.Code != 201 {
		t.Fatalf("control after disconnect: %d %s", recorder.Code, recorder.Body.String())
	}
}
