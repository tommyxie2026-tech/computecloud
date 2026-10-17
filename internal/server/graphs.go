package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type frozenGraph struct {
	GoalID     string                                `json:"goal_id"`
	Generation int64                                 `json:"generation"`
	Plan       graph.Plan                            `json:"plan"`
	Digests    map[string]string                     `json:"template_digests"`
	Commands   map[string]config.GraphCommandProfile `json:"command_bindings,omitempty"`
}
type GraphNodeStatus struct {
	ID     string  `json:"node_id"`
	State  string  `json:"state"`
	TaskID *string `json:"task_id"`
	Error  string  `json:"error_code,omitempty"`
}
type GraphAdmissionStatus struct {
	NodeID      string `json:"node_id"`
	ArtifactID  string `json:"artifact_id"`
	ReceiptID   string `json:"receipt_id"`
	State       string `json:"state"`
	Error       string `json:"error_code,omitempty"`
	RetryCount  int    `json:"retry_count"`
	NextRetryAt int64  `json:"next_retry_at"`
}
type GraphStatus struct {
	ValidityRevision  int64                  `json:"validity_revision"`
	Admissions        []GraphAdmissionStatus `json:"admissions"`
	GoalID            string                 `json:"goal_id"`
	Generation        int64                  `json:"graph_generation"`
	JobID             string                 `json:"job_id"`
	State             string                 `json:"state"`
	GoalState         string                 `json:"goal_state"`
	Plan              graph.Plan             `json:"plan"`
	Nodes             []GraphNodeStatus      `json:"nodes"`
	EvaluationVersion int64                  `json:"evaluation_version"`
	EvaluationID      string                 `json:"evaluation_id"`
	OutputHash        string                 `json:"output_sha256"`
	Evaluation        json.RawMessage        `json:"evaluation,omitempty"`
}

// graphLegacyMetadata supplies shared quota/workspace metadata; graph dispatch
// always resolves the real node execution from the frozen native plan.
func graphLegacyMetadata(f *frozenGraph) job.Frozen {
	s := f.Plan.Spec
	l := s.Limits
	meta := job.Spec{ProjectID: s.ProjectID, Workspace: s.Workspace, Limits: job.Limits{TimeoutSeconds: l.TimeoutSeconds, MaxAttemptsPerTask: l.MaxAttemptsPerTask}}
	for _, op := range s.Operators {
		if op.Execution != nil {
			meta.Execution = op.Execution
			break
		}
	}
	return job.Frozen{Spec: meta, Digests: f.Digests}
}

