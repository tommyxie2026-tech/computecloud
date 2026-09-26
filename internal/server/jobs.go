package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Job struct {
	ID             string                    `json:"job_id"`
	State          string                    `json:"state"`
	Mode           string                    `json:"mode"`
	Existing       bool                      `json:"existing"`
	LastSeq        int64                     `json:"last_seq,string"`
	Version        int64                     `json:"version,string"`
	Created        int64                     `json:"created_at_ms,string"`
	Updated        int64                     `json:"updated_at_ms,string"`
	Deadline       int64                     `json:"deadline_ms,string"`
	StopReason     string                    `json:"stop_reason,omitempty"`
	ErrorCode      string                    `json:"error_code,omitempty"`
	Counts         map[string]map[string]int `json:"counts"`
	Blockers       map[string]int            `json:"scheduling_blockers"`
	PollAfterMS    int                       `json:"poll_after_ms"`
	Usage          map[string]any            `json:"usage"`
	Links          map[string]string         `json:"links"`
	owner, project string
	parallelism    int
	frozen         job.Frozen
	manifest       []byte
	manifestHash   string
	result         json.RawMessage
}

func readJob(ctx context.Context, q store.Query, id string) (*Job, error) {
	j := &Job{ID: id, PollAfterMS: 2000, Counts: map[string]map[string]int{}, Blockers: map[string]int{}, Usage: map[string]any{"coverage": "unavailable", "input_tokens": nil, "output_tokens": nil}}
	var raw, result []byte
	e := q.QueryRowContext(ctx, `SELECT owner,project,state,mode,version,seq,created,updated,deadline,parallelism,stop_reason,error_code,spec,manifest_json,manifest_hash,result_json FROM jobs WHERE id=?`, id).Scan(&j.owner, &j.project, &j.State, &j.Mode, &j.Version, &j.LastSeq, &j.Created, &j.Updated, &j.Deadline, &j.parallelism, &j.StopReason, &j.ErrorCode, &raw, &j.manifest, &j.manifestHash, &result)
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(raw, &j.frozen); e != nil {
		return nil, e
	}
	j.result = result
	j.Links = map[string]string{"self": "/v1/jobs/" + id, "events": "/v1/jobs/" + id + "/events", "result": "/v1/jobs/" + id + "/result"}
	return j, nil
}
func insertStage(ctx context.Context, q store.Query, jobID, kind string, ordinal int, state string) (string, error) {
	id := jobID + ":" + kind
	now := store.Now()
	_, e := q.ExecContext(ctx, "INSERT INTO stages(id,job_id,kind,ordinal,state,created,updated) VALUES(?,?,?,?,?,?,?)", id, jobID, kind, ordinal, state, now, now)
	return id, e
}
func setStageState(ctx context.Context, q store.Query, stageID, next string) error {
	if stageID == "" {
		return nil
	}
	var jobID, kind, old string
	if e := q.QueryRowContext(ctx, "SELECT job_id,kind,state FROM stages WHERE id=?", stageID).Scan(&jobID, &kind, &old); e != nil {
		return e
	}
	if old == next {
		return nil
	}
	if old == "SUCCEEDED" || old == "FAILED" || old == "CANCELED" {
		return nil
	}
	if _, e := q.ExecContext(ctx, "UPDATE stages SET state=?,updated=? WHERE id=?", next, store.Now(), stageID); e != nil {
		return e
	}
	return appendJobEvent(ctx, q, jobID, "job.stage_state", map[string]string{"stage_id": stageID, "kind": kind, "from": old, "to": next}, "", "")
}
func setTaskStageState(ctx context.Context, q store.Query, taskID, next string) error {
	var stageID sql.NullString
	if e := q.QueryRowContext(ctx, "SELECT stage_id FROM tasks WHERE id=?", taskID).Scan(&stageID); e != nil {
		return e
	}
	if !stageID.Valid || stageID.String == "" {
		return nil
	}
	return setStageState(ctx, q, stageID.String, next)
}

