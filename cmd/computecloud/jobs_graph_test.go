package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

func TestJobGraphCLIUsesVersionedSubmissionAndStatus(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	input := filepath.Join(dir, "graph.json")
	os.WriteFile(token, []byte("test-token-01234567890123456789"), 0600)
	os.WriteFile(input, []byte(`{"version":"iu-graph-v1"}`), 0600)
	expectedMethod, expectedPath := "POST", "/v1/graphs"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != expectedMethod || r.URL.Path != expectedPath || r.Header.Get("Authorization") != "Bearer test-token-01234567890123456789" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Method == "POST" {
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != `{"version":"iu-graph-v1"}` || r.Header.Get("Idempotency-Key") != "key" {
				t.Errorf("submission %s", raw)
			}
		}
		w.Write([]byte(`{"state":"QUEUED"}`))
	}))
	defer server.Close()
	cfg := config.Client{HTTPURL: server.URL, TokenFile: token, TLS: config.TLS{InsecureLoopback: true}}
	if e := runJob(context.Background(), cfg, jobOptions{op: "submit-graph", file: input, key: "key"}); e != nil {
		t.Fatal(e)
	}
	expectedMethod = "GET"
	expectedPath = "/v1/jobs/id/graph"
	if e := runJob(context.Background(), cfg, jobOptions{op: "graph", id: "id"}); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestJobGraphCLIImportEndpoints(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	input := filepath.Join(dir, "grant.json")
	os.WriteFile(token, []byte("test-token-01234567890123456789"), 0600)
	os.WriteFile(input, []byte(`{"id":"grant_a"}`), 0600)
	expectedMethod, expectedPath := "POST", "/v1/jobs/source/graph/import-grants"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != expectedMethod || r.URL.Path != expectedPath {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	cfg := config.Client{HTTPURL: server.URL, TokenFile: token, TLS: config.TLS{InsecureLoopback: true}}
	if e := runJob(context.Background(), cfg, jobOptions{op: "issue-import", id: "source", file: input}); e != nil {
		t.Fatal(e)
	}
	expectedMethod = "GET"
	expectedPath += "/grant_a"
	if e := runJob(context.Background(), cfg, jobOptions{op: "get-import", id: "source", key: "grant_a"}); e != nil {
		t.Fatal(e)
	}
	expectedMethod = "POST"
	expectedPath += "/revoke"
	if e := runJob(context.Background(), cfg, jobOptions{op: "revoke-import", id: "source", key: "grant_a"}); e != nil {
		t.Fatal(e)
	}
}
