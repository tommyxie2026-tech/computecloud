package server

import (
	"fmt"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
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

func TestSpecificReadinessAndCapabilityBlockers(t *testing.T) {
	t.Run("no-ready-worker", func(t *testing.T) {
		s, uc, _, _ := offlineJobServer(t)
		defer s.Close()
		s.cfg.Credentials["account"] = 8
		spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
		j, err := s.SubmitJob(uc, "no-ready", job.JSON(spec))
		if err != nil {
			t.Fatal(err)
		}
		var task string
		if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		if err = s.assign(uc, task, nil); err != nil {
			t.Fatal(err)
		}
		var blocker string
		if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", task).Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		if blocker != "NO_READY_WORKER" {
			t.Fatalf("blocker=%s want=NO_READY_WORKER", blocker)
		}
	})

	t.Run("capability-mismatch", func(t *testing.T) {
		s, uc, _, peer := offlineJobServer(t)
		defer s.Close()
		s.cfg.Credentials["account"] = 8
		peer.hello.Runtimes = nil
		spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
		j, err := s.SubmitJob(uc, "capability-mismatch", job.JSON(spec))
		if err != nil {
			t.Fatal(err)
		}
		var task string
		if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		if err = s.assign(uc, task, []*session{peer}); err != nil {
			t.Fatal(err)
		}
		var blocker string
		if err = s.db.SQL.QueryRow("SELECT blocker FROM tasks WHERE id=?", task).Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		if blocker != "TEMPLATE_OR_CAPABILITY_MISMATCH" {
			t.Fatalf("blocker=%s want=TEMPLATE_OR_CAPABILITY_MISMATCH", blocker)
		}
	})
}

func TestChooseWorkerRotatesEqualMinimumLoad(t *testing.T) {
	loads := []workerLoad{{peer: &session{hello: &pb.WorkerHello{WorkerId: "worker-a"}}, active: 2}, {peer: &session{hello: &pb.WorkerHello{WorkerId: "worker-b"}}, active: 2}}
	got, cursor := chooseLeastLoadedWorker(loads, "")
	if got == nil || got.hello.WorkerId != "worker-a" || cursor != "worker-a" {
		t.Fatalf("first selection=%v cursor=%q, want worker-a", got, cursor)
	}
	got, cursor = chooseLeastLoadedWorker(loads, cursor)
	if got == nil || got.hello.WorkerId != "worker-b" || cursor != "worker-b" {
		t.Fatalf("second selection=%v cursor=%q, want worker-b", got, cursor)
	}
}

func TestChooseWorkerUsesMinimumLoadBeforeRotation(t *testing.T) {
	loads := []workerLoad{{peer: &session{hello: &pb.WorkerHello{WorkerId: "worker-a"}}, active: 3}, {peer: &session{hello: &pb.WorkerHello{WorkerId: "worker-b"}}, active: 1}}
	got, cursor := chooseLeastLoadedWorker(loads, "worker-b")
	if got == nil || got.hello.WorkerId != "worker-b" || cursor != "worker-b" {
		t.Fatalf("selection=%v cursor=%q, want lower-load worker-b", got, cursor)
	}
}

func TestChooseWorkerIgnoresPeerOrder(t *testing.T) {
	mk := func(ids ...string) []workerLoad {
		loads := make([]workerLoad, 0, len(ids))
		for _, id := range ids {
			loads = append(loads, workerLoad{peer: &session{hello: &pb.WorkerHello{WorkerId: id}}, active: 0})
		}
		return loads
	}
	first, _ := chooseLeastLoadedWorker(mk("worker-b", "worker-a"), "")
	second, _ := chooseLeastLoadedWorker(mk("worker-a", "worker-b"), "")
	if first.hello.WorkerId != second.hello.WorkerId {
		t.Fatalf("peer order changed first selection: %q vs %q", first.hello.WorkerId, second.hello.WorkerId)
	}
}

