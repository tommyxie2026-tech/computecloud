package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type graphNodeRow struct {
	ID, State, Error                 string
	Task                             sql.NullString
	Context, InputView, Output, View []byte
}

func graphOperator(f *frozenGraph, n graph.Node) graph.Operator {
	for _, op := range f.Plan.Spec.Operators {
		if op.ID == n.Operator {
			return op
		}
	}
	return graph.Operator{}
}
func graphNode(f *frozenGraph, id string) (graph.Node, bool) {
	for _, n := range f.Plan.Spec.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return graph.Node{}, false
}
func readGraphNodes(ctx context.Context, q store.Query, f *frozenGraph) (map[string]*graphNodeRow, error) {
	rows, e := q.QueryContext(ctx, `SELECT node_id,state,task_id,context_json,input_view,output_json,output_view,error_code FROM iu_nodes WHERE goal_id=? AND generation=?`, f.GoalID, f.Generation)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]*graphNodeRow{}
	for rows.Next() {
		r := &graphNodeRow{}
		if e = rows.Scan(&r.ID, &r.State, &r.Task, &r.Context, &r.InputView, &r.Output, &r.View, &r.Error); e != nil {
			return nil, e
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

func (s *Server) advanceGraph(ctx context.Context, id string) error {
	return s.db.Tx(ctx, func(q store.Query) error {
		j, e := readJob(ctx, q, id)
		if e != nil {
			return e
		}
		if j.graph == nil {
			return fmt.Errorf("NOT_GRAPH_JOB")
		}
		return s.advanceGraphTx(ctx, q, j)
	})
}

func (s *Server) advanceGraphTx(ctx context.Context, q store.Query, j *Job) error {
	if terminal(j.State) {
		return nil
	}
	f := j.graph
	invalidated, validityErr := graphInvalidatedTx(ctx, q, f)
	if validityErr != nil {
		return validityErr
	}
	if invalidated {
		j.StopReason = "IU_INPUT_INVALIDATED"
		j.ErrorCode = j.StopReason
	}
	var generation int64
	var goalState string
	if e := q.QueryRowContext(ctx, `SELECT active_graph_generation,state FROM goals WHERE id=?`, f.GoalID).Scan(&generation, &goalState); e != nil {
		return e
	}
	if generation != f.Generation || goalState == "CANCELED" {
		j.StopReason = "STALE_GENERATION"
		j.ErrorCode = j.StopReason
	}
	if j.Deadline <= store.Now() {
		j.StopReason = "DEADLINE_EXCEEDED"
		j.ErrorCode = j.StopReason
	}
	nodes, e := readGraphNodes(ctx, q, f)
	if e != nil {
		return e
	}
	for _, r := range nodes {
		if !r.Task.Valid {
			continue
		}
		var state, code string
		var released int
		if e = q.QueryRowContext(ctx, `SELECT t.state,t.error_code,coalesce(a.released,1) FROM tasks t LEFT JOIN attempts a ON a.id=t.attempt WHERE t.id=?`, r.Task.String).Scan(&state, &code, &released); e != nil {
			return e
		}
		if (state == "FAILED" || state == "CANCELED" || state == "RECONCILING") && j.StopReason == "" {
			j.StopReason = "CHILD_FAILED"
			j.ErrorCode = code
		}
		if state == "FAILED" || state == "CANCELED" {
			r.State = state
			r.Error = code
		}
		if state == "STARTING" || state == "RUNNING" {
			r.State = "RUNNING"
		}
		// Success is projected only after the result artifact and memory receipt exist.
		if state == "SUCCEEDED" && released == 1 && len(r.Output) > 0 && len(r.View) > 0 {
			r.State = "SUCCEEDED"
		}
		if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state=? WHERE goal_id=? AND generation=? AND node_id=?`, r.State, f.GoalID, f.Generation, r.ID); e != nil {
			return e
		}
	}
	if j.StopReason != "" {
		return s.stopGraphTx(ctx, q, j, nodes)
	}
	for _, id := range f.Plan.Order {
		r := nodes[id]
		if r.State != "BLOCKED" {
			continue
		}
		n, _ := graphNode(f, id)
		op := graphOperator(f, n)
		ready, skip := true, false
		selected := 0
		var views []graph.MemoryView
		var inputs []graph.InputValue
		joined := map[string]json.RawMessage{}
		for _, edge := range f.Plan.Spec.Edges {
			if edge.ToNode != id {
				continue
			}
			parent := nodes[edge.FromNode]
			if parent.State == "SKIPPED" {
				if op.Kind != "join" || n.JoinPolicy != "selected_success" {
					skip = true
				}
				continue
			}
			if parent.State != "SUCCEEDED" {
				ready = false
				continue
			}
			values := map[string]json.RawMessage{}
			if e = json.Unmarshal(parent.Output, &values); e != nil {
				return e
			}
			if edge.When != "" {
				var verdict string
				if e = json.Unmarshal(values[edge.FromPort], &verdict); e != nil || verdict != "PASS" && verdict != "FAIL" && verdict != "UNKNOWN" {
					j.StopReason = "GRAPH_CONDITION_INVALID"
					j.ErrorCode = j.StopReason
					return s.stopGraphTx(ctx, q, j, nodes)
				}
				if verdict != edge.When {
					if op.Kind != "join" || n.JoinPolicy != "selected_success" {
						skip = true
					}
					continue
				}
			}
			selected++
			if edge.Type == "memory" {
				var v graph.MemoryView
				if e = json.Unmarshal(parent.View, &v); e != nil {
					return e
				}
				views = append(views, v)
			}
			if edge.Type != "control" {
				raw := values[edge.FromPort]
				if len(raw) == 0 {
					return fmt.Errorf("GRAPH_OUTPUT_MISSING")
				}
				inputs = append(inputs, graph.InputValue{Node: edge.FromNode, Port: edge.ToPort, SourcePort: edge.FromPort, SHA256: graph.Hash(raw), Value: string(raw)})
				joined[edge.ToPort] = raw
			}
		}
		if !ready && !skip {
			continue
		}
		if skip || (op.Kind == "join" && n.JoinPolicy == "selected_success" && selected == 0) {
			r.State = "SKIPPED"
			if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state='SKIPPED' WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, id); e != nil {
				return e
			}
			continue
		}
		view, e := graph.MergeViews(views)
		if e != nil {
			return e
		}
		if op.Kind == "join" {
			values := map[string]json.RawMessage{}
			for _, port := range op.Outputs {
				values[port.Name] = job.JSON(joined)
			}
			if e = graph.ValidateValues(values, op.Outputs, f.Plan.Spec.Schemas); e != nil {
				j.StopReason = "GRAPH_JOIN_SCHEMA_INVALID"
				j.ErrorCode = j.StopReason
				return s.stopGraphTx(ctx, q, j, nodes)
			}
			r.State = "SUCCEEDED"
			r.Output = job.JSON(values)
			r.View = job.JSON(view)
			if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state='SUCCEEDED',output_json=?,output_view=? WHERE goal_id=? AND generation=? AND node_id=?`, r.Output, r.View, f.GoalID, f.Generation, id); e != nil {
				return e
			}
			continue
		}
		ex := graphExecutionFor(f, op)
		if ex == nil {
			return fmt.Errorf("GRAPH_COMMAND_PROFILE_UNAVAILABLE")
		}
		instruction := n.Instructions + "\nReturn only JSON with version=iu-node-output-v1, values matching the declared ports, and records=[] or unchecked memory records. Declared output ports: " + string(job.JSON(op.Outputs))
		manifest, e := graph.CompileContext(graph.ContextRequest{Sources: f.Plan.Spec.Sources, Rules: f.Plan.Spec.Rules, Spans: n.Spans, Instructions: instruction, View: view, Inputs: inputs, Model: ex.Model, Parameters: n.Parameters, OutputSchema: graph.OutputSchema(op.Outputs, f.Plan.Spec.Schemas)})
		if e != nil {
			j.StopReason = "CONTEXT_INSUFFICIENT"
			j.ErrorCode = j.StopReason
			return s.stopGraphTx(ctx, q, j, nodes)
		}
		if e = s.checkQueueAdmission(ctx, q, j.project, 1); e != nil {
			// Backpressure delays this activation; committing earlier activations
			// lets the scheduler drain the queue during the same tick.
			if status.Code(e) == codes.ResourceExhausted {
				continue
			}
			return e
		}
		ordinal := 0
		for i, x := range f.Plan.Order {
			if x == id {
				ordinal = i
			}
		}
		stageID, e := insertStage(ctx, q, j.ID, "node:"+id, ordinal, "READY")
		if e != nil {
			return e
		}
		task := ex.Task(j.frozen.Spec, j.ID+"/node/"+id, manifest.Text)
		task.RequiredCapabilities = append(task.RequiredCapabilities, "iu_graph_v1")
		tid := store.ID()
		now := store.Now()
		body := encode(task)
		if _, e = q.ExecContext(ctx, `INSERT INTO tasks(id,owner,project,idem,hash,spec,state,created,updated,priority,deadline,job_id,stage,stage_id,partition_key) VALUES(?,?,?,?,?,?,'QUEUED',?,?,?,?,?,'single',?,?)`, tid, j.owner, j.project, task.IdempotencyKey, store.Hash(body), body, now, now, task.Priority, j.Deadline, j.ID, stageID, id); e != nil {
			return e
		}
		if e = appendEvent(ctx, q, &pb.Event{TaskId: tid, Type: "task.state_changed", PayloadJson: job.JSON(map[string]string{"to": "QUEUED"})}, nil); e != nil {
			return e
		}
		r.State = "READY"
		r.Task = sql.NullString{String: tid, Valid: true}
		r.Context = job.JSON(manifest)
		r.InputView = job.JSON(view)
		if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state='READY',task_id=?,context_json=?,input_view=? WHERE goal_id=? AND generation=? AND node_id=? AND state='BLOCKED'`, tid, r.Context, r.InputView, f.GoalID, f.Generation, id); e != nil {
			return e
		}
	}
	complete := true
	for _, r := range nodes {
		if r.State != "SUCCEEDED" && r.State != "SKIPPED" {
			complete = false
		}
	}
	if complete {
		output := map[string]any{}
		for _, port := range f.Plan.Spec.Outputs {
			r := nodes[port.Node]
			if r.State != "SUCCEEDED" {
				j.StopReason = "GRAPH_OUTPUT_SKIPPED"
				j.ErrorCode = j.StopReason
				return s.stopGraphTx(ctx, q, j, nodes)
			}
			var values map[string]json.RawMessage
			if e = json.Unmarshal(r.Output, &values); e != nil {
				return e
			}
			output[port.Node+"/"+port.Port] = values[port.Port]
		}
		raw := job.JSON(output)
		if _, e = q.ExecContext(ctx, `UPDATE iu_graphs SET output_hash=? WHERE job_id=?`, graph.Hash(raw), j.ID); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `UPDATE jobs SET result_json=? WHERE id=?`, job.JSON(map[string]any{"job_id": j.ID, "state": "SUCCEEDED", "outputs": output, "output_sha256": graph.Hash(raw)}), j.ID); e != nil {
			return e
		}
		return jobState(ctx, q, j, "SUCCEEDED", "", "")
	}
	if j.State == "QUEUED" {
		return jobState(ctx, q, j, "EXECUTING", "", "")
	}
	return nil
}

func (s *Server) stopGraphTx(ctx context.Context, q store.Query, j *Job, nodes map[string]*graphNodeRow) error {
	if e := jobState(ctx, q, j, "STOPPING", j.StopReason, j.ErrorCode); e != nil {
		return e
	}
	stopped := true
	for _, r := range nodes {
		if !r.Task.Valid {
			if r.State != "SUCCEEDED" && r.State != "SKIPPED" {
				if _, e := q.ExecContext(ctx, `UPDATE iu_nodes SET state='CANCELED' WHERE goal_id=? AND generation=? AND node_id=?`, j.graph.GoalID, j.graph.Generation, r.ID); e != nil {
					return e
				}
			}
			continue
		}
		t, _, e := readTask(ctx, q, r.Task.String)
		if e != nil {
			return e
		}
		var released int
		if e = q.QueryRowContext(ctx, `SELECT coalesce((SELECT released FROM attempts WHERE id=t.attempt),1) FROM tasks t WHERE t.id=?`, t.TaskId).Scan(&released); e != nil {
			return e
		}
		if !terminal(t.State) {
			if t.AttemptId == "" {
				if e = setState(ctx, q, t.TaskId, "CANCELED", "", ""); e != nil {
					return e
				}
				r.State = "CANCELED"
			} else {
				stopped = false
				if e = setState(ctx, q, t.TaskId, "CANCELING", j.StopReason, "graph stopping"); e != nil {
					return e
				}
				if e = s.stopCommand(ctx, q, t); e != nil {
					return e
				}
			}
		} else if released == 0 {
			stopped = false
		}
		if terminal(t.State) && t.State != "SUCCEEDED" {
			r.State = t.State
			r.Error = t.ErrorCode
		}
		if _, e = q.ExecContext(ctx, `UPDATE iu_nodes SET state=?,error_code=? WHERE goal_id=? AND generation=? AND node_id=?`, r.State, r.Error, j.graph.GoalID, j.graph.Generation, r.ID); e != nil {
			return e
		}
	}
	if !stopped {
		return nil
	}
	state := "FAILED"
	if j.StopReason == "USER_CANCEL" {
		state = "CANCELED"
	}
	if _, e := q.ExecContext(ctx, `UPDATE jobs SET result_json=? WHERE id=?`, job.JSON(map[string]string{"job_id": j.ID, "state": state, "error_code": j.ErrorCode}), j.ID); e != nil {
		return e
	}
	if _, e := q.ExecContext(ctx, `UPDATE iu_graphs SET output_hash=? WHERE job_id=?`, job.Hash(job.JSON(map[string]string{"state": state, "error_code": j.ErrorCode})), j.ID); e != nil {
		return e
	}
	return jobState(ctx, q, j, state, j.StopReason, j.ErrorCode)
}

func assignmentGraph(ctx context.Context, q store.Query, j *Job, taskID, key string) (*pb.JobExecution, error) {
	n, ok := graphNode(j.graph, key)
	if !ok {
		return nil, fmt.Errorf("GRAPH_NODE_MISSING")
	}
	op := graphOperator(j.graph, n)
	var contextJSON, viewJSON []byte
	if e := q.QueryRowContext(ctx, `SELECT context_json,input_view FROM iu_nodes WHERE goal_id=? AND generation=? AND node_id=? AND task_id=?`, j.graph.GoalID, j.graph.Generation, key, taskID).Scan(&contextJSON, &viewJSON); e != nil {
		return nil, e
	}
	inv := graph.Invocation{Version: "computecloud-job-io-v2", GoalID: j.graph.GoalID, Generation: j.graph.Generation, NodeID: key, Outputs: op.Outputs, Kind: op.Kind, CommandProfile: op.CommandProfile}
	inv.Schemas = map[string]json.RawMessage{}
	for _, port := range op.Outputs {
		inv.Schemas[port.Schema] = j.graph.Plan.Spec.Schemas[port.Schema]
	}
	if e := json.Unmarshal(contextJSON, &inv.Context); e != nil {
		return nil, e
	}
	if e := json.Unmarshal(viewJSON, &inv.View); e != nil {
		return nil, e
	}
	ex := graphExecutionFor(j.graph, op)
	if ex == nil {
		return nil, fmt.Errorf("GRAPH_EXECUTION_BINDING_MISSING")
	}
	return &pb.JobExecution{JobId: j.ID, Stage: "single", PartitionKey: key, TemplateDigest: j.graph.Digests[job.TemplateKey(*ex)], GraphExecutionJson: job.JSON(inv)}, nil
}
