package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestAgentControlReadJobCollectionStableSnapshot(t *testing.T) {
	h := newJobHarness(t, false)
	var ids []string
	for _, key := range []string{"list-a", "list-b", "list-c"} {
		j, err := h.s.SubmitJob(h.ctx, key, job.JSON(h.spec("single")))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		time.Sleep(2 * time.Millisecond)
	}

	other := rpcutil.WithPrincipal(h.ctx, rpcutil.Principal{Identity: config.Identity{
		Owner: "other", Projects: []string{"project"}, Credentials: []string{"account"},
		Scopes: []string{"jobs:submit", "jobs:read"},
	}})
	if _, err := h.s.SubmitJob(other, "other-list", job.JSON(h.spec("single"))); err != nil {
		t.Fatal(err)
	}

	code, body := h.request(t, "GET", "/v1/jobs?limit=2", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list page 1: %d %s", code, body)
	}
	var first ControlJobList
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.ServerEpoch == "" || first.SnapshotMS == 0 || len(first.Jobs) != 2 || !first.HasMore || first.Next == nil {
		t.Fatalf("invalid first page: %+v", first)
	}
	for _, item := range first.Jobs {
		if item.Project != "project" {
			t.Fatalf("unexpected project projection: %+v", item)
		}
	}
	time.Sleep(2 * time.Millisecond)
	late, err := h.s.SubmitJob(h.ctx, "list-late", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}

	q := url.Values{}
	q.Set("limit", "2")
	q.Set("epoch", first.ServerEpoch)
	q.Set("snapshot_ms", strconv.FormatInt(first.SnapshotMS, 10))
	q.Set("before_created_ms", strconv.FormatInt(first.Next.CreatedMS, 10))
	q.Set("before_id", first.Next.JobID)
	code, body = h.request(t, "GET", "/v1/jobs?"+q.Encode(), "", nil)
	if code != http.StatusOK {
		t.Fatalf("list page 2: %d %s", code, body)
	}
	var second ControlJobList
	if err = json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range append(first.Jobs, second.Jobs...) {
		seen[item.JobID] = true
	}
	if seen[late.ID] {
		t.Fatalf("late job leaked into frozen snapshot: %s", late.ID)
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("job missing across pages: %s seen=%v", id, seen)
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("owner isolation failed or duplicate pagination: seen=%v", seen)
	}

	code, body = h.request(t, "GET", "/v1/jobs?epoch=stale&limit=1", "", nil)
	if code != http.StatusConflict {
		t.Fatalf("stale epoch should reset snapshot: %d %s", code, body)
	}
}

func TestAgentControlReadWorkerCollectionScopedAndProjected(t *testing.T) {
	h := newJobHarness(t, true)
	deadline := time.Now().Add(10 * time.Second)
	var workers *ControlWorkerList
	for time.Now().Before(deadline) {
		got, err := h.s.ListControlWorkers(h.ctx, "", "", 10)
		if err == nil && len(got.Workers) == 2 {
			workers = got
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if workers == nil {
		t.Fatal("fixture workers did not become visible")
	}
	if workers.ServerEpoch == "" || workers.ObservedMS == 0 {
		t.Fatalf("missing worker read watermark: %+v", workers)
	}
	for _, worker := range workers.Workers {
		if !worker.Online || worker.WorkerID == "" || len(worker.Runtimes) == 0 {
			t.Fatalf("invalid worker projection: %+v", worker)
		}
		for _, runtime := range worker.Runtimes {
			if runtime.Profile == "" || runtime.Version == "" || len(runtime.Control) == 0 {
				t.Fatalf("runtime capability projection incomplete: %+v", runtime)
			}
		}
	}

	h.s.cfg.Workers = append(h.s.cfg.Workers, config.Identity{WorkerID: "hidden-worker", Projects: []string{"hidden"}})
	hello := &pb.WorkerHello{WorkerId: "hidden-worker", Epoch: "hidden", Slots: 1}
	if _, err := h.s.db.SQL.Exec("INSERT INTO workers(id,epoch,hello,seen) VALUES(?,?,?,?)",
		"hidden-worker", "hidden", encode(hello), store.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.ListControlWorkers(h.ctx, workers.ServerEpoch, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range got.Workers {
		if worker.WorkerID == "hidden-worker" {
			t.Fatal("worker outside authorized projects leaked into C1 read model")
		}
	}

	code, body := h.request(t, "GET", "/v1/workers?limit=1", "", nil)
	if code != http.StatusOK {
		t.Fatalf("worker HTTP list: %d %s", code, body)
	}
	var page ControlWorkerList
	if err = json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Workers) != 1 || !page.HasMore || page.Next == "" {
		t.Fatalf("worker pagination invalid: %+v", page)
	}
	code, body = h.request(t, "GET", "/v1/workers?epoch=stale", "", nil)
	if code != http.StatusConflict {
		t.Fatalf("stale worker epoch should reset view: %d %s", code, body)
	}
}
