package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"github.com/tommyxie2026-tech/computecloud/internal/worker"
)

type jobHarness struct {
	s                  *Server
	ctx                context.Context
	token, url, commit string
	http               *http.Client
}

func newJobHarness(t *testing.T, workers bool, options ...func(*config.Server)) *jobHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	t.Cleanup(cancel)
	repo, commit := testutil.Repository(t)
	user, other := testutil.Token(t, "job-user"), testutil.Token(t, "other")
	wtokens := []string{testutil.Token(t, "w1"), testutil.Token(t, "w2")}
	wc := config.Worker{DataDir: t.TempDir(), ID: "w1", Address: "placeholder", TokenFile: wtokens[0], TLS: config.TLS{InsecureLoopback: true}, Slots: 1, StopGraceMS: 100, Repositories: map[string]string{"repo": repo}, Policies: map[string]config.Policy{"review": {CodexSandbox: "read-only", ClaudePermissionMode: "dontAsk", ClaudeAllowedTools: []string{"Read"}}, "write": {CodexSandbox: "workspace-write", ClaudePermissionMode: "dontAsk", ClaudeAllowedTools: []string{"Read", "Edit"}}}, Verifiers: map[string][][]string{"check": {{"test", "-f", "README.md"}}}, Runtimes: map[string]config.Runtime{"codex_exec": {Executable: testutil.JobCLI(t), Version: "fixture-1", Models: []string{"model-c"}, Credentials: []string{"account"}}, "claude_print": {Executable: testutil.JobCLI(t), Version: "fixture-1", Models: []string{"model-a"}, Credentials: []string{"account"}}}}
	cfg := config.Server{DataDir: t.TempDir(), TLS: wc.TLS, LeaseSeconds: 6, TickMS: 25, MaxArtifactBytes: 32 << 20, MaxProjectTasks: 8, Credentials: map[string]int{"account": 2}, Users: []config.Identity{{TokenFile: user, Owner: "owner", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:submit", "jobs:read", "jobs:cancel"}}, {TokenFile: other, Owner: "other", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:read"}}}, Jobs: config.Jobs{Enabled: true, Templates: config.Templates(wc)}, MCP: config.MCP{Enabled: true}}
	for i, tok := range wtokens {
		cfg.Workers = append(cfg.Workers, config.Identity{TokenFile: tok, WorkerID: []string{"w1", "w2"}[i], Projects: []string{"project"}, Credentials: []string{"account"}})
	}
	for _, option := range options {
		option(&cfg)
	}
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	sc, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Serve(sc, l) }()
	t.Cleanup(func() {
		stop()
		if e := <-done; e != nil {
			t.Error(e)
		}
		s.Close()
	})
	hs := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(hs.Close)
	tok, _ := config.Token(user)
	auth, e := s.auth.Bearer(ctx, "Bearer "+tok)
	if e != nil {
		t.Fatal(e)
	}
	h := &jobHarness{s, auth, tok, hs.URL, commit, hs.Client()}
	if workers {
		for i, tok := range wtokens {
			c := wc
			c.ID = []string{"w1", "w2"}[i]
			c.DataDir = t.TempDir()
			c.TokenFile = tok
			c.Address = l.Addr().String()
			w, e := worker.New(c)
			if e != nil {
				t.Fatal(e)
			}
			wctx, wstop := context.WithCancel(ctx)
			wdone := make(chan error, 1)
			go func() { wdone <- w.Run(wctx) }()
			t.Cleanup(func() {
				wstop()
				if e := <-wdone; e != nil {
					t.Error(e)
				}
				w.Close()
			})
		}
	}
	return h
}
func (h *jobHarness) spec(mode string) job.Spec {
	ex := job.Execution{Engine: "codex", RuntimeProfile: "codex_exec", Model: "model-c", CredentialRef: "account", PolicyRef: "review", AcceptanceProfile: "check"}
	s := job.Spec{SchemaVersion: "v0.2", ProjectID: "project", Mode: "single", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: h.commit}, Input: job.Input{Text: "fixture job"}, Execution: &ex, Limits: job.Limits{TimeoutSeconds: 50, MaxAttemptsPerTask: 1}}
	if mode != "single" {
		s.Mode = "map_reduce"
		s.Execution = nil
		ae := ex
		ae.Engine = "claude"
		ae.RuntimeProfile = "claude_print"
		ae.Model = "model-a"
		s.Map = &job.Map{Parallelism: 2, Partitions: []job.Partition{{Key: "a", ScopePaths: []string{"README.md"}, Input: job.Input{Text: "review a"}, Execution: ex}, {Key: "b", ScopePaths: []string{"README.md"}, Input: job.Input{Text: "review b"}, Execution: ae}}}
		s.Reduce = &job.Reduce{Strategy: mode, Input: job.Input{Text: "merge"}, Execution: ex}
		if mode == "patch_merge_v1" {
			for i := range s.Map.Partitions {
				s.Map.Partitions[i].Execution.PolicyRef = "write"
				s.Map.Partitions[i].ScopePaths = []string{[]string{"a.txt", "b.txt"}[i]}
			}
		}
	}
	return s
}
func (h *jobHarness) request(t *testing.T, method, path, key string, body []byte) (int, []byte) {
	t.Helper()
	r, e := http.NewRequestWithContext(h.ctx, method, h.url+path, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+h.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	res, e := h.http.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	return res.StatusCode, b
}
func (h *jobHarness) wait(t *testing.T, id string) *Job {
	t.Helper()
	for {
		j, e := h.s.GetJob(h.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if terminal(j.State) {
			return j
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("job timeout: %+v", j)
		case <-time.After(30 * time.Millisecond):
		}
	}
}
func TestJobHTTPMCPAndTwoWorkerStrategies(t *testing.T) {
	h := newJobHarness(t, true)
	for _, mode := range []string{"single", "report_merge_v1", "patch_merge_v1"} {
		t.Run(mode, func(t *testing.T) {
			spec := h.spec(mode)
			raw := job.JSON(spec)
			code, b := h.request(t, "POST", "/v1/jobs", mode, raw)
			if code != 202 {
				t.Fatalf("submit: %d %s", code, b)
			}
			var j Job
			if e := json.Unmarshal(b, &j); e != nil {
				t.Fatal(e)
			}
			code, b = h.request(t, "POST", "/v1/jobs", mode, raw)
			if code != 200 || !bytes.Contains(b, []byte(j.ID)) {
				t.Fatalf("replay: %d %s", code, b)
			}
			spec.Input.Text = "changed"
			code, b = h.request(t, "POST", "/v1/jobs", mode, job.JSON(spec))
			if code != 409 {
				t.Fatalf("conflict: %d %s", code, b)
			}
			j2 := h.wait(t, j.ID)
			if j2.State != "SUCCEEDED" {
				res, _ := h.s.JobResult(h.ctx, j.ID)
				t.Fatalf("job failed: %+v %s", j2, res)
			}
			res, e := h.s.JobResult(h.ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			var result struct {
				Final []struct {
					ID string `json:"artifact_id"`
				} `json:"final_artifacts"`
			}
			if e = json.Unmarshal(res, &result); e != nil || len(result.Final) != 1 {
				t.Fatalf("result: %s %v", res, e)
			}
			code, b = h.request(t, "GET", "/v1/jobs/"+j.ID+"/artifacts/"+result.Final[0].ID, "", nil)
			if code != 200 {
				t.Fatalf("download: %d %s", code, b)
			}
			members := map[string][]byte{}
			tr := tar.NewReader(bytes.NewReader(b))
			for {
				head, e := tr.Next()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				members[head.Name], e = io.ReadAll(tr)
				if e != nil {
					t.Fatal(e)
				}
			}
			if mode == "report_merge_v1" && !bytes.Contains(members["review.json"], []byte(`"sources"`)) {
				t.Fatalf("missing review provenance: %s", members["review.json"])
			}
			if mode == "patch_merge_v1" && (!bytes.Contains(members["changes.patch"], []byte("a.txt")) || !bytes.Contains(members["changes.patch"], []byte("b.txt")) || members["merge-report.json"] == nil) {
				t.Fatal("missing merged patches")
			}
			if mode != "single" {
				var maps, reducers, nodes int
				h.s.db.SQL.QueryRow("SELECT count(*),count(DISTINCT worker) FROM tasks WHERE job_id=? AND stage='map'", j.ID).Scan(&maps, &nodes)
				h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=? AND stage='reduce'", j.ID).Scan(&reducers)
				if maps != 2 || nodes != 2 || reducers != 1 {
					t.Fatalf("maps/nodes/reducers: %d %d %d", maps, nodes, reducers)
				}
			}
			ev, e := h.s.JobEvents(h.ctx, j.ID, 0, 500)
			if e != nil {
				t.Fatal(e)
			}
			for i, x := range ev.Events {
				if x.Seq != int64(i+1) {
					t.Fatal("event gap")
				}
			}
		})
	}
	// Real MCP initialization, discovery and a tool call over Streamable HTTP.
	code, b := h.request(t, "POST", "/mcp", "", []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	if code != 200 || !bytes.Contains(b, []byte("protocolVersion")) {
		t.Fatalf("MCP init: %d %s", code, b)
	}
	code, b = h.request(t, "POST", "/mcp", "", []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	if code != 200 || !bytes.Contains(b, []byte("submit_job")) {
		t.Fatalf("MCP list: %d %s", code, b)
	}
	args := map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "submit_job", "arguments": map[string]any{"idempotency_key": "mcp-submit", "spec": h.spec("single")}}}
	code, b = h.request(t, "POST", "/mcp", "", job.JSON(args))
	if code != 200 || !bytes.Contains(b, []byte("structuredContent")) || bytes.Contains(b, []byte(`"isError":true`)) {
		t.Fatalf("MCP submit: %d %s", code, b)
	}
}
func TestJobRejectsInvalidOutputAndCancellation(t *testing.T) {
	h := newJobHarness(t, true)
	for _, bad := range []string{"bad-evidence", "out-of-scope", "mutate-reduce"} {
		mode := "report_merge_v1"
		if bad != "bad-evidence" {
			mode = "patch_merge_v1"
		}
		spec := h.spec(mode)
		if bad == "mutate-reduce" {
			spec.Reduce.Input.Text = bad
		} else {
			spec.Map.Partitions[0].Input.Text = bad
		}
		j, e := h.s.SubmitJob(h.ctx, bad, job.JSON(spec))
		if e != nil {
			t.Fatal(e)
		}
		got := h.wait(t, j.ID)
		if got.State != "FAILED" {
			t.Fatalf("invalid output accepted: %+v", got)
		}
		if bad != "mutate-reduce" {
			var n int
			h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=? AND stage='reduce'", j.ID).Scan(&n)
			if n != 0 {
				t.Fatal("reduce created despite failed map")
			}
		}
	}
	spec := h.spec("report_merge_v1")
	spec.Input.Text = "slow-job"
	j, e := h.s.SubmitJob(h.ctx, "cancel", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	for {
		var n int
		h.s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=? AND state='RUNNING'", j.ID).Scan(&n)
		if n > 0 {
			break
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("not running")
		case <-time.After(30 * time.Millisecond):
		}
	}
	cancel := CancelJobRequest{ControlID: "cancel-1", Reason: "fixture"}
	j, e = h.s.CancelJob(h.ctx, j.ID, cancel)
	if e != nil || j.State != "STOPPING" {
		t.Fatalf("cancel intent: %+v %v", j, e)
	}
	got := h.wait(t, j.ID)
	if got.State != "CANCELED" {
		t.Fatalf("cancel result: %+v", got)
	}
	var live int
	h.s.db.SQL.QueryRow("SELECT count(*) FROM attempts a JOIN tasks t ON a.task=t.id WHERE t.job_id=? AND a.released=0", j.ID).Scan(&live)
	if live != 0 {
		t.Fatal("terminal job before cleanup")
	}
}
func TestJobValidationAndIsolation(t *testing.T) {
	h := newJobHarness(t, false)
	spec := h.spec("single")
	raw := job.JSON(spec)
	for _, body := range [][]byte{append([]byte(`{"mode":"single",`), raw[1:]...), []byte(strings.Replace(string(raw), `"max_attempts_per_task":1`, `"max_attempts_per_task":2`, 1)), append(raw, []byte(` {}`)...)} {
		code, b := h.request(t, "POST", "/v1/jobs", "invalid", body)
		if code != 400 {
			t.Fatalf("accepted invalid JSON: %d %s", code, b)
		}
	}
	j, e := h.s.SubmitJob(h.ctx, "isolate", raw)
	if e != nil {
		t.Fatal(e)
	}
	tok, _ := config.Token(h.s.cfg.Users[1].TokenFile)
	old := h.token
	h.token = tok
	code, _ := h.request(t, "GET", "/v1/jobs/"+j.ID, "", nil)
	h.token = old
	if code != 404 {
		t.Fatalf("cross-owner read: %d", code)
	}
	_, e = h.s.CancelJob(h.ctx, j.ID, CancelJobRequest{ControlID: "same"})
	if e != nil {
		t.Fatal(e)
	}
	got := h.wait(t, j.ID)
	if got.State != "CANCELED" {
		t.Fatal(got.State)
	}
	var attempts int
	h.s.db.SQL.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts)
	if attempts != 0 {
		t.Fatal("queued cancel dispatched work")
	}
}

func TestMCPProtocolAndOriginBoundaries(t *testing.T) {
	h := newJobHarness(t, false)
	for _, method := range []string{"GET", "DELETE"} {
		code, b := h.request(t, method, "/mcp", "", nil)
		if code != 405 {
			t.Fatalf("MCP %s: %d %s", method, code, b)
		}
	}
	code, b := h.request(t, "POST", "/mcp", "", []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if code != 202 {
		t.Fatalf("notification: %d %s", code, b)
	}
	j, e := h.s.SubmitJob(h.ctx, "unfinished", job.JSON(h.spec("single")))
	if e != nil {
		t.Fatal(e)
	}
	call := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "get_result", "arguments": map[string]string{"job_id": j.ID}}}
	code, b = h.request(t, "POST", "/mcp", "", job.JSON(call))
	if code != 200 || !bytes.Contains(b, []byte(`"isError":true`)) || !bytes.Contains(b, []byte("JOB_NOT_FINISHED")) {
		t.Fatalf("business error: %d %s", code, b)
	}
	for _, headers := range []map[string]string{{"Origin": "https://untrusted.invalid"}, {"MCP-Protocol-Version": "1900-01-01"}, {"Authorization": "Bearer invalid"}} {
		r, _ := http.NewRequest("POST", h.url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
		r.Header.Set("Authorization", "Bearer "+h.token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		res, e := h.http.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Fatalf("invalid protocol/auth/origin accepted: %v", headers)
		}
	}
}