func TestAssignRotatesEqualLoadWorkers(t *testing.T) {
	s, uc, _, workerA := offlineJobServer(t)
	defer s.Close()
	s.cfg.Credentials["account"] = 8
	workerA.hello.Slots = 4
	workerBHello := *workerA.hello
	workerBHello.WorkerId = "worker-b"
	workerBHello.Epoch = "epoch-b"
	workerB := &session{hello: &workerBHello, identity: workerA.identity}
	if _, err := s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", workerBHello.WorkerId, workerBHello.Epoch, encode(&workerBHello), store.Now()); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"w", "worker-b", "w"} {
		spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
		j, err := s.SubmitJob(uc, fmt.Sprintf("worker-rotation-%d", i), job.JSON(spec))
		if err != nil {
			t.Fatal(err)
		}
		var task string
		if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		if err = s.assign(uc, task, []*session{workerB, workerA}); err != nil {
			t.Fatal(err)
		}
		var got string
		if err = s.db.SQL.QueryRow("SELECT worker FROM tasks WHERE id=?", task).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("assignment %d chose %q, want %q", i+1, got, want)
		}
	}
}

func TestAssignSkipsWorkerAtCapacity(t *testing.T) {
	s, uc, _, workerA := offlineJobServer(t)
	defer s.Close()
	s.cfg.Credentials["account"] = 8
	workerA.hello.Slots = 1
	workerBHello := *workerA.hello
	workerBHello.WorkerId = "worker-b"
	workerBHello.Epoch = "epoch-b"
	workerB := &session{hello: &workerBHello, identity: workerA.identity}
	if _, err := s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", workerBHello.WorkerId, workerBHello.Epoch, encode(&workerBHello), store.Now()); err != nil {
		t.Fatal(err)
	}

	var tasks []string
	for i := 0; i < 2; i++ {
		spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
		j, err := s.SubmitJob(uc, fmt.Sprintf("worker-capacity-%d", i), job.JSON(spec))
		if err != nil {
			t.Fatal(err)
		}
		var task string
		if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	if err := s.assign(uc, tasks[0], []*session{workerA, workerB}); err != nil {
		t.Fatal(err)
	}
	if err := s.assign(uc, tasks[1], []*session{workerA, workerB}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.db.SQL.QueryRow("SELECT worker FROM tasks WHERE id=?", tasks[1]).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "worker-b" {
		t.Fatalf("assignment at capacity chose %q, want worker-b", got)
	}
}

func TestAssignExcludesIncompatibleWorkerFromRotation(t *testing.T) {
	s, uc, _, workerA := offlineJobServer(t)
	defer s.Close()
	s.cfg.Credentials["account"] = 8
	workerA.hello.Slots = 4
	workerBHello := *workerA.hello
	workerBHello.WorkerId = "worker-b"
	workerBHello.Epoch = "epoch-b"
	workerBHello.Runtimes = nil
	workerB := &session{hello: &workerBHello, identity: workerA.identity}
	if _, err := s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", workerBHello.WorkerId, workerBHello.Epoch, encode(&workerBHello), store.Now()); err != nil {
		t.Fatal(err)
	}
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j, err := s.SubmitJob(uc, "worker-compatible-only", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	var task string
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if err = s.assign(uc, task, []*session{workerB, workerA}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = s.db.SQL.QueryRow("SELECT worker FROM tasks WHERE id=?", task).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != workerA.hello.WorkerId {
		t.Fatalf("assignment chose incompatible worker %q", got)
	}
}

func TestAssignDoesNotAdvanceCursorOnFailure(t *testing.T) {
	s, uc, _, workerA := offlineJobServer(t)
	defer s.Close()
	s.cfg.Credentials["account"] = 8
	workerA.hello.Slots = 3
	workerBHello := *workerA.hello
	workerBHello.WorkerId = "worker-b"
	workerBHello.Epoch = "epoch-b"
	workerB := &session{hello: &workerBHello, identity: workerA.identity}
	if _, err := s.db.SQL.Exec("INSERT INTO workers VALUES(?,?,?,?)", workerBHello.WorkerId, workerBHello.Epoch, encode(&workerBHello), store.Now()); err != nil {
		t.Fatal(err)
	}
	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j, err := s.SubmitJob(uc, "worker-rotation-failure", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	var task string
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=?", j.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec(`CREATE TRIGGER fail_start_command BEFORE INSERT ON commands BEGIN SELECT RAISE(ABORT, 'injected command failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.assign(uc, task, []*session{workerA, workerB}); err == nil {
		t.Fatal("expected injected transaction failure")
	}
	if s.workerCursor != "" {
		t.Fatalf("cursor advanced after rollback: %q", s.workerCursor)
	}
}
