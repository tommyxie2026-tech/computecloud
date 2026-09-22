package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	jobv1 "github.com/tommyxie2026-tech/computecloud/api/job/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

func (s *Server) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "computecloud", Version: "0.2.0"}, nil)
	var specSchema map[string]any
	if e := json.Unmarshal(jobv1.Schema, &specSchema); e != nil {
		panic(e)
	}
	defs := specSchema["$defs"]
	delete(specSchema, "$defs")
	delete(specSchema, "$schema")
	str := map[string]any{"type": "string", "minLength": 1}
	submitSchema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"idempotency_key", "spec"}, "properties": map[string]any{"idempotency_key": str, "spec": specSchema}, "$defs": defs}
	add := func(name, description string, schema any, handler func(context.Context, json.RawMessage) (any, error)) {
		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema, OutputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, e := handler(ctx, r.Params.Arguments)
			if e != nil {
				if p, ok := e.(*jsonrpc.Error); ok {
					return nil, p
				}
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: cleanCode(e)}}}, nil
			}
			raw := job.JSON(out)
			result := &mcp.CallToolResult{StructuredContent: json.RawMessage(raw), Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
			if len(job.JSON(result)) > 64<<10 {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "RESULT_TOO_LARGE: use the paginated HTTP API"}}}, nil
			}
			return result, nil
		})
	}
	bad := func(e error) error { return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: e.Error()} }
	add("submit_job", "Submit an explicit remote Agent Job; returns immediately with a durable job_id. Reuse the same idempotency_key after a network failure.", submitSchema, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Key  string          `json:"idempotency_key"`
			Spec json.RawMessage `json:"spec"`
		}
		if e := jsonutil.Decode(raw, &in); e != nil {
			return nil, bad(e)
		}
		return s.SubmitJob(ctx, in.Key, in.Spec)
	})
	schema := func(props map[string]any, required ...string) any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	add("get_job", "Read Job progress and bounded phase events; poll using poll_after_ms. Disconnecting does not cancel the Job.", schema(map[string]any{"job_id": str, "after_seq": map[string]any{"type": "string", "pattern": "^[0-9]+$"}}, "job_id"), func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID    string `json:"job_id"`
			After string `json:"after_seq"`
		}
		if e := jsonutil.Decode(raw, &in); e != nil {
			return nil, bad(e)
		}
		j, e := s.GetJob(ctx, in.ID)
		if e != nil {
			return nil, e
		}
		after, e := decimal(in.After)
		if e != nil {
			return nil, e
		}
		events, e := s.JobEvents(ctx, in.ID, after, 100)
		if e != nil {
			return nil, e
		}
		return map[string]any{"job": j, "events": events.Events, "next_seq": strconv.FormatInt(events.Next, 10), "has_more": events.HasMore}, nil
	})
	add("cancel_job", "Persist cancellation of the entire Job; completion still waits for Worker cleanup.", schema(map[string]any{"job_id": str, "control_id": str, "reason": map[string]any{"type": "string", "maxLength": 1000}}, "job_id", "control_id"), func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID        string `json:"job_id"`
			ControlID string `json:"control_id"`
			Reason    string `json:"reason"`
		}
		if e := jsonutil.Decode(raw, &in); e != nil {
			return nil, bad(e)
		}
		return s.CancelJob(ctx, in.ID, CancelJobRequest{ControlID: in.ControlID, Reason: in.Reason})
	})
	add("get_result", "Read terminal Job result and artifact references. Unfinished jobs return JOB_NOT_FINISHED.", schema(map[string]any{"job_id": str}, "job_id"), func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"job_id"`
		}
		if e := jsonutil.Decode(raw, &in); e != nil {
			return nil, bad(e)
		}
		return s.JobResult(ctx, in.ID)
	})
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: s.cfg.Jobs.MaxRequestBytes + 4096})
}