func appendJobEvent(ctx context.Context, q store.Query, id, typ string, body any, op, hash string) error {
	var seq int64
	if e := q.QueryRowContext(ctx, "UPDATE jobs SET seq=seq+1,updated=? WHERE id=? RETURNING seq", store.Now(), id).Scan(&seq); e != nil {
		return e
	}
	var operation, digest any
	if op != "" {
		operation = op
		digest = hash
	}
	_, e := q.ExecContext(ctx, "INSERT INTO job_events(job_id,seq,type,recorded_at,body,operation_key,operation_hash) VALUES(?,?,?,?,?,?,?)", id, seq, typ, store.Now(), job.JSON(body), operation, digest)
	return e
}
func jobState(ctx context.Context, q store.Query, j *Job, next, reason, code string) error {
	if terminal(j.State) {
		return nil
	}
	if j.State == next && j.StopReason == reason && j.ErrorCode == code {
		return nil
	}
	res, e := q.ExecContext(ctx, "UPDATE jobs SET state=?,stop_reason=?,error_code=?,version=version+1,updated=? WHERE id=? AND version=?", next, reason, code, store.Now(), j.ID, j.Version)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("job version conflict")
	}
	old := j.State
	j.State = next
	j.StopReason = reason
	j.ErrorCode = code
	j.Version++
	return appendJobEvent(ctx, q, j.ID, "job.state_changed", map[string]string{"from": old, "to": next, "reason": reason, "code": code}, "", "")
}
func (s *Server) jobAuthorized(ctx context.Context, id, scope string) (*Job, error) {
	p, e := rpcutil.Require(ctx, scope, false)
	if e != nil {
		return nil, e
	}
	j, e := readJob(ctx, s.db.SQL, id)
	if e != nil {
		return nil, dbErr(e)
	}
	if j.owner != p.Identity.Owner || !config.Contains(p.Identity.Projects, j.project) {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	return j, nil
}
func (s *Server) SubmitJob(ctx context.Context, key string, b []byte) (*Job, error) {
	p, e := rpcutil.Require(ctx, "jobs:submit", false)
	if e != nil {
		return nil, e
	}
	if !s.cfg.Jobs.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "JOBS_DISABLED")
	}
	if s.cfg.Maintenance {
		return nil, status.Error(codes.Unavailable, "MAINTENANCE")
	}
	if !job.ValidKey(key) || len(b) > int(s.cfg.Jobs.MaxRequestBytes) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency key or request size")
	}
	spec, e := job.Decode(b, s.cfg.Jobs.MaxPartitions, s.cfg.Jobs.MaxParallelism)
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	if !config.Contains(p.Identity.Projects, spec.ProjectID) {
		return nil, status.Error(codes.PermissionDenied, "project not authorized")
	}
	requestHash := job.Hash(job.JSON(spec))
	id := store.ID()
	existing := false
	e = s.db.Tx(ctx, func(q store.Query) error {
		var previous string
		e := q.QueryRowContext(ctx, "SELECT id,request_hash FROM jobs WHERE owner=? AND project=? AND idem=?", p.Identity.Owner, spec.ProjectID, key).Scan(&id, &previous)
		if e == nil {
			if previous != requestHash {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			existing = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		frozen := job.Frozen{Spec: spec, Digests: map[string]string{}, Routes: map[string]string{}, RouteDigests: map[string]string{}}
		for _, ex := range spec.Executions() {
			if !config.Contains(p.Identity.Credentials, ex.CredentialRef) || s.cfg.Credentials[ex.CredentialRef] < 1 {
				return status.Error(codes.PermissionDenied, "credential not authorized")
			}
			key := job.TemplateKey(ex)
			for _, t := range s.cfg.Jobs.Templates {
				if t.Key() == key {
					frozen.Digests[key] = t.Digest
					break
				}
			}
			if frozen.Digests[key] == "" {
				return status.Error(codes.FailedPrecondition, "TEMPLATE_NOT_CONFIGURED")
			}
			if route := s.cfg.ModelGateway.CredentialRoutes[ex.CredentialRef]; route != "" {
				if !s.cfg.ModelGateway.Enabled || ex.Engine != "codex" || !config.Contains(s.cfg.ModelGateway.Routes[route].AllowedModels, ex.Model) {
					return status.Error(codes.FailedPrecondition, "GATEWAY_ROUTE_UNAVAILABLE")
				}
				frozen.Routes[ex.CredentialRef] = route
				frozen.RouteDigests[route] = config.RouteDigest(s.cfg.ModelGateway.Routes[route])
			}
		}
		now := store.Now()
		deadline := now + spec.Limits.TimeoutSeconds*1000
		parallel := 1
		if spec.Map != nil {
			parallel = spec.Map.Parallelism
		}
		frozenBytes := job.JSON(frozen)
		specHash := job.Hash(job.JSON(struct {
			Frozen   job.Frozen
			Deadline int64
		}{frozen, deadline}))
		if _, e = q.ExecContext(ctx, `INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES(?,?,?,?,?,?,?,?,'QUEUED',?,?,?,?)`, id, p.Identity.Owner, spec.ProjectID, key, requestHash, specHash, frozenBytes, spec.Mode, now, now, deadline, parallel); e != nil {
			return e
		}
		if spec.Mode == "single" {
			if _, e = insertStage(ctx, q, id, "single", 0, "READY"); e != nil {
				return e
			}
			e = s.insertJobTask(ctx, q, id, p.Identity.Owner, deadline, "single", "_single", spec, *spec.Execution, spec.Input.Text, frozen)
		} else {
			if _, e = insertStage(ctx, q, id, "map", 0, "READY"); e != nil {
				return e
			}
			if _, e = insertStage(ctx, q, id, "reduce", 1, "PENDING"); e != nil {
				return e
			}
			for _, part := range spec.Map.Partitions {
				if e = s.insertJobTask(ctx, q, id, p.Identity.Owner, deadline, "map", part.Key, spec, part.Execution, spec.Input.Text+"\n\n"+part.Input.Text, frozen); e != nil {
					break
				}
			}
		}
		if e != nil {
			return e
		}
		return appendJobEvent(ctx, q, id, "job.created", map[string]any{"mode": spec.Mode}, "", "")
	})
	if e != nil {
		return nil, dbErr(e)
	}
	s.wake()
	j, e := readJob(ctx, s.db.SQL, id)
	if e != nil {
		return nil, dbErr(e)
	}
	j.Existing = existing
	return j, nil
}
func (s *Server) insertJobTask(ctx context.Context, q store.Query, jid, owner string, deadline int64, stage, key string, spec job.Spec, ex job.Execution, text string, frozen job.Frozen) error {
	t := ex.Task(spec, "__job/"+jid+"/"+stage+"/"+key, text)
	if stage == "reduce" {
		t.RequiredCapabilities = append(t.RequiredCapabilities, "artifact_inputs_v1")
	}
	if frozen.Routes[ex.CredentialRef] != "" {
		t.RequiredCapabilities = append(t.RequiredCapabilities, "gateway_inference_v1")
	}
	id := store.ID()
	raw := encode(t)
	now := store.Now()
	var stageID string
	if e := q.QueryRowContext(ctx, "SELECT id FROM stages WHERE job_id=? AND kind=?", jid, stage).Scan(&stageID); e != nil {
		return e
	}
	if _, e := q.ExecContext(ctx, `INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,deadline,job_id,stage,stage_id,partition_key) VALUES(?,?,?,?,?,?,'QUEUED',?,?,?,?,?,?,?)`, id, owner, spec.ProjectID, t.IdempotencyKey, store.Hash(raw), raw, now, now, deadline, jid, stage, stageID, key); e != nil {
		return e
	}
	return appendEvent(ctx, q, &pb.Event{TaskId: id, Type: "task.state_changed", PayloadJson: job.JSON(map[string]string{"to": "QUEUED"})}, nil)
}
func (s *Server) GetJob(ctx context.Context, id string) (*Job, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	rows, e := s.db.SQL.QueryContext(ctx, "SELECT stage,state,blocker,count(*) FROM tasks WHERE job_id=? GROUP BY stage,state,blocker", id)
	if e != nil {
		return nil, dbErr(e)
	}
	for rows.Next() {
		var stage, state, blocker string
		var n int
		if e = rows.Scan(&stage, &state, &blocker, &n); e != nil {
			break
		}
		if j.Counts[stage] == nil {
			j.Counts[stage] = map[string]int{}
		}
		j.Counts[stage][state] += n
		if blocker != "" && state == "QUEUED" {
			j.Blockers[blocker] += n
		}
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return nil, dbErr(e)
	}
	if re != nil {
		return nil, dbErr(re)
	}
	j.Usage, e = s.jobUsage(ctx, j)
	if e != nil {
		return nil, dbErr(e)
	}
	return j, nil
}

type CancelJobRequest struct {
	ControlID string `json:"control_id"`
	Reason    string `json:"reason"`
}

func (s *Server) CancelJob(ctx context.Context, id string, in CancelJobRequest) (*Job, error) {
	if !job.ValidKey(in.ControlID) || len(in.Reason) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "invalid cancellation")
	}
	if _, e := s.jobAuthorized(ctx, id, "jobs:cancel"); e != nil {
		return nil, e
	}
	replayed := false
	e := s.db.Tx(ctx, func(q store.Query) error {
		digest := job.Hash(job.JSON(in))
		var old string
		e := q.QueryRowContext(ctx, "SELECT operation_hash FROM job_events WHERE job_id=? AND operation_key=?", id, in.ControlID).Scan(&old)
		if e == nil {
			if old != digest {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			replayed = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		j, e := readJob(ctx, q, id)
		if e != nil {
			return e
		}
		if !terminal(j.State) && j.StopReason == "" {
			if e = jobState(ctx, q, j, "STOPPING", "USER_CANCEL", ""); e != nil {
				return e
			}
		}
		return appendJobEvent(ctx, q, id, "job.cancel_requested", map[string]string{"reason": in.Reason}, in.ControlID, digest)
	})
	if e != nil {
		return nil, dbErr(e)
	}
	s.wake()
	j, e := readJob(ctx, s.db.SQL, id)
	if e == nil {
		j.Existing = replayed
	}
	return j, dbErr(e)
}

type JobEvent struct {
	Seq  int64           `json:"seq,string"`
	Type string          `json:"type"`
	At   int64           `json:"recorded_at_ms,string"`
	Body json.RawMessage `json:"payload"`
}
type JobEvents struct {
	Events  []JobEvent `json:"events"`
	Next    int64      `json:"next_seq,string"`
	HasMore bool       `json:"has_more"`
}

func (s *Server) JobEvents(ctx context.Context, id string, after int64, limit int) (*JobEvents, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if after < 0 || after > j.LastSeq || limit < 1 || limit > 500 {
		return nil, status.Error(codes.InvalidArgument, "invalid event cursor/limit")
	}
	rows, e := s.db.SQL.QueryContext(ctx, "SELECT seq,type,recorded_at,body FROM job_events WHERE job_id=? AND seq>? ORDER BY seq LIMIT ?", id, after, limit)
	if e != nil {
		return nil, dbErr(e)
	}
	defer rows.Close()
	out := &JobEvents{Events: []JobEvent{}, Next: after}
	for rows.Next() {
		var ev JobEvent
		if e = rows.Scan(&ev.Seq, &ev.Type, &ev.At, &ev.Body); e != nil {
			return nil, dbErr(e)
		}
		out.Events = append(out.Events, ev)
		out.Next = ev.Seq
	}
	out.HasMore = out.Next < j.LastSeq
	return out, dbErr(rows.Err())
}
func (s *Server) JobResult(ctx context.Context, id string) (json.RawMessage, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if !terminal(j.State) {
		return nil, status.Error(codes.FailedPrecondition, "JOB_NOT_FINISHED")
	}
	var result map[string]any
	if e = json.Unmarshal(j.result, &result); e != nil {
		return nil, e
	}
	usage, e := s.jobUsage(ctx, j)
	if e != nil {
		return nil, dbErr(e)
	}
	result["usage"] = usage
	return job.JSON(result), nil
}

type jobChild struct {
	id, state, code, stage, key, attempt string
	released                             bool
}

func jobChildren(ctx context.Context, q store.Query, id string) ([]jobChild, error) {
	rows, e := q.QueryContext(ctx, `SELECT t.id,t.state,t.error_code,t.stage,t.partition_key,t.attempt,coalesce(a.released,1) FROM tasks t LEFT JOIN attempts a ON t.attempt=a.id WHERE t.job_id=? ORDER BY t.stage,t.partition_key`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []jobChild
	for rows.Next() {
		var c jobChild
		if e = rows.Scan(&c.id, &c.state, &c.code, &c.stage, &c.key, &c.attempt, &c.released); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Server) advanceJobs(ctx context.Context) error {
	rows, e := s.db.SQL.QueryContext(ctx, "SELECT id FROM jobs WHERE state NOT IN ('SUCCEEDED','FAILED','CANCELED') AND id>? ORDER BY id LIMIT 64", s.jobCursor)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	if len(ids) == 0 {
		s.jobCursor = ""
		return nil
	}
	for _, id := range ids {
		if e = s.advanceJob(ctx, id); e != nil {
			return e
		}
		s.jobCursor = id
	}
	return nil
}
func (s *Server) advanceJob(ctx context.Context, id string) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		j, e := readJob(ctx, q, id)
		if e != nil {
			return e
		}
		if terminal(j.State) {
			return nil
		}
		children, e := jobChildren(ctx, q, id)
		if e != nil {
			return e
		}
		reason := j.StopReason
		code := j.ErrorCode
		if reason == "" && j.Deadline <= store.Now() {
			reason = "DEADLINE_EXCEEDED"
			code = reason
		}
		unknown := false
		for _, c := range children {
			if c.state == "RECONCILING" {
				unknown = true
				if reason == "" {
					reason = "EXECUTION_UNCERTAIN"
					code = c.code
				}
			}
			if (c.state == "FAILED" || c.state == "CANCELED") && reason == "" {
				reason = "CHILD_FAILED"
				code = c.code
			}
		}
		if reason == "" && j.Deadline <= store.Now() {
			reason = "DEADLINE_EXCEEDED"
			code = reason
		}
		if reason != "" {
			next := "STOPPING"
			if unknown {
				next = "RECONCILING"
			}
			if e = jobState(ctx, q, j, next, reason, code); e != nil {
				return e
			}
			allStopped := true
			for i := range children {
				c := &children[i]
				if !terminal(c.state) {
					t, _, e := readTask(ctx, q, c.id)
					if e != nil {
						return e
					}
					if c.attempt == "" {
						if e = setState(ctx, q, c.id, "CANCELED", "", ""); e != nil {
							return e
						}
						c.state = "CANCELED"
					} else {
						if c.state != "RECONCILING" {
							if e = setState(ctx, q, c.id, "CANCELING", reason, "job stopping"); e != nil {
								return e
							}
						}
						if e = s.stopCommand(ctx, q, t); e != nil {
							return e
						}
					}
				}
				allStopped = allStopped && terminal(c.state) && c.released
			}
			if allStopped {
				state := "FAILED"
				if reason == "USER_CANCEL" {
					state = "CANCELED"
				}
				return s.finishJob(ctx, q, j, children, state)
			}
			return nil
		}
		if j.Mode == "single" {
			if len(children) != 1 {
				return fmt.Errorf("invalid single job child count")
			}
			if children[0].state == "SUCCEEDED" && children[0].released {
				return s.finishJob(ctx, q, j, children, "SUCCEEDED")
			}
			return nil
		}
		var mapStage, reduceStage string
		if e = q.QueryRowContext(ctx, "SELECT state FROM stages WHERE job_id=? AND kind='map'", j.ID).Scan(&mapStage); e != nil {
			return e
		}
		if e = q.QueryRowContext(ctx, "SELECT state FROM stages WHERE job_id=? AND kind='reduce'", j.ID).Scan(&reduceStage); e != nil {
			return e
		}
		if mapStage != "SUCCEEDED" {
			mapCount := 0
			for _, c := range children {
				if c.stage != "map" {
					continue
				}
				mapCount++
				if c.state != "SUCCEEDED" || !c.released {
					return nil
				}
			}
			if mapCount != len(j.frozen.Spec.Map.Partitions) {
				return fmt.Errorf("map partition set mismatch")
			}
			return s.createReduce(ctx, q, j, children)
		}
		if reduceStage == "READY" || reduceStage == "RUNNING" || reduceStage == "SUCCEEDED" {
			for _, c := range children {
				if c.stage == "reduce" && c.state == "SUCCEEDED" && c.released {
					return s.finishJob(ctx, q, j, children, "SUCCEEDED")
				}
			}
		}
		return nil
	})
}
func (s *Server) createReduce(ctx context.Context, q store.Query, j *Job, children []jobChild) error {
	manifest := job.Manifest{Version: "inputs.v1", BaseCommit: j.frozen.Spec.Workspace.BaseCommit, Items: []job.ManifestItem{}}
	var total int64
	for _, c := range children {
		var part *job.Partition
		for i := range j.frozen.Spec.Map.Partitions {
			p := &j.frozen.Spec.Map.Partitions[i]
			if p.Key == c.key {
				part = p
				break
			}
		}
		if part == nil {
			return fmt.Errorf("unexpected map partition")
		}
		item := job.ManifestItem{PartitionKey: c.key, TaskID: c.id, AttemptID: c.attempt, ScopePaths: part.ScopePaths, TemplateDigest: j.frozen.Digests[job.TemplateKey(part.Execution)]}
		selected, e := selectedResultArtifact(ctx, q, c)
		if e != nil {
			return e
		}
		if selected == nil {
			return jobState(ctx, q, j, "STOPPING", "INVALID_INPUT_ARTIFACT", "INVALID_INPUT_ARTIFACT")
		}
		item.ArtifactID = selected.ArtifactId
		item.SHA256 = selected.Sha256
		item.Size = selected.Size
		if e = q.QueryRowContext(ctx, "SELECT generation FROM attempts WHERE id=? AND released=1", c.attempt).Scan(&item.Generation); e != nil {
			return e
		}
		total += item.Size
		manifest.Items = append(manifest.Items, item)
	}
	raw := job.JSON(manifest)
	if total > s.cfg.Jobs.MaxReduceInputBytes || len(raw) > int(s.cfg.Jobs.MaxManifestBytes) {
		return jobState(ctx, q, j, "STOPPING", "INPUT_LIMIT", "INPUT_LIMIT")
	}
	digest := job.Hash(raw)
	res, e := q.ExecContext(ctx, "UPDATE jobs SET manifest_json=?,manifest_hash=? WHERE id=? AND manifest_hash=''", raw, digest, j.ID)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("reduce barrier already committed")
	}
	if e := setStageState(ctx, q, j.ID+":map", "SUCCEEDED"); e != nil {
		return e
	}
	if e := setStageState(ctx, q, j.ID+":reduce", "READY"); e != nil {
		return e
	}
	if e := s.insertJobTask(ctx, q, j.ID, j.owner, j.Deadline, "reduce", "_reduce", j.frozen.Spec, j.frozen.Spec.Reduce.Execution, j.frozen.Spec.Input.Text+"\n\n"+j.frozen.Spec.Reduce.Input.Text, j.frozen); e != nil {
		return e
	}
	if e := jobState(ctx, q, j, "REDUCING", "", ""); e != nil {
		return e
	}
	return appendJobEvent(ctx, q, j.ID, "job.reduce_created", map[string]any{"manifest_sha256": digest, "partitions": len(manifest.Items)}, "", "")
}
func (s *Server) finishJob(ctx context.Context, q store.Query, j *Job, children []jobChild, state string) error {
	finals := []*pb.Artifact{}
	failures := []map[string]string{}
	summary := ""
	for _, c := range children {
		if !c.released {
			return fmt.Errorf("cannot publish job before cleanup")
		}
		if c.state == "FAILED" {
			failures = append(failures, map[string]string{"task_id": c.id, "stage": c.stage, "partition_key": c.key, "error_code": c.code})
		}
		if state == "SUCCEEDED" && (c.stage == "single" || c.stage == "reduce") {
			selected, e := selectedResultArtifact(ctx, q, c)
			if e != nil {
				return e
			}
			if selected == nil {
				return fmt.Errorf("missing completed result artifact")
			}
			finals = append(finals, selected)
			if e = q.QueryRowContext(ctx, "SELECT result FROM tasks WHERE id=?", c.id).Scan(&summary); e != nil {
				return e
			}
			if len(summary) > 4096 {
				summary = string([]rune(summary)[:min(1024, len([]rune(summary)))])
			}
		}
	}
	result := job.JSON(map[string]any{"job_id": j.ID, "state": state, "error_code": j.ErrorCode, "stop_reason": j.StopReason, "base_commit": j.frozen.Spec.Workspace.BaseCommit, "manifest_sha256": j.manifestHash, "summary": summary, "final_artifacts": finals, "child_failures": failures})
	if _, e := q.ExecContext(ctx, "UPDATE jobs SET result_json=? WHERE id=?", result, j.ID); e != nil {
		return e
	}
	stageState := state
	if state == "CANCELED" {
		stageState = "CANCELED"
	}
	rows, e := q.QueryContext(ctx, "SELECT id,state FROM stages WHERE job_id=? ORDER BY ordinal", j.ID)
	if e != nil {
		return e
	}
	var stageIDs []string
	var stageStates []string
	for rows.Next() {
		var sid, ss string
		if e = rows.Scan(&sid, &ss); e != nil {
			break
		}
		stageIDs = append(stageIDs, sid)
		stageStates = append(stageStates, ss)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for i, sid := range stageIDs {
		if stageStates[i] == "SUCCEEDED" {
			continue
		}
		next := stageState
		if state == "SUCCEEDED" {
			next = "SUCCEEDED"
		}
		if e = setStageState(ctx, q, sid, next); e != nil {
			return e
		}
	}
	return jobState(ctx, q, j, state, j.StopReason, j.ErrorCode)
}
func (s *Server) assignmentJob(ctx context.Context, q store.Query, id string) (*pb.JobExecution, *Job, error) {
	var jid sql.NullString
	var stage, key string
	if e := q.QueryRowContext(ctx, "SELECT job_id,stage,partition_key FROM tasks WHERE id=?", id).Scan(&jid, &stage, &key); e != nil {
		return nil, nil, e
	}
	if !jid.Valid {
		return nil, nil, nil
	}
	j, e := readJob(ctx, q, jid.String)
	if e != nil {
		return nil, nil, e
	}
	jc := &pb.JobExecution{JobId: j.ID, Stage: stage, PartitionKey: key, InputLimitBytes: s.cfg.Jobs.MaxReduceInputBytes}
	var ex job.Execution
	if stage == "single" {
		ex = *j.frozen.Spec.Execution
	} else {
		jc.Strategy = j.frozen.Spec.Reduce.Strategy
		if stage == "reduce" {
			ex = j.frozen.Spec.Reduce.Execution
			jc.InputManifestJson = j.manifest
			jc.InputManifestSha256 = j.manifestHash
		} else {
			found := false
			for _, p := range j.frozen.Spec.Map.Partitions {
				if p.Key == key {
					found = true
					ex = p.Execution
					jc.ScopePaths = p.ScopePaths
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("unknown partition")
			}
		}
	}
	jc.TemplateDigest = j.frozen.Digests[job.TemplateKey(ex)]
	return jc, j, nil
}
func (s *Server) jobAllowsExecution(ctx context.Context, q store.Query, task string) (bool, error) {
	var n int
	e := q.QueryRowContext(ctx, `SELECT count(*) FROM tasks t LEFT JOIN jobs j ON t.job_id=j.id WHERE t.id=? AND (t.job_id IS NULL OR (j.stop_reason='' AND j.state IN ('QUEUED','EXECUTING','MAPPING','REDUCING') AND j.deadline>?))`, task, store.Now()).Scan(&n)
	return n == 1, e
}
func (s *Server) jobUsage(ctx context.Context, j *Job) (map[string]any, error) {
	out := map[string]any{"coverage": "unavailable", "input_tokens": nil, "output_tokens": nil}
	var n, unknown int
	var in, output sql.NullInt64
	e := s.db.SQL.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(CASE WHEN usage_complete=0 THEN 1 ELSE 0 END),0),sum(input_tokens),sum(output_tokens) FROM gateway_requests WHERE job_id=?", j.ID).Scan(&n, &unknown, &in, &output)
	if e != nil {
		return nil, e
	}
	if n == 0 {
		return out, nil
	}
	out["coverage"] = "partial"
	out["unknown_requests"] = unknown
	out["requests"] = n
	if in.Valid {
		out["input_tokens"] = in.Int64
	}
	if output.Valid {
		out["output_tokens"] = output.Int64
	}
	all := true
	for _, ex := range j.frozen.Spec.Executions() {
		all = all && j.frozen.Routes[ex.CredentialRef] != ""
	}
	if all && unknown == 0 && terminal(j.State) {
		out["coverage"] = "complete"
	}
	return out, nil
}
func taskJobReadScope(ctx context.Context, q store.Query, id string) error {
	p, e := rpcutil.User(ctx)
	if e != nil {
		return e
	}
	if len(p.Identity.Scopes) == 0 {
		return nil
	}
	var managed int
	if e = q.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE id=? AND job_id IS NOT NULL", id).Scan(&managed); e != nil {
		return e
	}
	scope := "tasks:read"
	if managed != 0 {
		scope = "jobs:read"
	}
	_, e = rpcutil.Require(ctx, scope, false)
	return e
}
func cleanCode(e error) string {
	if e == nil {
		return ""
	}
	s := status.Convert(e).Message()
	if strings.Contains(s, "\n") {
		return "REQUEST_FAILED"
	}
	return s
}

