package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ControlJobListItem struct {
	JobID      string `json:"job_id"`
	Project    string `json:"project"`
	State      string `json:"state"`
	Mode       string `json:"mode"`
	Version    int64  `json:"version,string"`
	LastSeq    int64  `json:"last_seq,string"`
	CreatedMS  int64  `json:"created_at_ms,string"`
	UpdatedMS  int64  `json:"updated_at_ms,string"`
	DeadlineMS int64  `json:"deadline_ms,string"`
	StopReason string `json:"stop_reason,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type ControlJobCursor struct {
	CreatedMS int64  `json:"created_at_ms,string"`
	JobID     string `json:"job_id"`
}

type ControlJobList struct {
	ServerEpoch string               `json:"server_epoch"`
	SnapshotMS  int64                `json:"snapshot_ms,string"`
	SnapshotID  string               `json:"snapshot_id"`
	Jobs        []ControlJobListItem `json:"jobs"`
	Next        *ControlJobCursor    `json:"next,omitempty"`
	HasMore     bool                 `json:"has_more"`
}

type ControlWorkerRuntime struct {
	Profile      string   `json:"profile"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities"`
	Control      []string `json:"control_capabilities"`
}

type ControlWorkerView struct {
	WorkerID   string                 `json:"worker_id"`
	Online     bool                   `json:"online"`
	Slots      int32                  `json:"slots"`
	Active     int32                  `json:"active"`
	LastSeenMS int64                  `json:"last_seen_ms,string"`
	Runtimes   []ControlWorkerRuntime `json:"runtimes"`
}

type ControlWorkerList struct {
	ServerEpoch string              `json:"server_epoch"`
	ObservedMS  int64               `json:"observed_at_ms,string"`
	Workers     []ControlWorkerView `json:"workers"`
	Next        string              `json:"next,omitempty"`
	HasMore     bool                `json:"has_more"`
}

func validateSnapshotEpoch(current, supplied string) error {
	if supplied != "" && supplied != current {
		return status.Error(codes.Aborted, "SNAPSHOT_EPOCH_CHANGED")
	}
	return nil
}

func projectsJSON(projects []string) string {
	b, _ := json.Marshal(projects)
	return string(b)
}

