package server

import (
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFairGroupOrderRoundRobinsProjectsAndGroups(t *testing.T) {
	groups := []queueGroup{
		{project: "a", group: "a1", effective: 5, oldest: 1},
		{project: "a", group: "a2", effective: 5, oldest: 2},
		{project: "b", group: "b1", effective: 5, oldest: 1},
		{project: "b", group: "b2", effective: 5, oldest: 2},
		{project: "c", group: "c-high", effective: 8, oldest: 9},
	}
	got := fairGroupOrder(groups, map[int32]string{}, map[string]string{})
	want := []string{"c-high", "a1", "b1", "a2", "b2"}
	if len(got) != len(want) {
		t.Fatalf("order len=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].group != want[i] {
			t.Fatalf("order[%d]=%s want=%s", i, got[i].group, want[i])
		}
	}

	got = fairGroupOrder(groups, map[int32]string{5: "a"}, map[string]string{"5:a": "a1"})
	want = []string{"c-high", "b1", "a2", "b2", "a1"}
	for i := range want {
		if got[i].group != want[i] {
			t.Fatalf("rotated order[%d]=%s want=%s", i, got[i].group, want[i])
		}
	}
}

func TestSchedulerAgingIsBoundedAndMonotonic(t *testing.T) {
	now := int64(1_000_000)
	aging := int64(10_000)
	for _, tc := range []struct {
		base int32
		age  int64
		want int32
	}{
		{0, 0, 0},
		{1, 9_999, 1},
		{1, 10_000, 2},
		{4, 30_000, 7},
		{9, 99_000, 10},
		{10, 99_000, 10},
	} {
		got := effectivePriority(tc.base, now-tc.age, now, aging)
		if got != tc.want {
			t.Fatalf("base=%d age=%d got=%d want=%d", tc.base, tc.age, got, tc.want)
		}
	}
}

func TestJobPriorityPropagatesToManagedTasks(t *testing.T) {
	s, uc, _, _ := offlineJobServer(t)
	defer s.Close()

	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	spec.Limits.Priority = 7
	j, err := s.SubmitJob(uc, "job-priority", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.SQL.Query("SELECT priority FROM tasks WHERE job_id=? ORDER BY id", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var priority int32
		if err = rows.Scan(&priority); err != nil {
			t.Fatal(err)
		}
		if priority != 7 {
			t.Fatalf("managed task priority=%d want=7", priority)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("map task count=%d want=2", count)
	}
}

func TestFairSchedulerDoesNotBypassRetryAfter(t *testing.T) {
	s, uc, _, _ := offlineJobServer(t)
	defer s.Close()
	s.cfg.Jobs.SchedulerAgingSeconds = 1

	old := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j1, err := s.SubmitJob(uc, "aging-backoff", job.JSON(old))
	if err != nil {
		t.Fatal(err)
	}
	current := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j2, err := s.SubmitJob(uc, "aging-runnable", job.JSON(current))
	if err != nil {
		t.Fatal(err)
	}
	var t1 string
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j1.ID).Scan(&t1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("UPDATE tasks SET updated=?,priority=0,retry_after=? WHERE id=?", store.Now()-60_000, store.Now()+60_000, t1); err != nil {
		t.Fatal(err)
	}
	groups, err := s.queuedGroups(uc, store.Now())
	if err != nil {
		t.Fatal(err)
	}
	seen1, seen2 := false, false
	for _, g := range groups {
		if g.group == j1.ID {
			seen1 = true
		}
		if g.group == j2.ID {
			seen2 = true
		}
	}
	if seen1 || !seen2 {
		t.Fatalf("retry_after bypassed by aging: old=%v runnable=%v groups=%+v", seen1, seen2, groups)
	}
}

func TestQueueBackpressurePreservesIdempotentReplay(t *testing.T) {
	s, uc, _, _ := offlineJobServer(t)
	defer s.Close()
	s.cfg.Jobs.MaxQueuedTasks = 1
	s.cfg.Jobs.MaxQueuedTasksPerProject = 1

	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	first, err := s.SubmitJob(uc, "queue-one", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.SubmitJob(uc, "queue-one", job.JSON(spec))
	if err != nil || !replay.Existing || replay.ID != first.ID {
		t.Fatalf("accepted replay rejected under backpressure: replay=%+v err=%v", replay, err)
	}
	_, err = s.SubmitJob(uc, "queue-two", job.JSON(spec))
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(status.Convert(err).Message(), "QUEUE_BACKPRESSURE") {
		t.Fatalf("queue overflow not explicit: %v", err)
	}
}

func TestSpecificConcurrencyBlockers(t *testing.T) {
	cases := []struct {
		name        string
		credentials int
		project     int
		want        string
	}{
		{"credential", 1, 8, "CREDENTIAL_CONCURRENCY_EXHAUSTED"},
		{"project", 8, 1, "PROJECT_CONCURRENCY_EXHAUSTED"},
		{"worker", 8, 8, "WORKER_CAPACITY_EXHAUSTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, uc, _, peer := offlineJobServer(t)
			defer s.Close()
			s.cfg.Credentials["account"] = tc.credentials
			s.cfg.MaxProjectTasks = tc.project

			spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
			j1, err := s.SubmitJob(uc, "blocker-1", job.JSON(spec))
			if err != nil {
				t.Fatal(err)
			}
			j2, err := s.SubmitJob(uc, "blocker-2", job.JSON(spec))
			if err != nil {
				t.Fatal(err)
			}
			var a, b string
			if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j1.ID).Scan(&a); err != nil {
				t.Fatal(err)
			}
			if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j2.ID).Scan(&b); err != nil {
				t.Fatal(err)
			}
			if err = s.assign(uc, a, []*session{peer}); err != nil {
				t.Fatal(err)
			}
			if err = s.assign(uc, b, []*session{peer}); err != nil {
				t.Fatal(err)
			}
			var blocker string
			if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", b).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if blocker != tc.want {
				t.Fatalf("blocker=%s want=%s", blocker, tc.want)
			}
		})
	}
}
