package server

import (
	"context"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
)

func configureClaudeHTTPBudgetFixture(s *Server, peer *session) job.Spec {
	digest := strings.Repeat("b", 64)
	template := config.JobTemplate{RuntimeProfile: "claude_http", PolicyRef: "review", AcceptanceProfile: "check", Digest: digest}
	s.cfg.Jobs.Templates = append(s.cfg.Jobs.Templates, template)
	digests := map[string]string{template.Key(): digest}
	peer.hello.Runtimes = append(peer.hello.Runtimes, &pb.Runtime{
		Profile: "claude_http", Models: []string{"model-a"}, Credentials: []string{"account"},
		Repositories: []string{"repo"}, Policies: []string{"review"}, Verifiers: []string{"check"},
		Capabilities:    []string{"event_stream", "cancel", "job_io_v1", "environment:process", claudeEstimatedUSDBudgetCapability},
		TemplateDigests: digests,
	})
	execution := job.Execution{RuntimeProfile: "claude_http", Model: "model-a", CredentialRef: "account", PolicyRef: "review", AcceptanceProfile: "check"}
	return job.Spec{SchemaVersion: "v0.2", ProjectID: "project", Mode: "single", Workspace: job.Workspace{RepositoryRef: "repo", BaseCommit: strings.Repeat("a", 40)}, Input: job.Input{Text: "budgeted"}, Execution: &execution, Limits: job.Limits{TimeoutSeconds: 50, MaxAttemptsPerTask: 1}}
}

func budgetedJob(t *testing.T, maxTokens, maxCost any) (*Server, context.Context, context.Context, *session, string, string) {
	t.Helper()
	s, ctx, workerCtx, peer := offlineJobServer(t)
	spec := configureClaudeHTTPBudgetFixture(s, peer)
	j, err := s.SubmitJob(ctx, "budgeted-job", job.JSON(spec))
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	goalID := "goal_" + j.ID
	if _, err = s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_tokens,max_cost_units) VALUES(?,?,?)", goalID, maxTokens, maxCost); err != nil {
		s.Close()
		t.Fatal(err)
	}
	var taskID string
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&taskID); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, ctx, workerCtx, peer, j.ID, taskID
}

func TestRuntimeBudgetFrozenOnAssignment(t *testing.T) {
	s, ctx, _, peer, _, taskID := budgetedJob(t, nil, int64(2_500_000))
	defer s.Close()
	if err := s.assign(ctx, taskID, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := s.db.SQL.QueryRow("SELECT body FROM commands WHERE task=? AND kind='start'", taskID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	command := new(pb.Command)
	if err := decode(body, command); err != nil {
		t.Fatal(err)
	}
	budget := command.GetAssignment().GetRuntimeBudget()
	if budget == nil || budget.GetRemainingCostUnits() != 2_500_000 || budget.GetCostSemantics() != "usd_micros_client_estimate" || budget.RemainingTokenUnits != nil {
		t.Fatalf("unexpected frozen Runtime budget: %+v", budget)
	}
}

func TestRuntimeBudgetRequiresWorkerCapability(t *testing.T) {
	s, _, _, peer, _, taskID := budgetedJob(t, nil, int64(100))
	defer s.Close()
	peer.hello.Runtimes[len(peer.hello.Runtimes)-1].Capabilities = []string{"event_stream", "cancel", "job_io_v1", "environment:process"}
	if err := s.assign(context.Background(), taskID, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var blocker string
	if err := s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", taskID).Scan(&blocker); err != nil || blocker != "GOAL_RUNTIME_BUDGET_UNSUPPORTED" {
		t.Fatalf("blocker=%q err=%v", blocker, err)
	}
}

func TestRuntimeBudgetRejectsTokenAndCodexPolicies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxTokens any
		maxCost   any
		useCodex  bool
	}{
		{name: "Claude token", maxTokens: int64(10), maxCost: nil},
		{name: "mixed", maxTokens: int64(10), maxCost: int64(20)},
		{name: "Codex cost", maxTokens: nil, maxCost: int64(20), useCodex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, peer, _, taskID := budgetedJob(t, tc.maxTokens, tc.maxCost)
			defer s.Close()
			if tc.useCodex {
				if _, err := s.db.SQL.Exec("UPDATE tasks SET spec=json_set(spec,'$.runtime_profile','codex_exec') WHERE id=?", taskID); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.assign(context.Background(), taskID, []*session{peer}); err != nil {
				t.Fatal(err)
			}
			var blocker string
			if err := s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", taskID).Scan(&blocker); err != nil || blocker != "GOAL_RUNTIME_BUDGET_UNSUPPORTED" {
				t.Fatalf("blocker=%q err=%v", blocker, err)
			}
		})
	}
}