func (s *Server) ListControlJobs(ctx context.Context, epoch string, snapshotMS int64, snapshotID string, beforeCreatedMS int64, beforeID string, limit int) (*ControlJobList, error) {
	p, err := rpcutil.Require(ctx, "jobs:read", false)
	if err != nil {
		return nil, err
	}
	if err = validateSnapshotEpoch(s.controlEpoch, epoch); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid page limit")
	}
	if snapshotMS < 0 || beforeCreatedMS < 0 || (beforeCreatedMS == 0) != (beforeID == "") {
		return nil, status.Error(codes.InvalidArgument, "invalid job cursor")
	}
	if snapshotMS == 0 {
		err = s.db.SQL.QueryRowContext(ctx, `SELECT created,id FROM jobs
			WHERE owner=? AND project IN (SELECT value FROM json_each(?))
			ORDER BY created DESC,id DESC LIMIT 1`,
			p.Identity.Owner, projectsJSON(p.Identity.Projects)).Scan(&snapshotMS, &snapshotID)
		if errors.Is(err, sql.ErrNoRows) {
			snapshotMS = store.Now()
			snapshotID = ""
		} else if err != nil {
			return nil, dbErr(err)
		}
	} else if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	if beforeCreatedMS > snapshotMS {
		return nil, status.Error(codes.InvalidArgument, "invalid job cursor")
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT id,project,state,mode,version,seq,created,updated,deadline,stop_reason,error_code
		FROM jobs
		WHERE owner=?
		  AND project IN (SELECT value FROM json_each(?))
		  AND (created<? OR (created=? AND (?='' OR id<=?)))
		  AND (?=0 OR created<? OR (created=? AND id<?))
		ORDER BY created DESC,id DESC
		LIMIT ?`,
		p.Identity.Owner, projectsJSON(p.Identity.Projects),
		snapshotMS, snapshotMS, snapshotID, snapshotID,
		beforeCreatedMS, beforeCreatedMS, beforeCreatedMS, beforeID, limit+1)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()

	out := &ControlJobList{ServerEpoch: s.controlEpoch, SnapshotMS: snapshotMS, SnapshotID: snapshotID, Jobs: []ControlJobListItem{}}
	for rows.Next() {
		var item ControlJobListItem
		if err = rows.Scan(&item.JobID, &item.Project, &item.State, &item.Mode, &item.Version, &item.LastSeq,
			&item.CreatedMS, &item.UpdatedMS, &item.DeadlineMS, &item.StopReason, &item.ErrorCode); err != nil {
			return nil, dbErr(err)
		}
		out.Jobs = append(out.Jobs, item)
	}
	if err = rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	if len(out.Jobs) > limit {
		out.HasMore = true
		out.Jobs = out.Jobs[:limit]
	}
	if out.HasMore && len(out.Jobs) > 0 {
		last := out.Jobs[len(out.Jobs)-1]
		out.Next = &ControlJobCursor{CreatedMS: last.CreatedMS, JobID: last.JobID}
	}
	return out, nil
}

func workerProjects(cfg config.Server, workerID string) []string {
	for _, identity := range cfg.Workers {
		if identity.WorkerID == workerID {
			return identity.Projects
		}
	}
	return nil
}

func visibleProject(user, worker []string) bool {
	for _, p := range user {
		if config.Contains(worker, p) {
			return true
		}
	}
	return false
}

func runtimeView(r *pb.Runtime) ControlWorkerRuntime {
	out := ControlWorkerRuntime{Profile: r.Profile, Version: r.Version, Capabilities: append([]string(nil), r.Capabilities...)}
	sort.Strings(out.Capabilities)
	for _, capability := range controlCapabilities(r.Capabilities) {
		out.Control = append(out.Control, string(capability))
	}
	sort.Strings(out.Control)
	return out
}

func (s *Server) ListControlWorkers(ctx context.Context, epoch, after string, limit int) (*ControlWorkerList, error) {
	p, err := rpcutil.Require(ctx, "jobs:read", false)
	if err != nil {
		return nil, err
	}
	if err = validateSnapshotEpoch(s.controlEpoch, epoch); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid page limit")
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT w.id,w.hello,w.seen,
		(SELECT count(*) FROM attempts a WHERE a.worker=w.id AND a.released=0)
		FROM workers w WHERE w.id>? ORDER BY w.id`, after)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()

	out := &ControlWorkerList{ServerEpoch: s.controlEpoch, ObservedMS: store.Now(), Workers: []ControlWorkerView{}}
	for rows.Next() {
		var id string
		var raw []byte
		var seen int64
		var active int32
		if err = rows.Scan(&id, &raw, &seen, &active); err != nil {
			return nil, dbErr(err)
		}
		if !visibleProject(p.Identity.Projects, workerProjects(s.cfg, id)) {
			continue
		}
		hello := new(pb.WorkerHello)
		if err = decode(raw, hello); err != nil {
			return nil, dbErr(err)
		}
		view := ControlWorkerView{WorkerID: id, Slots: hello.Slots, Active: active, LastSeenMS: seen}
		s.mu.Lock()
		view.Online = s.peers[id] != nil
		s.mu.Unlock()
		view.Online = view.Online && out.ObservedMS-seen < int64(s.cfg.LeaseSeconds)*1000
		for _, runtime := range hello.Runtimes {
			view.Runtimes = append(view.Runtimes, runtimeView(runtime))
		}
		sort.Slice(view.Runtimes, func(i, j int) bool { return view.Runtimes[i].Profile < view.Runtimes[j].Profile })
		out.Workers = append(out.Workers, view)
		if len(out.Workers) == limit+1 {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	if len(out.Workers) > limit {
		out.HasMore = true
		out.Workers = out.Workers[:limit]
	}
	if out.HasMore && len(out.Workers) > 0 {
		out.Next = out.Workers[len(out.Workers)-1].WorkerID
	}
	return out, nil
}

func queryInt64(r *http.Request, key string) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid integer query")
	}
	return value, nil
}

func (s *Server) httpControlJobs(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, 25, 100)
	if err != nil {
		httpError(w, err)
		return
	}
	snapshot, err := queryInt64(r, "snapshot_ms")
	if err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid snapshot"))
		return
	}
	before, err := queryInt64(r, "before_created_ms")
	if err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid job cursor"))
		return
	}
	value, err := s.ListControlJobs(r.Context(), r.URL.Query().Get("epoch"), snapshot, r.URL.Query().Get("snapshot_id"), before, r.URL.Query().Get("before_id"), limit)
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, value)
}

func (s *Server) httpControlWorkers(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, 25, 100)
	if err != nil {
		httpError(w, err)
		return
	}
	value, err := s.ListControlWorkers(r.Context(), r.URL.Query().Get("epoch"), r.URL.Query().Get("after"), limit)
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, value)
}