func (s *Server) SubmitGraph(ctx context.Context, key string, raw []byte) (*Job, error) {
	p, e := rpcutil.Require(ctx, "jobs:submit", false)
	if e != nil {
		return nil, e
	}
	if !s.cfg.Jobs.Enabled || !s.cfg.Jobs.GraphsEnabled {
		return nil, status.Error(codes.FailedPrecondition, "GRAPHS_DISABLED")
	}
	if s.cfg.Maintenance {
		return nil, status.Error(codes.Unavailable, "MAINTENANCE")
	}
	if !job.ValidKey(key) || len(raw) > int(s.cfg.Jobs.MaxRequestBytes) {
		return nil, status.Error(codes.InvalidArgument, "invalid key or request size")
	}
	f, e := s.freezeGraph(raw, p)
	if e != nil {
		return nil, e
	}
	spec := f.Plan.Spec
	plan := f.Plan
	id := store.ID()
	existing := false
	traceID := "trc_" + store.ID()
	e = s.db.Tx(ctx, func(q store.Query) error {
		var previous string
		err := q.QueryRowContext(ctx, `SELECT id,request_hash FROM jobs WHERE owner=? AND project=? AND idem=?`, p.Identity.Owner, spec.ProjectID, key).Scan(&id, &previous)
		if err == nil {
			if previous != plan.Digest {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			existing = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		limits := p.Identity.Limits
		if limits == (config.IdentityLimits{}) {
			limits = config.IdentityLimits{MaxActiveJobs: 8, MaxQueuedJobs: 32, MaxModelRequests: 4}
		}
		var active, queued int
		if err = q.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(state='QUEUED'),0) FROM jobs WHERE owner=? AND project=? AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')`, p.Identity.Owner, spec.ProjectID).Scan(&active, &queued); err != nil {
			return err
		}
		if active >= limits.MaxActiveJobs || queued >= limits.MaxQueuedJobs {
			return status.Error(codes.ResourceExhausted, "IDENTITY_JOB_LIMIT")
		}
		if err = s.checkQueueAdmission(ctx, q, spec.ProjectID, len(spec.Nodes)); err != nil {
			return err
		}
		if err = createTrace(ctx, q, traceID, p.Identity.Owner, spec.ProjectID, "job"); err != nil {
			return err
		}
		now := store.Now()
		deadline := now + spec.Limits.TimeoutSeconds*1000
		frozen := job.JSON(f)
		if _, err = q.ExecContext(ctx, `INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism,trace_id) VALUES(?,?,?,?,?,?,?,'iu_graph','QUEUED',?,?,?,?,?)`, id, p.Identity.Owner, spec.ProjectID, key, plan.Digest, job.Hash(frozen), frozen, now, now, deadline, plan.EffectiveParallelism, traceID); err != nil {
			return err
		}
		attempts := 0
		ops := map[string]graph.Operator{}
		for _, op := range spec.Operators {
			ops[op.ID] = op
		}
		for _, n := range spec.Nodes {
			if ops[n.Operator].Kind != "join" {
				attempts += spec.Limits.MaxAttemptsPerTask
			}
		}
		if attempts < 1 {
			attempts = 1
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO goals(id,owner,project,state,max_replans,max_total_attempts,max_wall_time_ms,created,updated,deadline) VALUES(?,?,?,'GRAPH_READY',?,?,?,?,?,?)`, f.GoalID, p.Identity.Owner, spec.ProjectID, spec.Limits.MaxReplans, attempts*(spec.Limits.MaxReplans+1), spec.Limits.TimeoutSeconds*1000, now, now, deadline); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO goal_job_bindings VALUES(?,?,1,1)`, id, f.GoalID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO goal_plans VALUES(?,1,1,?,?,?,?)`, f.GoalID, id, frozen, job.JSON(plan.Spec), now); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO iu_graphs(job_id,goal_id,generation,plan) VALUES(?,?,1,?)`, id, f.GoalID, frozen); err != nil {
			return err
		}
		for _, n := range spec.Nodes {
			if _, err = q.ExecContext(ctx, `INSERT INTO iu_nodes(goal_id,generation,node_id) VALUES(?,1,?)`, f.GoalID, n.ID); err != nil {
				return err
			}
		}
		if spec.Limits.MaxCostUnits != nil {
			if _, err = q.ExecContext(ctx, `INSERT INTO goal_budget_policy(goal_id,max_cost_units) VALUES(?,?)`, f.GoalID, *spec.Limits.MaxCostUnits); err != nil {
				return err
			}
		}
		return appendJobEvent(ctx, q, id, "graph.created", map[string]any{"goal_id": f.GoalID, "sha256": plan.Digest, "requested_parallelism": plan.RequestedParallelism, "effective_parallelism": plan.EffectiveParallelism, "budget_execution_mode": plan.BudgetMode}, "", "")
	})
	if e != nil {
		return nil, dbErr(e)
	}
	s.wake()
	j, e := readJob(ctx, s.db.SQL, id)
	if j != nil {
		j.Existing = existing
	}
	return j, e
}

func validateGraphCommandParameters(profile config.GraphCommandProfile, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	schema := profile.ParametersSchema
	if schema == "" {
		schema = `{"type":"object","additionalProperties":false}`
	}
	var definition jsonschema.Schema
	var value any
	if e := json.Unmarshal([]byte(schema), &definition); e != nil {
		return e
	}
	resolved, e := definition.Resolve(nil)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(raw, &value); e != nil {
		return e
	}
	return resolved.Validate(value)
}

func graphExecutionFor(f *frozenGraph, op graph.Operator) *job.Execution {
	if op.Kind == "command" {
		profile, ok := f.Commands[op.ID]
		if !ok {
			return nil
		}
		ex := profile.Execution
		return &ex
	}
	return op.Execution
}