func TestRuntimeBudgetRejectsMixedProviderActiveJob(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	configureClaudeHTTPBudgetFixture(s, peer)
	spec.Map.Partitions[1].Execution.Engine = ""
	spec.Map.Partitions[1].Execution.RuntimeProfile = "claude_http"
	j, err := s.SubmitJob(ctx, "mixed-budget-job", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,100)", "goal_"+j.ID); err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(ctx, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var claudeTask string
	for _, child := range children {
		var profile string
		if err = s.db.SQL.QueryRow("SELECT json_extract(spec,'$.runtime_profile') FROM tasks WHERE id=?", child.id).Scan(&profile); err != nil {
			t.Fatal(err)
		}
		if profile == "claude_http" {
			claudeTask = child.id
		}
	}
	if claudeTask == "" {
		t.Fatal("mixed Job has no claude_http task")
	}
	if err = s.assign(ctx, claudeTask, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var blocker string
	var attempts int
	if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", claudeTask).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?", j.ID).Scan(&attempts); err != nil || blocker != "GOAL_RUNTIME_BUDGET_UNSUPPORTED" || attempts != 0 {
		t.Fatalf("blocker=%q attempts=%d err=%v", blocker, attempts, err)
	}
}

func TestRuntimeBudgetFailsClosedForUnknownAndExhaustedUsage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   bool
		blocker string
	}{
		{name: "unknown", blocker: "GOAL_USAGE_UNKNOWN"},
		{name: "exhausted", usage: true, blocker: "GOAL_USAGE_EXHAUSTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, peer, jobID, taskID := budgetedJob(t, nil, int64(100))
			defer s.Close()
			attemptID := "settled-" + tc.name
			if _, err := s.db.SQL.Exec(`INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released)
				VALUES(?,?,'old-worker','old-epoch',1,?,1,1)`, attemptID, taskID, tc.name); err != nil {
				t.Fatal(err)
			}
			goalID := "goal_" + jobID
			if _, err := s.db.SQL.Exec("INSERT INTO goal_attempt_reservations VALUES(?,?,1,1,1)", attemptID, goalID); err != nil {
				t.Fatal(err)
			}
			if tc.usage {
				if _, err := s.db.SQL.Exec(`INSERT INTO goal_usage VALUES(?,?,?,'fixture',0,100,1,?,1)`, goalID, "usage-"+tc.name, attemptID, tc.name); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.assign(context.Background(), taskID, []*session{peer}); err != nil {
				t.Fatal(err)
			}
			var blocker string
			if err := s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", taskID).Scan(&blocker); err != nil || blocker != tc.blocker {
				t.Fatalf("blocker=%q want=%q err=%v", blocker, tc.blocker, err)
			}
		})
	}
}

func assignedAttempt(t *testing.T, s *Server, ctx context.Context, peer *session, taskID string) *pb.AttemptRef {
	t.Helper()
	if err := s.assign(ctx, taskID, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	ref := new(pb.AttemptRef)
	if err := s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? ORDER BY generation DESC LIMIT 1", taskID).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); err != nil {
		t.Fatal(err)
	}
	return ref
}

