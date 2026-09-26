package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestArtifactLifecyclePinsAcceptedAndGCsOrphans(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()

	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j, err := s.SubmitJob(uc, "artifact-lifecycle", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(uc, s.db.SQL, j.ID)
	if err != nil || len(children) != 1 {
		t.Fatalf("children=%v err=%v", children, err)
	}
	taskID := children[0].id
	if err = s.assign(uc, taskID, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var attempt string
	if err = s.db.SQL.QueryRow("SELECT attempt FROM tasks WHERE id=?", taskID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	selected := registerResult(t, s, taskID, attempt)
	orphan := registerResult(t, s, taskID, attempt)

	ref := new(pb.AttemptRef)
	if err = s.db.SQL.QueryRow("SELECT id,generation,token FROM attempts WHERE id=?", attempt).Scan(&ref.AttemptId, &ref.Generation, &ref.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteAttempt(wc, &pb.CompleteRequest{Attempt: ref, Success: true, CleanupConfirmed: true, ArtifactIds: []string{selected}}); err != nil {
		t.Fatal(err)
	}

	var selectedState, orphanState string
	var orphanGC int64
	if err = s.db.SQL.QueryRow("SELECT state FROM artifacts WHERE id=?", selected).Scan(&selectedState); err != nil {
		t.Fatal(err)
	}
	if err = s.db.SQL.QueryRow("SELECT state,gc_after FROM artifacts WHERE id=?", orphan).Scan(&orphanState, &orphanGC); err != nil {
		t.Fatal(err)
	}
	if selectedState != "ACCEPTED" || orphanState != "ORPHANED" || orphanGC <= store.Now() {
		t.Fatalf("states selected=%s orphan=%s gc_after=%d", selectedState, orphanState, orphanGC)
	}
	var refs int
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM artifact_refs WHERE artifact=? AND ref_type='task_result' AND ref_id=?", selected, taskID).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("task result refs=%d err=%v", refs, err)
	}
	if _, err = s.db.SQL.Exec("UPDATE artifacts SET state='ORPHANED' WHERE id=?", selected); err == nil {
		t.Fatal("referenced accepted artifact was allowed to leave ACCEPTED")
	}

	list, err := s.ListArtifacts(uc, &pb.TaskRef{TaskId: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Artifacts) != 1 || list.Artifacts[0].ArtifactId != selected {
		t.Fatalf("non-accepted artifact leaked through list: %+v", list.Artifacts)
	}

	if _, err = s.db.SQL.Exec("UPDATE artifacts SET gc_after=? WHERE id=?", store.Now()-1, orphan); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileArtifactLifecycle(uc); err != nil {
		t.Fatal(err)
	}
	var deletedAt int64
	var path string
	if err = s.db.SQL.QueryRow("SELECT state,path,deleted_at FROM artifacts WHERE id=?", orphan).Scan(&orphanState, &path, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if orphanState != "DELETED" || path != "" || deletedAt == 0 {
		t.Fatalf("orphan tombstone state=%s path=%q deleted_at=%d", orphanState, path, deletedAt)
	}
	if _, err = os.Stat(filepath.Join(s.cfg.DataDir, "artifacts", orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan file still exists: %v", err)
	}
	if _, err = os.Stat(filepath.Join(s.cfg.DataDir, "artifacts", selected)); err != nil {
		t.Fatalf("accepted file removed: %v", err)
	}

	if err = s.reconcileArtifactLifecycle(uc); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactLifecycleResumesDeletingTombstone(t *testing.T) {
	s, uc, _, peer := offlineJobServer(t)
	defer s.Close()

	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("single")
	j, err := s.SubmitJob(uc, "artifact-delete-recovery", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	children, _ := jobChildren(uc, s.db.SQL, j.ID)
	if err = s.assign(uc, children[0].id, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	var attempt string
	if err = s.db.SQL.QueryRow("SELECT attempt FROM tasks WHERE id=?", children[0].id).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	id := registerResult(t, s, children[0].id, attempt)
	now := store.Now()
	if _, err = s.db.SQL.Exec("UPDATE artifacts SET state='ORPHANED',gc_after=?,updated=? WHERE id=?", now-1, now, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL.Exec("UPDATE artifacts SET state='DELETING',updated=? WHERE id=?", now, id); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileArtifactLifecycle(uc); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.SQL.QueryRow("SELECT state FROM artifacts WHERE id=?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "DELETED" {
		t.Fatalf("deleting state not recovered: %s", state)
	}
	if err = s.reconcileArtifactLifecycle(uc); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactLifecyclePinsReduceInputsAndJobResult(t *testing.T) {
	s, uc, wc, peer := offlineJobServer(t)
	defer s.Close()

	spec := (&jobHarness{commit: strings.Repeat("a", 40)}).spec("report_merge_v1")
	j, err := s.SubmitJob(uc, "artifact-refs", job.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	children, err := jobChildren(uc, s.db.SQL, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var mapArtifacts []string
	for _, child := range children {
		if child.stage != "map" {
			continue
		}
		if err = s.assign(uc, child.id, []*session{peer}); err != nil {
			t.Fatal(err)
		}
		mapArtifacts = append(mapArtifacts, completeFake(t, s, wc, child.id))
	}
	if err = s.advanceJob(uc, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range mapArtifacts {
		var n int
		if err = s.db.SQL.QueryRow("SELECT count(*) FROM artifact_refs WHERE artifact=? AND ref_type='reduce_input' AND ref_id=?", id, j.ID+":reduce").Scan(&n); err != nil || n != 1 {
			t.Fatalf("reduce ref artifact=%s count=%d err=%v", id, n, err)
		}
	}

	var reduceTask string
	if err = s.db.SQL.QueryRow("SELECT id FROM tasks WHERE job_id=? AND stage='reduce'", j.ID).Scan(&reduceTask); err != nil {
		t.Fatal(err)
	}
	if err = s.assign(uc, reduceTask, []*session{peer}); err != nil {
		t.Fatal(err)
	}
	finalArtifact := completeFake(t, s, wc, reduceTask)
	if err = s.advanceJob(uc, j.ID); err != nil {
		t.Fatal(err)
	}
	var finalRefs int
	if err = s.db.SQL.QueryRow("SELECT count(*) FROM artifact_refs WHERE artifact=? AND ref_type='job_result' AND ref_id=?", finalArtifact, j.ID).Scan(&finalRefs); err != nil || finalRefs != 1 {
		t.Fatalf("job result ref count=%d err=%v", finalRefs, err)
	}
}