func selectedResultArtifact(ctx context.Context, q store.Query, c jobChild) (*pb.Artifact, error) {
	var raw []byte
	if e := q.QueryRowContext(ctx, "SELECT body FROM events WHERE task=? AND attempt=? AND worker_seq IS NULL AND json_extract(body,'$.type')='attempt.completed' ORDER BY seq DESC LIMIT 1", c.id, c.attempt).Scan(&raw); e != nil {
		return nil, e
	}
	ev := new(pb.Event)
	if e := decode(raw, ev); e != nil {
		return nil, e
	}
	var completion struct {
		IDs []string `json:"artifact_ids"`
	}
	if e := json.Unmarshal(ev.PayloadJson, &completion); e != nil {
		return nil, e
	}
	if len(completion.IDs) != 1 {
		return nil, nil
	}
	a := new(pb.Artifact)
	e := q.QueryRowContext(ctx, `SELECT ar.id,ar.task,ar.attempt,ar.kind,ar.hash,ar.size
		FROM artifacts ar
		JOIN tasks t ON t.id=ar.task
		JOIN attempts x ON x.id=ar.attempt
		WHERE ar.id=? AND ar.task=? AND ar.attempt=? AND ar.kind='result-bundle'
		  AND ar.state='ACCEPTED' AND ar.generation=x.generation
		  AND t.attempt=x.id AND t.current_generation=x.generation`, completion.IDs[0], c.id, c.attempt).Scan(&a.ArtifactId, &a.TaskId, &a.AttemptId, &a.Kind, &a.Sha256, &a.Size)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return a, e
}