func (s *Server) GraphStatus(ctx context.Context, id string) (*GraphStatus, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, status.Error(codes.NotFound, "NOT_GRAPH_JOB")
	}
	f := j.graph
	out := &GraphStatus{GoalID: f.GoalID, Generation: f.Generation, JobID: id, State: j.State, Plan: f.Plan, Nodes: []GraphNodeStatus{}, Admissions: []GraphAdmissionStatus{}}
	if e = s.db.SQL.QueryRowContext(ctx, `SELECT g.state,x.evaluation_version,x.evaluation_id,x.output_hash FROM goals g JOIN iu_graphs x ON x.goal_id=g.id WHERE x.job_id=?`, id).Scan(&out.GoalState, &out.EvaluationVersion, &out.EvaluationID, &out.OutputHash); e != nil {
		return nil, e
	}
	out.ValidityRevision, e = graphValidityRevisionTx(ctx, s.db.SQL, f)
	if e != nil {
		return nil, e
	}
	if out.EvaluationID != "" {
		var body []byte
		if e = s.db.SQL.QueryRowContext(ctx, `SELECT body FROM iu_evaluations WHERE id=? AND goal_id=? AND generation=?`, out.EvaluationID, f.GoalID, f.Generation).Scan(&body); e != nil {
			return nil, e
		}
		out.Evaluation = body
	}
	rows, e := s.db.SQL.QueryContext(ctx, `SELECT node_id,state,task_id,error_code FROM iu_nodes WHERE goal_id=? AND generation=? ORDER BY node_id`, f.GoalID, f.Generation)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var n GraphNodeStatus
		if e = rows.Scan(&n.ID, &n.State, &n.TaskID, &n.Error); e != nil {
			return nil, e
		}
		out.Nodes = append(out.Nodes, n)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	admissions, e := s.db.SQL.QueryContext(ctx, `SELECT node_id,artifact_id,receipt_id,state,error_code,retry_count,next_retry_at FROM iu_admissions WHERE goal_id=? AND generation=? ORDER BY node_id,artifact_id,output_port`, f.GoalID, f.Generation)
	if e != nil {
		return nil, e
	}
	defer admissions.Close()
	for admissions.Next() {
		var a GraphAdmissionStatus
		if e = admissions.Scan(&a.NodeID, &a.ArtifactID, &a.ReceiptID, &a.State, &a.Error, &a.RetryCount, &a.NextRetryAt); e != nil {
			return nil, e
		}
		out.Admissions = append(out.Admissions, a)
	}
	return out, admissions.Err()
}

func (s *Server) httpSubmitGraph(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, s.cfg.Jobs.MaxRequestBytes)
	if e != nil {
		httpError(w, e)
		return
	}
	j, e := s.SubmitGraph(r.Context(), r.Header.Get("Idempotency-Key"), raw)
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 201, j)
}
func (s *Server) httpGraphStatus(w http.ResponseWriter, r *http.Request) {
	v, e := s.GraphStatus(r.Context(), r.PathValue("id"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, v)
}

func (s *Server) freezeGraph(raw []byte, p rpcutil.Principal) (frozenGraph, error) {
	spec, e := graph.Decode(raw)
	if e != nil {
		return frozenGraph{}, status.Error(codes.InvalidArgument, e.Error())
	}
	plan, e := graph.Compile(spec)
	if e != nil {
		return frozenGraph{}, e
	}
	if !config.Contains(p.Identity.Projects, spec.ProjectID) {
		return frozenGraph{}, status.Error(codes.PermissionDenied, "project not authorized")
	}
	if plan.RequestedParallelism > s.cfg.Jobs.MaxParallelism {
		return frozenGraph{}, status.Error(codes.InvalidArgument, "graph parallelism exceeds configured limit")
	}
	f := frozenGraph{GoalID: "goal_" + store.ID(), Generation: 1, Plan: plan, Digests: map[string]string{}, Commands: map[string]config.GraphCommandProfile{}}
	for _, op := range spec.Operators {
		ex := op.Execution
		if op.Kind == "command" {
			profile, ok := s.cfg.Jobs.GraphCommands[op.CommandProfile]
			if !ok || profile.Execution.RuntimeProfile != "command_exec" || profile.Execution.Model != op.CommandProfile || profile.Execution.Validate() != nil {
				return frozenGraph{}, status.Error(codes.FailedPrecondition, "GRAPH_COMMAND_PROFILE_UNAVAILABLE")
			}
			f.Commands[op.ID] = profile
			ex = &profile.Execution
			for _, n := range spec.Nodes {
				if n.Operator == op.ID {
					if e = validateGraphCommandParameters(profile, n.Parameters); e != nil {
						return frozenGraph{}, status.Error(codes.InvalidArgument, e.Error())
					}
				}
			}
		}
		if ex == nil {
			continue
		}
		if !config.Contains(p.Identity.Credentials, ex.CredentialRef) || s.cfg.Credentials[ex.CredentialRef] < 1 {
			return frozenGraph{}, status.Error(codes.PermissionDenied, "credential not authorized")
		}
		// Graph execution requires a directly configured Worker runtime. Gateway
		// routing remains the legacy contract until a frozen graph route is supported.
		if s.cfg.ModelGateway.CredentialRoutes[ex.CredentialRef] != "" {
			return frozenGraph{}, status.Error(codes.FailedPrecondition, "GRAPH_GATEWAY_ROUTE_UNSUPPORTED")
		}
		key := job.TemplateKey(*ex)
		for _, tpl := range s.cfg.Jobs.Templates {
			if tpl.Key() == key {
				f.Digests[key] = tpl.Digest
				break
			}
		}
		if f.Digests[key] == "" {
			return frozenGraph{}, status.Error(codes.FailedPrecondition, "TEMPLATE_NOT_CONFIGURED")
		}
	}
	return f, nil
}
