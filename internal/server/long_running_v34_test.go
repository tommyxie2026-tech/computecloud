package server

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type eventStreamFixture struct {
	ctx  context.Context
	sent []*pb.Event
}

func (s *eventStreamFixture) Send(e *pb.Event) error { s.sent = append(s.sent, e); return nil }
func (s *eventStreamFixture) SetHeader(metadata.MD) error { return nil }
func (s *eventStreamFixture) SendHeader(metadata.MD) error { return nil }
func (s *eventStreamFixture) SetTrailer(metadata.MD) {}
func (s *eventStreamFixture) Context() context.Context { return s.ctx }
func (s *eventStreamFixture) SendMsg(any) error { return nil }
func (s *eventStreamFixture) RecvMsg(any) error { return nil }

func TestLongRunningDeadlineExtensionUsesLeaseLiveness(t *testing.T) {
	h := newJobHarness(t, true, func(c *config.Server) {
		c.Jobs.MaxTotalRuntimeSeconds = 86400
		c.Jobs.MaxDeadlineExtendSeconds = 10
		c.Jobs.MaxTaskEvents = 100
	})
	spec := h.spec("single")
	spec.Input.Text = "long-job"
	spec.Limits.TimeoutSeconds = 3

	j, e := h.s.SubmitJob(h.ctx, "long-deadline", job.JSON(spec))
	if e != nil { t.Fatal(e) }

	var taskID, attemptID string
	var firstRenew, firstLease int64
	for {
		e = h.s.db.SQL.QueryRow("SELECT t.id,t.attempt,coalesce(a.last_renewed,0),coalesce(a.lease_until,0) FROM tasks t LEFT JOIN attempts a ON a.id=t.attempt WHERE t.job_id=? AND t.state='RUNNING'", j.ID).Scan(&taskID, &attemptID, &firstRenew, &firstLease)
		if e == nil && firstRenew > 0 { break }
		select {
		case <-h.ctx.Done(): t.Fatal("long job never reached RUNNING")
		case <-time.After(25 * time.Millisecond):
		}
	}

	oldDeadline := j.Deadline
	newDeadline := oldDeadline + 5000
	code, body := h.request(t, "POST", "/v1/jobs/"+j.ID+"/deadline", "", job.JSON(ExtendDeadlineRequest{OperationID: "extend-long-1", NewDeadlineMS: newDeadline}))
	if code != 202 { t.Fatalf("extend: %d %s", code, body) }
	code, body = h.request(t, "POST", "/v1/jobs/"+j.ID+"/deadline", "", job.JSON(ExtendDeadlineRequest{OperationID: "extend-long-1", NewDeadlineMS: newDeadline}))
	if code != 200 || !strings.Contains(string(body), "\"existing\":true") { t.Fatalf("deadline replay: %d %s", code, body) }

	var taskDeadline int64
	if e = h.s.db.SQL.QueryRow("SELECT deadline FROM tasks WHERE id=?", taskID).Scan(&taskDeadline); e != nil || taskDeadline != newDeadline {
		t.Fatalf("task deadline=%d err=%v", taskDeadline, e)
	}

	var secondRenew, secondLease int64
	renewDeadline := time.Now().Add(3500 * time.Millisecond)
	for {
		if e = h.s.db.SQL.QueryRow("SELECT last_renewed,lease_until FROM attempts WHERE id=?", attemptID).Scan(&secondRenew, &secondLease); e != nil { t.Fatal(e) }
		if secondRenew > firstRenew && secondLease > firstLease {
			break
		}
		if time.Now().After(renewDeadline) {
			t.Fatalf("lease did not renew without runtime output: renew %d->%d lease %d->%d", firstRenew, secondRenew, firstLease, secondLease)
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := h.wait(t, j.ID)
	if got.State != "SUCCEEDED" { t.Fatalf("extended long job failed: %+v", got) }
	if got.Deadline != newDeadline { t.Fatalf("job deadline=%d want=%d", got.Deadline, newDeadline) }
	if _, e = h.s.ExtendJobDeadline(h.ctx, j.ID, ExtendDeadlineRequest{OperationID: "after-terminal", NewDeadlineMS: newDeadline + 1000}); status.Code(e) != codes.FailedPrecondition {
		t.Fatalf("terminal deadline extension accepted: %v", e)
	}
}

func TestLongRunningDeadlineDoesNotExtendImplicitly(t *testing.T) {
	h := newJobHarness(t, true)
	spec := h.spec("single")
	spec.Input.Text = "long-job"
	spec.Limits.TimeoutSeconds = 1
	j, e := h.s.SubmitJob(h.ctx, "long-timeout", job.JSON(spec))
	if e != nil { t.Fatal(e) }
	got := h.wait(t, j.ID)
	if got.State == "SUCCEEDED" { t.Fatalf("deadline-expired job published success: %+v", got) }
	if got.ErrorCode != "DEADLINE_EXCEEDED" && got.StopReason != "DEADLINE_EXCEEDED" { t.Fatalf("unexpected timeout outcome: %+v", got) }
	var accepted int
	if e = h.s.db.SQL.QueryRow("SELECT count(*) FROM artifacts a JOIN tasks t ON t.id=a.task WHERE t.job_id=? AND a.state='ACCEPTED'", j.ID).Scan(&accepted); e != nil { t.Fatal(e) }
	if accepted != 0 { t.Fatalf("deadline-expired job published %d accepted artifacts", accepted) }
}

func TestTaskEventCompactionHasExplicitCursorFloor(t *testing.T) {
	h := newJobHarness(t, false, func(c *config.Server) { c.Jobs.MaxTaskEvents = 100 })
	j, e := h.s.SubmitJob(h.ctx, "event-compaction", job.JSON(h.spec("single")))
	if e != nil { t.Fatal(e) }
	children, e := jobChildren(h.ctx, h.s.db.SQL, j.ID)
	if e != nil || len(children) != 1 { t.Fatalf("children=%v err=%v", children, e) }
	taskID := children[0].id
	e = h.s.db.Tx(h.ctx, func(q store.Query) error {
		for i := int64(1); i <= 150; i++ {
			seq := i
			if err := appendEvent(h.ctx, q, &pb.Event{TaskId: taskID, AttemptId: "fixture-attempt", Generation: 1, EventId: store.ID(), WorkerSeq: i, Type: "message.delta", PayloadJson: []byte("{\"delta\":\"x\"}")}, &seq); err != nil { return err }
		}
		return nil
	})
	if e != nil { t.Fatal(e) }
	if e = h.s.compactTaskEvents(h.ctx); e != nil { t.Fatal(e) }

	var floor, last, retained, dedup int64
	if e = h.s.db.SQL.QueryRow("SELECT event_floor_seq,seq FROM tasks WHERE id=?", taskID).Scan(&floor, &last); e != nil { t.Fatal(e) }
	if e = h.s.db.SQL.QueryRow("SELECT count(*) FROM events WHERE task=?", taskID).Scan(&retained); e != nil { t.Fatal(e) }
	if e = h.s.db.SQL.QueryRow("SELECT count(*) FROM event_dedup WHERE attempt='fixture-attempt'").Scan(&dedup); e != nil { t.Fatal(e) }
	if floor != last-100 || retained != 100 || dedup != 150 { t.Fatalf("floor=%d last=%d retained=%d dedup=%d", floor, last, retained, dedup) }

	stream := &eventStreamFixture{ctx: h.ctx}
	e = h.s.WatchEvents(&pb.WatchRequest{TaskId: taskID, AfterSeq: 0}, stream)
	if status.Code(e) != codes.OutOfRange || !strings.Contains(status.Convert(e).Message(), "EVENT_CURSOR_COMPACTED") {
		t.Fatalf("compacted cursor was not explicit: %v", e)
	}
}