func reportBudgetMetrics(t *testing.T, s *Server, workerCtx context.Context, taskID string, ref *pb.AttemptRef, metrics telemetry.Attempt) {
	t.Helper()
	_, err := s.ReportEvents(workerCtx, &pb.ReportRequest{Attempt: ref, Events: []*pb.Event{{
		EventId: "metrics-" + ref.AttemptId, WorkerSeq: 1, TaskId: taskID, AttemptId: ref.AttemptId,
		Generation: ref.Generation, Type: "attempt.metrics", PayloadJson: config.JSON(metrics),
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBudgetedCompletionSettlesUsageBeforeRelease(t *testing.T) {
	s, userCtx, workerCtx, peer, jobID, taskID := budgetedJob(t, nil, int64(100))
	defer s.Close()
	ref := assignedAttempt(t, s, userCtx, peer, taskID)
	cost := int64(40)
	reportBudgetMetrics(t, s, workerCtx, taskID, ref, telemetry.Attempt{
		Source: "claude_http", NativeFinal: true, UsageComplete: true,
		Usage: &telemetry.Tokens{Input: 3, Output: 2}, CostUnits: &cost, CostComplete: true,
	})
	if _, err := s.CompleteAttempt(workerCtx, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "RUNTIME_FAILED", FinalWorkerSeq: 1}); err != nil {
		t.Fatal(err)
	}
	var tokens, storedCost int64
	var complete, released int
	if err := s.db.SQL.QueryRow(`SELECT u.tokens,u.cost_units,u.complete,a.released FROM goal_usage u JOIN attempts a ON a.id=u.attempt_id
		WHERE u.goal_id=? AND u.attempt_id=?`, "goal_"+jobID, ref.AttemptId).Scan(&tokens, &storedCost, &complete, &released); err != nil {
		t.Fatal(err)
	}
	if tokens != 5 || storedCost != 40 || complete != 1 || released != 1 {
		t.Fatalf("tokens=%d cost=%d complete=%d released=%d", tokens, storedCost, complete, released)
	}
}

func TestBudgetedCompletionWithUnknownUsageBlocksNextAttempt(t *testing.T) {
	s, userCtx, workerCtx, peer, jobID, taskID := budgetedJob(t, nil, int64(100))
	defer s.Close()
	ref := assignedAttempt(t, s, userCtx, peer, taskID)
	reportBudgetMetrics(t, s, workerCtx, taskID, ref, telemetry.Attempt{Source: "claude_http", Usage: &telemetry.Tokens{Input: 1, Output: 1}, UsageComplete: true})
	if _, err := s.CompleteAttempt(workerCtx, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "MISSING_FINAL", FinalWorkerSeq: 1}); err != nil {
		t.Fatal(err)
	}
	var complete int
	if err := s.db.SQL.QueryRow("SELECT complete FROM goal_usage WHERE goal_id=? AND attempt_id=?", "goal_"+jobID, ref.AttemptId).Scan(&complete); err != nil || complete != 0 {
		t.Fatalf("complete=%d err=%v", complete, err)
	}
	err := s.db.Tx(context.Background(), func(q store.Query) error {
		j, readErr := readJob(context.Background(), q, jobID)
		if readErr != nil {
			return readErr
		}
		_, _, readErr = buildGoalRuntimeBudget(context.Background(), q, j, &pb.Task{Spec: &pb.TaskSpec{RuntimeProfile: "claude_http"}})
		return readErr
	})
	if err == nil || err.Error() != "GOAL_USAGE_UNKNOWN" {
		t.Fatalf("next attempt admission err=%v", err)
	}
}

func TestBudgetedWorkerRestartRecordsIncompleteUsage(t *testing.T) {
	s, userCtx, workerCtx, peer, jobID, taskID := budgetedJob(t, nil, int64(100))
	defer s.Close()
	ref := assignedAttempt(t, s, userCtx, peer, taskID)
	if _, err := s.db.SQL.Exec("UPDATE workers SET epoch='restarted' WHERE id=?", peer.hello.WorkerId); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteAttempt(workerCtx, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "WORKER_RESTARTED"}); err != nil {
		t.Fatal(err)
	}
	var complete int
	if err := s.db.SQL.QueryRow("SELECT complete FROM goal_usage WHERE goal_id=? AND attempt_id=?", "goal_"+jobID, ref.AttemptId).Scan(&complete); err != nil || complete != 0 {
		t.Fatalf("complete=%d err=%v", complete, err)
	}
}

func TestBudgetExhaustedCompletionDoesNotRetryAndRetainsOvershoot(t *testing.T) {
	s, userCtx, workerCtx, peer, jobID, taskID := budgetedJob(t, nil, int64(100))
	defer s.Close()
	j, err := readJob(userCtx, s.db.SQL, jobID)
	if err != nil {
		t.Fatal(err)
	}
	j.frozen.Spec.Limits.MaxAttemptsPerTask = 2
	j.frozen.Spec.Execution.ReplaySafe = true
	if _, err = s.db.SQL.Exec("UPDATE jobs SET spec=? WHERE id=?", job.JSON(j.frozen), jobID); err != nil {
		t.Fatal(err)
	}
	ref := assignedAttempt(t, s, userCtx, peer, taskID)
	cost := int64(125)
	reportBudgetMetrics(t, s, workerCtx, taskID, ref, telemetry.Attempt{
		Source: "claude_http", NativeFinal: true, UsageComplete: true,
		Usage: &telemetry.Tokens{Input: 2, Output: 1}, CostUnits: &cost, CostComplete: true, BudgetReached: true,
	})
	if _, err = s.CompleteAttempt(workerCtx, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "RUNTIME_BUDGET_EXHAUSTED", FinalWorkerSeq: 1}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var state, code string
	var storedCost int64
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM attempts WHERE task=?", taskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state,error_code FROM tasks WHERE id=?", taskID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT cost_units FROM goal_usage WHERE attempt_id=?", ref.AttemptId).Scan(&storedCost); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || state != "FAILED" || code != "RUNTIME_BUDGET_EXHAUSTED" || storedCost != 125 {
		t.Fatalf("attempts=%d state=%s code=%s cost=%d", attempts, state, code, storedCost)
	}
}

func TestFiniteBudgetGoalAllowsOnlyOneInflightAttempt(t *testing.T) {
	s, ctx, _, peer := offlineJobServer(t)
	defer s.Close()
	spec := configureClaudeHTTPBudgetFixture(s, peer)
	first := *spec.Execution
	second := first
	spec.Mode, spec.Execution = "map_reduce", nil
	spec.Map = &job.Map{Parallelism: 2, Partitions: []job.Partition{
		{Key: "a", ScopePaths: []string{"a"}, Input: job.Input{Text: "a"}, Execution: first},
		{Key: "b", ScopePaths: []string{"b"}, Input: job.Input{Text: "b"}, Execution: second},
	}}
	spec.Reduce = &job.Reduce{Strategy: "report_merge_v1", Input: job.Input{Text: "merge"}, Execution: first}
	j, err := s.SubmitJob(ctx, "budget-race", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,?)", "goal_"+j.ID, 1000); err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(ctx, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.assign(ctx, children[0].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	if err = s.assign(ctx, children[1].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM attempts a JOIN tasks t ON t.id=a.task WHERE t.job_id=?", j.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	var blocker string
	if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", children[1].id).Scan(&blocker); err != nil || attempts != 1 || blocker != "GOAL_BUDGET_IN_FLIGHT" {
		t.Fatalf("attempts=%d blocker=%q err=%v", attempts, blocker, err)
	}
}
