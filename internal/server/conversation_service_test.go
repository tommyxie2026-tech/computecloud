package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/conversation"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConversationRequestDurableIdempotency(t *testing.T) {
	db, err := store.Open(t.TempDir(), store.ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := config.ConversationProfile{ID: "safe", PublicModel: "agent-model", ProjectID: "p", MaxInputBytes: 4096, MaxOutputTokens: 100, MaxActive: 2}
	s := &Server{db: db, cfg: config.Server{Jobs: config.Jobs{Enabled: true}, ConversationJobs: config.ConversationJobs{Enabled: true, Profiles: []config.ConversationProfile{p}}}}
	ctx := context.Background()
	ctx = rpcutil.WithPrincipal(ctx, rpcutil.Principal{Identity: config.Identity{Owner: "caller", Projects: []string{"p"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit"}}})
	r := conversation.Request{Protocol: "messages", Model: "agent-model", MaxOutputTokens: 20, Transcript: []conversation.Message{{Role: "user", Text: "hello"}}}
	first, err := s.beginConversationRequest(ctx, r, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.beginConversationRequest(ctx, r, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ID == "" {
		t.Fatalf("idempotent replay created a new request: %q %q", first.ID, second.ID)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, e := s.beginConversationRequest(ctx, r, "key-1")
			if e != nil {
				errs <- e
				return
			}
			ids <- rec.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	for id := range ids {
		if id != first.ID {
			t.Fatalf("concurrent idempotency created %q, want %q", id, first.ID)
		}
	}
	auto1, err := s.beginConversationRequest(ctx, r, "")
	if err != nil {
		t.Fatal(err)
	}
	auto2, err := s.beginConversationRequest(ctx, r, "")
	if err != nil || auto1.ID != auto2.ID {
		t.Fatalf("automatic digest replay mismatch: %q %q err=%v", auto1.ID, auto2.ID, err)
	}
	if _, err = s.beginConversationRequest(ctx, r, "key-2"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected profile concurrency limit, got %v", err)
	}
	s.cfg.ConversationJobs.Profiles[0].MaxOutputTokens++
	if _, err = s.beginConversationRequest(ctx, r, "key-1"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected profile revision conflict, got %v", err)
	}
	s.cfg.ConversationJobs.Profiles[0].MaxOutputTokens--
	r.Transcript[0].Text = "different"
	if _, err = s.beginConversationRequest(ctx, r, "key-1"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestConversationRetryRecoversSubmitBeforeMappingAfterRestart(t *testing.T) {
	execToken, callerToken := testutil.Token(t, "exec"), testutil.Token(t, "caller")
	dataDir := t.TempDir()
	w := config.Worker{Runtimes: map[string]config.Runtime{"codex_exec": {Version: "fixture-1", Models: []string{"model-c"}, Credentials: []string{"account"}}}, Policies: map[string]config.Policy{"write": {}}, Verifiers: map[string][][]string{"check": {{"true"}}}}
	template := config.Templates(w)[0]
	profile := config.ConversationProfile{ID: "safe", PublicModel: "remote", ExecutionOwner: "runner", ProjectID: "project", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)}, Execution: job.Execution{Engine: "codex", RuntimeProfile: "codex_exec", Model: "model-c", CredentialRef: "account", PolicyRef: template.PolicyRef, AcceptanceProfile: template.AcceptanceProfile}, Limits: job.Limits{TimeoutSeconds: 60, MaxAttemptsPerTask: 1}, MaxInputBytes: 64 << 10, MaxOutputTokens: 1024, MaxOutputBytes: 1 << 20, MaxActive: 2}
	cfg := config.Server{DataDir: dataDir, LeaseSeconds: 30, TickMS: 50, MaxArtifactBytes: 1 << 20, MaxProjectTasks: 100, Credentials: map[string]int{"account": 1}, Jobs: config.Jobs{Enabled: true, Templates: []config.JobTemplate{template}}, ConversationJobs: config.ConversationJobs{Enabled: true, Profiles: []config.ConversationProfile{profile}}, Users: []config.Identity{{TokenFile: execToken, Owner: "runner", Projects: []string{"project"}, Credentials: []string{"account"}, Scopes: []string{"jobs:submit", "jobs:read", "jobs:cancel"}}, {TokenFile: callerToken, Owner: "client", Projects: []string{"project"}, ConversationProfile: "safe", Scopes: []string{"conversations:submit", "conversations:read"}}}}
	s1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rawCaller, _ := config.Token(callerToken)
	ctx, err := s1.auth.Bearer(context.Background(), "Bearer "+rawCaller)
	if err != nil {
		t.Fatal(err)
	}
	r := conversation.Request{Protocol: "messages", Model: "remote", MaxOutputTokens: 100, Transcript: []conversation.Message{{Role: "user", Text: "recover me"}}}
	rec, err := s1.beginConversationRequest(ctx, r, "restart-key")
	if err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	prompt.WriteString("[user]\nrecover me\n\n")
	spec := job.Spec{SchemaVersion: "v0.2", ProjectID: profile.ProjectID, Mode: "single", Workspace: profile.Workspace, Input: job.Input{Text: prompt.String()}, Execution: &profile.Execution, Limits: profile.Limits}
	delegated, err := rpcutil.ConversationJobContext(ctx, cfg.Users[0], profile.ID, "submit")
	if err != nil {
		t.Fatal(err)
	}
	created, err := s1.SubmitJob(delegated, "conversation_"+rec.ID, job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	if err = s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ctx2, err := s2.auth.Bearer(context.Background(), "Bearer "+rawCaller)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s2.beginConversationRequest(ctx2, r, "restart-key")
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != rec.ID || retry.JobID != "" {
		t.Fatalf("unexpected recovered request: %#v", retry)
	}
	retry, _, err = s2.ensureConversationJob(ctx2, retry, retry.Request)
	if err != nil {
		t.Fatal(err)
	}
	if retry.JobID != created.ID {
		t.Fatalf("retry created duplicate Job: %s != %s", retry.JobID, created.ID)
	}
	var count int
	if err = s2.db.SQL.QueryRow("SELECT count(*) FROM jobs WHERE owner='runner'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("job count=%d err=%v", count, err)
	}
	var mappingState string
	if err = s2.db.SQL.QueryRow("SELECT state FROM conversation_requests WHERE request_id=?", rec.ID).Scan(&mappingState); err != nil || mappingState != "SUBMITTED" {
		t.Fatalf("mapping state=%q err=%v", mappingState, err)
	}
}

func TestConversationSubmissionCannotDelegateWrongProfile(t *testing.T) {
	ctx := rpcutil.WithPrincipal(context.Background(), rpcutil.Principal{Identity: config.Identity{Owner: "caller", ConversationProfile: "other", Scopes: []string{"conversations:submit"}}})
	if _, err := rpcutil.ConversationJobContext(ctx, config.Identity{Owner: "executor", Scopes: []string{"jobs:submit"}}, "safe", "submit"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected delegation denial, got %v", err)
	}
}
