package server

import (
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func failedSingleForManualRetry(t *testing.T, maxAttempts int, replaySafe bool) (*Server, string, string, *pb.AttemptRef, *session) {
	t.Helper()
	s, uc, wc, peer := offlineJobServer(t)
	spec := retrySpec(strings.Repeat("a", 40), maxAttempts, replaySafe)
	j, err := s.SubmitJob(uc, "manual-retry-fixture", job.JSON(spec))
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	children, err := jobChildren(uc, s.db.SQL, j.ID)
	if err != nil || len(children) != 1 {
		s.Close()
		t.Fatalf("children=%v err=%v", children, err)
	}
	taskID := children[0].id
	if err = s.assign(uc, taskID, []*session{peer}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	ref := new(pb.AttemptRef)
	if err = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).
		Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); err != nil {
		s.Close()
		t.Fatal(err)
	}
	// VERIFICATION_FAILED is intentionally outside automatic retry taxonomy so
	// the Job reaches a durable FAILED state for explicit user retry.
	if _, err = s.CompleteAttempt(wc, &pb.CompleteRequest{
		Attempt: ref, CleanupConfirmed: true, ErrorCode: "VERIFICATION_FAILED", ErrorMessage: "fixture verification failure",
	}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err = s.advanceJob(uc, j.ID); err != nil {
		s.Close()
		t.Fatal(err)
	}
	var jobState, taskState, stageState string
	if err = s.db.SQL.QueryRow("SELECT state FROM jobs WHERE id=?", j.ID).Scan(&jobState); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state FROM tasks WHERE id=?", taskID).Scan(&taskState); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state FROM stages WHERE job_id=? AND kind='single'", j.ID).Scan(&stageState); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if jobState != "FAILED" || taskState != "FAILED" || stageState != "FAILED" {
		s.Close()
		t.Fatalf("fixture not terminal: job=%s task=%s stage=%s", jobState, taskState, stageState)
	}
	return s, j.ID, taskID, ref, peer
}

