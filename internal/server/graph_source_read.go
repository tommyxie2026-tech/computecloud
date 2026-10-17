package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"unicode/utf8"
)

type GraphSourceReadRequest struct {
	ID            string     `json:"id"`
	NodeID        string     `json:"node_id"`
	PlanHash      string     `json:"plan_sha256"`
	ExpectedReads int        `json:"expected_reads"`
	Span          graph.Span `json:"span"`
}
type GraphSourceReadReceipt struct {
	ReceiptID    string     `json:"receipt_id"`
	GoalID       string     `json:"goal_id"`
	Generation   int64      `json:"graph_generation"`
	PlanHash     string     `json:"plan_sha256"`
	NodeID       string     `json:"node_id"`
	Span         graph.Span `json:"span"`
	Text         string     `json:"text"`
	SHA256       string     `json:"sha256"`
	Role         string     `json:"role,omitempty"`
	MessageIndex int        `json:"message_index"`
	ReadsUsed    int        `json:"reads_used"`
}

func (s *Server) ReadGraphSource(ctx context.Context, id string, in GraphSourceReadRequest) (*GraphSourceReadReceipt, error) {
	if !job.ValidKey(in.ID) || !job.Ref(in.NodeID) || !job.ValidHash(in.PlanHash) || in.ExpectedReads < 0 {
		return nil, status.Error(codes.InvalidArgument, "SOURCE_READ_INVALID")
	}
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, status.Error(codes.NotFound, "NOT_GRAPH_JOB")
	}
	f := j.graph
	if in.PlanHash != f.Plan.Digest {
		return nil, status.Error(codes.Aborted, "SOURCE_READ_PLAN_MISMATCH")
	}
	if _, ok := graphNode(f, in.NodeID); !ok {
		return nil, status.Error(codes.NotFound, "SOURCE_READ_NODE_UNKNOWN")
	}
	var out *GraphSourceReadReceipt
	e = s.db.Tx(ctx, func(q store.Query) error {
		actor, e := graphMutationAuthorityTx(ctx, q, j, "jobs:read")
		if e != nil {
			return e
		}
		var request, body []byte
		e = q.QueryRowContext(ctx, `SELECT request,body FROM iu_source_reads WHERE goal_id=? AND generation=? AND node_id=? AND read_id=?`, f.GoalID, f.Generation, in.NodeID, in.ID).Scan(&request, &body)
		if e == nil {
			if !bytes.Equal(request, job.JSON(in)) {
				return status.Error(codes.AlreadyExists, "IDEMPOTENCY_CONFLICT")
			}
			return json.Unmarshal(body, &out)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var count int
		if e = q.QueryRowContext(ctx, `SELECT count(*) FROM iu_source_reads WHERE goal_id=? AND generation=? AND node_id=?`, f.GoalID, f.Generation, in.NodeID).Scan(&count); e != nil {
			return e
		}
		if count != in.ExpectedReads {
			return status.Error(codes.Aborted, "SOURCE_READ_STALE")
		}
		if count >= 2 {
			return status.Error(codes.ResourceExhausted, "SOURCE_READ_BUDGET_EXHAUSTED")
		}
		var source *graph.Source
		for i := range f.Plan.Spec.Sources {
			if f.Plan.Spec.Sources[i].ID == in.Span.Source {
				source = &f.Plan.Spec.Sources[i]
			}
		}
		span := in.Span
		if source == nil || span.SHA256 != graph.Hash([]byte(source.Text)) || span.Start < 0 || span.End <= span.Start || span.End > len(source.Text) || span.End-span.Start > 16<<10 {
			return status.Error(codes.InvalidArgument, "SOURCE_READ_SPAN_INVALID")
		}
		text := source.Text[span.Start:span.End]
		if !utf8.ValidString(text) {
			return status.Error(codes.InvalidArgument, "SOURCE_READ_UTF8_BOUNDARY")
		}
		out = &GraphSourceReadReceipt{ReceiptID: "read_" + store.ID(), GoalID: f.GoalID, Generation: f.Generation, PlanHash: f.Plan.Digest, NodeID: in.NodeID, Span: span, Text: text, SHA256: graph.Hash([]byte(text)), Role: source.Role, MessageIndex: source.MessageIndex, ReadsUsed: count + 1}
		if _, e = q.ExecContext(ctx, `INSERT INTO iu_source_reads VALUES(?,?,?,?,?,?,?,?)`, f.GoalID, f.Generation, in.NodeID, in.ID, job.JSON(in), job.JSON(out), actor, store.Now()); e != nil {
			return e
		}
		return appendJobEvent(ctx, q, id, "graph.source_read", map[string]any{"receipt_id": out.ReceiptID, "node_id": in.NodeID, "source": span.Source, "source_sha256": span.SHA256, "slice_sha256": out.SHA256, "bytes": len(text), "reads_used": out.ReadsUsed}, "", "")
	})
	return out, e
}
func (s *Server) httpGraphSourceRead(w http.ResponseWriter, r *http.Request) {
	raw, e := readJSONBody(w, r, 4096)
	if e != nil {
		httpError(w, e)
		return
	}
	var in GraphSourceReadRequest
	if e = jsonutil.Decode(raw, &in); e != nil {
		httpError(w, status.Error(codes.InvalidArgument, "SOURCE_READ_INVALID"))
		return
	}
	out, e := s.ReadGraphSource(r.Context(), r.PathValue("id"), in)
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
