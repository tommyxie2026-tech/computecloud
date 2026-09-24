package server

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStageAndGenerationFencing(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()

	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, e := s.SubmitJob(uc, "generation-fencing", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	var stageID, stageState string
	if e = s.db.SQL.QueryRow("SELECT id,state FROM stages WHERE job_id=? AND kind='single'", j.ID).Scan(&stageID, &stageState); e != nil {
		t.Fatal(e)
	}
	if stageState != "READY" {
		t.Fatalf("initial stage state=%s", stageState)
	}

	children, e := jobChildren(uc, s.db.SQL, j.ID)
	if e != nil || len(children) != 1 {
		t.Fatalf("children=%v err=%v", children, e)
	}
	taskID := children[0].id
	if e = s.assign(uc, taskID, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	if e = s.db.SQL.QueryRow("SELECT state FROM stages WHERE id=?", stageID).Scan(&stageState); e != nil {
		t.Fatal(e)
	}
	if stageState != "RUNNING" {
		t.Fatalf("assigned stage state=%s", stageState)
	}

	old := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).Scan(&old.AttemptId, &old.Generation, &old.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if old.Generation != 1 {
		t.Fatalf("first generation=%d", old.Generation)
	}
	oldArtifact := registerResult(t, s, taskID, old.AttemptId)

	// v0.3.0 does not implement automatic retry yet. Simulate the hand-off
	// that the future retry controller will perform after cleanup is proven.
	if _, e = s.db.SQL.Exec("UPDATE attempts SET released=1 WHERE id=?", old.AttemptId); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.SQL.Exec("UPDATE tasks SET state='QUEUED',attempt='',worker='' WHERE id=?", taskID); e != nil {
		t.Fatal(e)
	}
	if e = s.assign(uc, taskID, []*session{peer}); e != nil {
		t.Fatal(e)
	}

	current := new(pb.AttemptRef)
	if e = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE task=? AND released=0", taskID).Scan(&current.AttemptId, &current.Generation, &current.LeaseToken); e != nil {
		t.Fatal(e)
	}
	if current.Generation != 2 || current.AttemptId == old.AttemptId {
		t.Fatalf("current attempt=%+v old=%+v", current, old)
	}
	var attempts int
	if e = s.db.SQL.QueryRow("SELECT count(*) FROM attempts WHERE task=?", taskID).Scan(&attempts); e != nil || attempts != 2 {
		t.Fatalf("attempt history=%d err=%v", attempts, e)
	}

	_, e = s.ReportEvents(wc, &pb.ReportRequest{Attempt: old, Events: []*pb.Event{{
		TaskId: taskID, AttemptId: old.AttemptId, Generation: old.Generation,
		EventId: "old-event", WorkerSeq: 1, Type: "attempt.progress", PayloadJson: []byte(`{"old":true}`),
	}}})
	if status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("stale event accepted: %v", e)
	}

	var oldState string
	if e = s.db.SQL.QueryRow("SELECT state FROM artifacts WHERE id=?", oldArtifact).Scan(&oldState); e != nil {
		t.Fatal(e)
	}
	if oldState != "STAGED" {
		t.Fatalf("old artifact unexpectedly published: %s", oldState)
	}

	newArtifact := registerResult(t, s, taskID, current.AttemptId)
	if _, e = s.CompleteAttempt(wc, &pb.CompleteRequest{
		Attempt: current, Success: true, CleanupConfirmed: true, ArtifactIds: []string{newArtifact},
	}); e != nil {
		t.Fatal(e)
	}
	var newState string
	if e = s.db.SQL.QueryRow("SELECT state FROM artifacts WHERE id=?", newArtifact).Scan(&newState); e != nil {
		t.Fatal(e)
	}
	if newState != "ACCEPTED" {
		t.Fatalf("current artifact state=%s", newState)
	}
	if e = s.db.SQL.QueryRow("SELECT state FROM stages WHERE id=?", stageID).Scan(&stageState); e != nil {
		t.Fatal(e)
	}
	// Stage becomes terminal when the Job controller observes the completed Task.
	if e = s.advanceJob(uc, j.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.db.SQL.QueryRow("SELECT state FROM stages WHERE id=?", stageID).Scan(&stageState); e != nil {
		t.Fatal(e)
	}
	if stageState != "SUCCEEDED" {
		t.Fatalf("final stage state=%s", stageState)
	}
}

func TestSecondActiveAttemptRejectedBySchema(t *testing.T) {
	s, uc, _, peer := offlineJobServer(t)
	defer s.Close()
	spec := (&jobHarness{commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}).spec("single")
	j, e := s.SubmitJob(uc, "active-attempt-unique", job.JSON(spec))
	if e != nil {
		t.Fatal(e)
	}
	children, e := jobChildren(uc, s.db.SQL, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.assign(uc, children[0].id, []*session{peer}); e != nil {
		t.Fatal(e)
	}
	var attempt, worker, epoch, token string
	var generation, lease int64
	if e = s.db.SQL.QueryRow("SELECT id,worker,epoch,generation,token,lease_until FROM attempts WHERE task=? AND released=0", children[0].id).Scan(&attempt, &worker, &epoch, &generation, &token, &lease); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.SQL.Exec("INSERT INTO attempts(id,task,worker,epoch,generation,token,lease_until,released) VALUES('duplicate-active',?,?,?,?,?,?,0)", children[0].id, worker, epoch, generation+1, token+"x", lease); e == nil {
		t.Fatal("schema accepted a second active attempt")
	}
}