func TestManualRetryReopensFailedSingleJobAndRunsNextGeneration(t *testing.T) {
	s, jobID, taskID, first, peer := failedSingleForManualRetry(t, 2, true)
	defer s.Close()
	uc, wc := func() (context.Context, context.Context) {
		// offlineJobServer contexts are tied to tokens rather than Server state;
		// reconstruct them from the configured token files after fixture setup.
		ut, _ := config.Token(s.cfg.Users[0].TokenFile)
		wt, _ := config.Token(s.cfg.Workers[0].TokenFile)
		u, _ := s.auth.Bearer(context.Background(), "Bearer "+ut)
		w, _ := s.auth.Bearer(context.Background(), "Bearer "+wt)
		return u, w
	}()

	before, err := readJob(uc, s.db.SQL, jobID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.ManualRetry(uc, jobID, ManualRetryRequest{
		OperationID: "manual-retry-1", TaskID: taskID,
		ExpectedAttemptID: first.AttemptId, ExpectedGeneration: first.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Existing || receipt.NextGeneration != 2 {
		t.Fatalf("receipt=%+v", receipt)
	}

	var jobState, taskState, attempt, stageState string
	var generation, deadline int64
	if err = s.db.SQL.QueryRow("SELECT state,deadline FROM jobs WHERE id=?", jobID).Scan(&jobState, &deadline); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state,attempt,current_generation FROM tasks WHERE id=?", taskID).
		Scan(&taskState, &attempt, &generation); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state FROM stages WHERE job_id=? AND kind='single'", jobID).Scan(&stageState); err != nil {
		t.Fatal(err)
	}
	if jobState != "QUEUED" || taskState != "QUEUED" || attempt != "" || generation != 1 || stageState != "READY" {
		t.Fatalf("reopen mismatch job=%s task=%s attempt=%s gen=%d stage=%s", jobState, taskState, attempt, generation, stageState)
	}
	if deadline != before.Deadline {
		t.Fatalf("manual retry reset deadline: got=%d want=%d", deadline, before.Deadline)
	}

	// Lost response replay is receipt-only; no second state mutation.
	replay, err := s.ManualRetry(uc, jobID, ManualRetryRequest{
		OperationID: "manual-retry-1", TaskID: taskID,
		ExpectedAttemptID: first.AttemptId, ExpectedGeneration: first.Generation,
	})
	if err != nil || !replay.Existing {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}

	if err = s.assign(uc, taskID, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	second := new(pb.AttemptRef)
	if err = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).
		Scan(&second.AttemptId, &second.Generation, &second.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if second.Generation != 2 || second.AttemptId == first.AttemptId {
		t.Fatalf("second=%+v first=%+v", second, first)
	}
	artifact := registerResult(t, s, taskID, second.AttemptId)
	if _, err = s.CompleteAttempt(wc, &pb.CompleteRequest{
		Attempt: second, Success: true, CleanupConfirmed: true, ArtifactIds: []string{artifact},
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.advanceJob(uc, jobID); err != nil {
		t.Fatal(err)
	}
	done, err := s.GetJob(uc, jobID)
	if err != nil || done.State != "SUCCEEDED" {
		t.Fatalf("done=%+v err=%v", done, err)
	}
}

func TestManualRetryFencingConflictAndSafety(t *testing.T) {
	s, jobID, taskID, first, _ := failedSingleForManualRetry(t, 2, true)
	defer s.Close()
	ut, _ := config.Token(s.cfg.Users[0].TokenFile)
	uc, _ := s.auth.Bearer(context.Background(), "Bearer "+ut)

	base := ManualRetryRequest{OperationID: "retry-safety", TaskID: taskID, ExpectedAttemptID: first.AttemptId, ExpectedGeneration: first.Generation}
	stale := base
	stale.ExpectedGeneration++
	if _, err := s.ManualRetry(uc, jobID, stale); status.Code(err) != codes.Aborted || status.Convert(err).Message() != control.ErrorAttemptFenced.String() {
		t.Fatalf("stale retry not fenced: %v", err)
	}
	if _, err := s.ManualRetry(uc, jobID, base); err != nil {
		t.Fatal(err)
	}
	conflict := base
	conflict.TaskID = "different-task"
	if _, err := s.ManualRetry(uc, jobID, conflict); status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != control.ErrorOperationConflict.String() {
		t.Fatalf("operation conflict not detected: %v", err)
	}
}

func TestManualRetryRejectsReplayUnsafeAndExhausted(t *testing.T) {
	unsafe, unsafeJob, unsafeTask, unsafeRef, _ := failedSingleForManualRetry(t, 1, false)
	defer unsafe.Close()
	ut, _ := config.Token(unsafe.cfg.Users[0].TokenFile)
	uc, _ := unsafe.auth.Bearer(context.Background(), "Bearer "+ut)
	if _, err := unsafe.ManualRetry(uc, unsafeJob, ManualRetryRequest{
		OperationID: "unsafe-retry", TaskID: unsafeTask,
		ExpectedAttemptID: unsafeRef.AttemptId, ExpectedGeneration: unsafeRef.Generation,
	}); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "MANUAL_RETRY_REPLAY_UNSAFE" {
		t.Fatalf("unsafe retry accepted: %v", err)
	}

	exhausted, exhaustedJob, exhaustedTask, exhaustedRef, _ := failedSingleForManualRetry(t, 1, true)
	defer exhausted.Close()
	ut2, _ := config.Token(exhausted.cfg.Users[0].TokenFile)
	uc2, _ := exhausted.auth.Bearer(context.Background(), "Bearer "+ut2)
	if _, err := exhausted.ManualRetry(uc2, exhaustedJob, ManualRetryRequest{
		OperationID: "exhausted-retry", TaskID: exhaustedTask,
		ExpectedAttemptID: exhaustedRef.AttemptId, ExpectedGeneration: exhaustedRef.Generation,
	}); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "MANUAL_RETRY_ATTEMPTS_EXHAUSTED" {
		t.Fatalf("exhausted retry accepted: %v", err)
	}
}
