package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
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

func TestJobGraphCompileRequiresNoClientConnection(t *testing.T) {
	if e := runJob(context.Background(), config.Client{}, jobOptions{op: "compile-graph", file: "../../examples/iu-graph/orders.json"}); e != nil {
		t.Fatal(e)
	}
}

func TestJobGraphCLIProposalAcquiresAndReleasesControlLease(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[rejected], func(t *testing.T) {
			dir := t.TempDir()
			token := filepath.Join(dir, "token")
			input := filepath.Join(dir, "proposal.json")
			os.WriteFile(token, []byte("test-token-01234567890123456789"), 0600)
			os.WriteFile(input, []byte(`{"proposal":{}}`), 0600)
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch {
				case r.Method == "POST" && r.URL.Path == "/v1/jobs/source/control-lease":
					w.WriteHeader(201)
					w.Write([]byte(`{"lease_token":"opaque-fixture-token"}`))
				case r.Method == "DELETE" && r.URL.Path == "/v1/jobs/source/control-lease":
					if r.Header.Get("X-Control-Lease") != "opaque-fixture-token" {
						t.Error("missing release token")
					}
					w.WriteHeader(204)
				case r.Method == "POST" && r.URL.Path == "/v1/jobs/source/goal/replans":
					if r.Header.Get("X-Control-Lease") != "opaque-fixture-token" {
						t.Error("proposal bypassed lease")
					}
					if rejected {
						http.Error(w, "IMPORT_GRANT_NOT_USABLE", 412)
					} else {
						w.Write([]byte(`{"decision":{"allowed":true}}`))
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			e := runJob(context.Background(), config.Client{HTTPURL: server.URL, TokenFile: token, TLS: config.TLS{InsecureLoopback: true}}, jobOptions{op: "propose-graph", id: "source", file: input})
			if (e != nil) != rejected {
				t.Fatalf("error=%v rejected=%v", e, rejected)
			}
			if len(calls) != 3 || calls[0] != "POST /v1/jobs/source/control-lease" || calls[2] != "DELETE /v1/jobs/source/control-lease" {
				t.Fatalf("lease lifecycle: %v", calls)
			}
		})
	}
}
func TestCoverageCompileCLIReturnsGapWithoutConnection(t *testing.T) {
	raw, e := os.ReadFile("../../examples/iu-graph/coverage-v5/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	s, e := graph.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	s.Nodes[0].Spans = nil
	path := filepath.Join(t.TempDir(), "gap.json")
	os.WriteFile(path, job.JSON(s), 0600)
	old := os.Stdout
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	os.Stdout = w
	err := runJob(context.Background(), config.Client{}, jobOptions{op: "compile-graph", file: path})
	w.Close()
	os.Stdout = old
	defer r.Close()
	var ce *graph.CoverageError
	if !errors.As(err, &ce) {
		t.Fatal("not coverage preflight", err)
	}
	out, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	var report graph.CoverageError
	if e = json.Unmarshal(out, &report); e != nil || report.Report.Complete || len(report.Report.Gaps) == 0 {
		t.Fatal(string(out), e)
	}
}
