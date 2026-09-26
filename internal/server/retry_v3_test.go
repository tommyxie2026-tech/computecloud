package server

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func retrySpec(commit string, attempts int, replaySafe bool) job.Spec {
	s := (&jobHarness{commit: commit}).spec("single")
	s.Limits.MaxAttemptsPerTask = attempts
	s.Execution.ReplaySafe = replaySafe
	return s
}

func TestRetrySafetySchedulesSecondGeneration(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()

	spec := retrySpec("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, true)
	j, e := s.SubmitJob(uc, "retry-safe", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	children, e := jobChildren(uc, s.db.SQL, j.ID)
	if e != nil || len(children) != 1 {
		t.Fatalf("children=%v err=%v", children, e)
	}
	taskID := children[0].id
	var originalDeadline int64
	if e = s.db.SQL.QueryRow("SELECT deadline FROM tasks WHERE id=?", taskID).Scan(&originalDeadline); e != nil {
		t.Fatal(e)
	}
	if e = s.assign(uc, taskID, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	first := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).Scan(&first.AttemptId, &first.Generation, &first.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{
		Attempt: first, CleanupConfirmed: true, ErrorCode: "RUNTIME_FAILED", ErrorMessage: "transient fixture",
	}); e != nil {
		t.Fatal(e)
	}

	var state, attempt, blocker string
	var retryAfter, generation, deadline int64
	if e = s.db.SQL.QueryRow("SELECT state,attempt,blocker,retry_after,current_generation,deadline FROM tasks WHERE id=?", taskID).
		Scan(&state, &attempt, &blocker, &retryAfter, &generation, &deadline); e != nil {
		t.Fatal(e)
	}
	if state != "QUEUED" || attempt != "" || blocker != "RETRY_BACKOFF" || retryAfter <= store.Now() || generation != 1 {
		t.Fatalf("retry not scheduled safely: state=%s attempt=%s blocker=%s retry_after=%d generation=%d", state, attempt, blocker, retryAfter, generation)
	}
	if deadline != originalDeadline {
		t.Fatalf("retry reset deadline: got=%d want=%d", deadline, originalDeadline)
	}
	if e = s.assign(uc, taskID, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	var attempts int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM attempts WHERE task=?", taskID).Scan(&attempts); e != nil || attempts != 1 {
		t.Fatalf("backoff dispatch created attempt early: attempts=%d err=%v", attempts, e)
	}

	if _, e = s.db.SQL.Exec("UPDATE tasks SET retry_after=0 WHERE id=?", taskID); e != nil {
		t.Fatal(e)
	}
	if e = s.assign(uc, taskID, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	second := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).Scan(&second.AttemptId, &second.Generation, &second.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if second.Generation != 2 || second.AttemptId == first.AttemptId {
		t.Fatalf("second attempt=%+v first=%+v", second, first)
	}
	artifact := registerResult(t, s, taskID, second.AttemptId)
	if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{
		Attempt: second, Success: true, CleanupConfirmed: true, ArtifactIds: []string{artifact},
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	done, e := s.GetJob(uc, j.ID)
	if e != nil || done.State != "SUCCEEDED" {
		t.Fatalf("retry job=%+v err=%v", done, e)
	}
	var retryEvents int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM events WHERE task=? AND json_extract(body,'$.type')='task.retry_scheduled'", taskID).Scan(&retryEvents); e != nil || retryEvents != 1 {
		t.Fatalf("retry events=%d err=%v", retryEvents, e)
	}
}

func TestRetrySafetyExhaustionAndTaxonomy(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()

	spec := retrySpec("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, true)
	j, e := s.SubmitJob(uc, "retry-exhaust", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	children, _ := jobChildren(uc, s.db.SQL, j.ID)
	taskID := children[0].id

	for generation := int64(1); generation <= 2; generation++ {
		if generation == 2 {
			if _, e = s.db.SQL.Exec("UPDATE tasks SET retry_after=0 WHERE id=?", taskID); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.assign(uc, taskID, []*session{peer}); e != nil {
			t.Fatal(e)
		}
		ref := new(pb.AttemptRef)
		if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); e != nil {
			t.Fatal(e)
		}
		if ref.Generation != generation {
			t.Fatalf("generation=%d want=%d", ref.Generation, generation)
		}
		if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "RUNTIME_FAILED"}); e != nil {
			t.Fatal(e)
		}
	}
	task, _, e := readTask(uc, s.db.SQL, taskID)
	if e != nil || task.State != "FAILED" || task.ErrorCode != "RUNTIME_FAILED" {
		t.Fatalf("exhausted task=%+v err=%v", task, e)
	}
	var exhausted int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM events WHERE task=? AND json_extract(body,'$.type')='task.retry_exhausted'", taskID).Scan(&exhausted); e != nil || exhausted != 1 {
		t.Fatalf("retry exhausted events=%d err=%v", exhausted, e)
	}

	nonRetry := retrySpec("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, true)
	j2, e := s.SubmitJob(uc, "retry-taxonomy", job.JSON(nonRetry))
	if e != nil {
		t.Fatal(e)
	}
	c2, _ := jobChildren(uc, s.db.SQL, j2.ID)
	if e = s.assign(uc, c2[0].id, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	ref := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", c2[0].id).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{Attempt: ref, CleanupConfirmed: true, ErrorCode: "VERIFICATION_FAILED"}); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM attempts WHERE task=?", c2[0].id).Scan(&count); e != nil || count != 1 {
		t.Fatalf("non-retryable error retried: count=%d err=%v", count, e)
	}
}

func TestRetryRequiresExplicitReplaySafety(t *testing.T) {
	s, uc, _, _ := offlineJobServer(t)
	defer s.Close()

	spec := retrySpec("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, false)
	if _, e := s.SubmitJob(uc, "unsafe-retry", job.JSON(spec)); status.Code(e) != codes.InvalidArgument {
		t.Fatalf("unsafe retry accepted: %v", e)
	}
}
