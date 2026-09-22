package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func offlineJobServer(t *testing.T) (*Server, context.Context, context.Context, *session) {
	t.Helper()
	u, w := testutil.Token(t, "u"), testutil.Token(t, "w")
	wc := config.Worker{Runtimes: map[string]config.Runtime{"codex_exec": {Version: "fixture-1"}, "claude_print": {Version: "fixture-1"}}, Policies: map[string]config.Policy{"review": {CodexSandbox: "read-only", ClaudePermissionMode: "dontAsk", ClaudeAllowedTools: []string{"Read"}}}, Verifiers: map[string][][]string{"check": {}}}
	cfg := config.Server{DataDir: t.TempDir(), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 60, TickMS: 20, MaxArtifactBytes: 32 << 20, MaxProjectTasks: 8, Credentials: map[string]int{"account": 1}, Jobs: config.Jobs{Enabled: true, Templates: config.Templates(wc)}, Users: []config.Identity{{TokenFile: u, Owner: "owner", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:submit", "jobs:read", "jobs:cancel"}}}, Workers: []config.Identity{{TokenFile: w, WorkerID: "w", Projects: []string{"project"}, Credentials: []string{"account"}}}}
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ut, _ := config.Token(u)
	wt, _ := config.Token(w)
	uc, _ := s.auth.Bearer(context.Background(), "Bearer "+ut)
	wctx, _ := s.auth.Bearer(context.Background(), "Bearer "+wt)
	hello := &pb.WorkerHello{WorkerId: "w", Epoch: "e", Slots: 1}
	for _, profile := range []string{"codex_exec", "claude_print"} {
		digests := map[string]string{}
		for _, template := range cfg.Jobs.Templates {
			digests[template.Key()] = template.Digest
		}
		hello.Runtimes = append(hello.Runtimes, &pb.Runtime{Profile: profile, Models: []string{"model-c", "model-a"}, Credentials: []string{"account"}, Repositories: []string{"repo"}, Policies: []string{"review"}, Verifiers: []string{"check"}, Capabilities: []string{"event_stream", "cancel", "job_io_v1", "artifact_inputs_v1"}, TemplateDigests: digests})
	}
	if _, e = s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", "w", "e", encode(hello), store.Now()); e != nil {
		t.Fatal(e)
	}
	return s, uc, wctx, &session{hello: hello, identity: cfg.Workers[0]}
}
func registerResult(t *testing.T, s *Server, task, attempt string) string {
	t.Helper()
	id := store.ID()
	b := []byte("fixture result")
	dir := filepath.Join(s.cfg.DataDir, "artifacts")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, id), b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.SQL.Exec("INSERT INTO artifacts VALUES(?,?,?,?,?,?,?)", id, task, attempt, "result-bundle", job.Hash(b), len(b), id); e != nil {
		t.Fatal(e)
	}
	return id
}
func completeFake(t *testing.T, s *Server, ctx context.Context, task string) string {
	t.Helper()
	ref := new(pb.AttemptRef)
	if e := s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=?", task).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); e != nil {
		t.Fatal(e)
	}
	id := registerResult(t, s, task, ref.AttemptId)
	if _, e := s.CompleteAttempt(ctx, &pb.CompleteRequest{Attempt: ref, Success: true, CleanupConfirmed: true, ArtifactIds: []string{id}}); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestJobBarrierRestartInputAuthorizationAndCancelReplay(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer func() { s.Close() }()
	cfg := s.cfg
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	j, e := s.SubmitJob(uc, "restart", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	children, e := jobChildren(uc, s.db.SQL, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	var selected []string
	for _, c := range children {
		if e = s.assign(uc, c.id, []*session{peer}); e != nil {
			t.Fatal(e)
		}
		var aid string
		s.db.SQL.QueryRow("SELECT attempt FROM tasks WHERE id=?", c.id).Scan(&aid)
		registerResult(t, s, c.id, aid)
		selected = append(selected, completeFake(t, s, wc, c.id))
	}
	reopen := func() {
		t.Helper()
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		s, e = New(cfg)
		if e != nil {
			t.Fatal(e)
		}
	}
	reopen()
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	first, e := readJob(uc, s.db.SQL, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if first.State != "REDUCING" {
		t.Fatal(first.State)
	}
	for _, id := range selected {
		if !strings.Contains(string(first.manifest), id) {
			t.Fatal("manifest lost completion-selected artifact")
		}
	}
	var reduce string
	if e = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=? AND stage='reduce'", j.ID).Scan(&reduce); e != nil {
		t.Fatal(e)
	}
	reopen()
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	again, _ := readJob(uc, s.db.SQL, j.ID)
	var n int
	s.db.SQL.QueryRow("SELECT count(*) FROM tasks WHERE job_id=? AND stage='reduce'", j.ID).Scan(&n)
	if n != 1 || again.manifestHash != first.manifestHash {
		t.Fatal("barrier replay changed reduce/manifest")
	}
	if e = s.assign(uc, reduce, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	ref := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=?", reduce).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); e != nil {
		t.Fatal(e)
	}
	req := &pb.InputArtifactRequest{Attempt: ref, ArtifactId: selected[0]}
	if _, e = s.inputArtifact(uc, "w", req); e != nil {
		t.Fatal(e)
	}
	if _, e = s.inputArtifact(uc, "other", req); e == nil {
		t.Fatal("wrong worker downloaded inputs")
	}
	req.ArtifactId = store.ID()
	if _, e = s.inputArtifact(uc, "w", req); e == nil {
		t.Fatal("artifact outside manifest downloaded")
	}
	req.ArtifactId = selected[0]
	if _, e = s.CancelJob(uc, j.ID, CancelJobRequest{ControlID: "stop"}); e != nil {
		t.Fatal(e)
	}
	reopen()
	if _, e = s.inputArtifact(uc, "w", req); e == nil {
		t.Fatal("canceled attempt downloaded inputs")
	}
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	stopping, _ := s.GetJob(uc, j.ID)
	if terminal(stopping.State) {
		t.Fatal("cancel published before cleanup")
	}
	if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, Success: true}); e != nil {
		t.Fatal(e)
	}
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	last, _ := s.GetJob(uc, j.ID)
	if last.State != "CANCELED" {
		t.Fatalf("late success won: %+v", last)
	}
}
func TestJobFairnessTemplateGateDeadlineAndStorageFailure(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	jobs := []string{}
	for _, key := range []string{"one", "two"} {
		j, e := s.SubmitJob(uc, key, job.JSON(spec))
		if e != nil {
			t.Fatal(e)
		}
		jobs = append(jobs, j.ID)
	}
	wrong := &session{hello: &pb.WorkerHello{WorkerId: "w", Epoch: "e", Slots: 1, Runtimes: []*pb.Runtime{{Profile: "codex_exec", Models: []string{"model-c"}, Credentials: []string{"account"}, Repositories: []string{"repo"}, Policies: []string{"review"}, Verifiers: []string{"check"}, Capabilities: []string{"event_stream", "cancel", "job_io_v1"}, TemplateDigests: map[string]string{}}}}, identity: peer.identity}
	if e := s.scheduleQueued(uc, []*session{wrong}); e != nil {
		t.Fatal(e)
	}
	var n int
	s.db.SQL.QueryRow("SELECT count(*) FROM attempts").Scan(&n)
	if n != 0 {
		t.Fatal("template mismatch dispatched")
	}
	var previous string
	for i := 0; i < 2; i++ {
		if e := s.scheduleQueued(uc, []*session{peer}); e != nil {
			t.Fatal(e)
		}
		var task, jid string
		if e := s.db.SQL.QueryRow("SELECT t.id,t.job_id FROM tasks t JOIN attempts a ON a.task=t.id WHERE a.released=0").Scan(&task, &jid); e != nil {
			t.Fatal(e)
		}
		if jid == previous {
			t.Fatal("same job monopolized the released account slot")
		}
		previous = jid
		completeFake(t, s, wc, task)
	}
	if _, e := s.db.SQL.Exec("UPDATE jobs SET deadline=? WHERE id=?", store.Now()-1, jobs[0]); e != nil {
		t.Fatal(e)
	}
	if e := s.advanceJob(uc, jobs[0]); e != nil {
		t.Fatal(e)
	}
	expired, _ := s.GetJob(uc, jobs[0])
	if expired.State != "FAILED" || expired.StopReason != "DEADLINE_EXCEEDED" {
		t.Fatalf("deadline: %+v", expired)
	}
	// SQLite page exhaustion must roll back the Job and all initial Tasks.
	var pages int
	if e := s.db.SQL.QueryRow("PRAGMA page_count").Scan(&pages); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.SQL.Exec("PRAGMA max_page_count=" + strconv.Itoa(pages)); e != nil {
		t.Fatal(e)
	}
	single := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	single.Input.Text = strings.Repeat("x", 64<<10)
	_, e := s.SubmitJob(uc, "disk-full", job.JSON(single))
	if status.Code(e) != codes.Unavailable {
		t.Fatalf("storage failure not surfaced: %v", e)
	}
	s.db.SQL.QueryRow("SELECT count(*) FROM jobs WHERE idem='disk-full'").Scan(&n)
	if n != 0 {
		t.Fatal("disk full left partial job")
	}
	// An external writer tests the busy rollback path after restoring page capacity.
	s.db.SQL.Exec("PRAGMA max_page_count=2147483646")
	raw, e := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	if _, e = raw.Exec("BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	single.Input.Text = "busy"
	_, e = s.SubmitJob(uc, "busy", job.JSON(single))
	raw.Exec("ROLLBACK")
	if status.Code(e) != codes.Unavailable {
		t.Fatalf("busy: %v", e)
	}
	s.db.SQL.QueryRow("SELECT count(*) FROM jobs WHERE idem='busy'").Scan(&n)
	if n != 0 {
		t.Fatal("busy left partial job")
	}
}

func TestGatewayUnsettledRequestsRecoverAsUnknown(t *testing.T) {
	s, _, _, _ := offlineJobServer(t)
	cfg := s.cfg
	if _, e := s.db.SQL.Exec("INSERT INTO gateway_requests(id,owner,project,route,model,endpoint,state,started) VALUES('pending','owner','project','route','model','/v1/responses','STARTED',1)"); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var state string
	var in, out sql.NullInt64
	if e = s.db.SQL.QueryRow("SELECT state,input_tokens,output_tokens FROM gateway_requests WHERE id='pending'").Scan(&state, &in, &out); e != nil || state != "UNKNOWN" || in.Valid || out.Valid {
		t.Fatalf("unknown usage replaced by zero: %s %v %v %v", state, in, out, e)
	}
}
