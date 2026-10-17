package server

import (
	"context"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"net/http"
)

type GraphRecordSnapshot struct {
	Record           graph.MemoryRecord `json:"record"`
	RecordHash       string             `json:"record_sha256"`
	ViewHash         string             `json:"view_sha256"`
	ValidityRevision int64              `json:"validity_revision"`
}

func (s *Server) GraphRecord(ctx context.Context, id, node, record string) (*GraphRecordSnapshot, error) {
	j, e := s.jobAuthorized(ctx, id, "jobs:read")
	if e != nil {
		return nil, e
	}
	if j.graph == nil {
		return nil, importFailure("NOT_GRAPH")
	}
	out := &GraphRecordSnapshot{}
	e = s.db.Tx(ctx, func(q store.Query) error {
		if _, e := graphMutationAuthorityTx(ctx, q, j, "jobs:read"); e != nil {
			return e
		}
		view, e := graphMemoryTx(ctx, q, j.graph, node)
		if e != nil {
			return e
		}
		out.Record, e = verificationRecord(view, record)
		if e != nil {
			return e
		}
		out.RecordHash = graph.Hash(job.JSON(out.Record))
		out.ViewHash = view.SHA256
		out.ValidityRevision, e = graphValidityRevisionTx(ctx, q, j.graph)
		return e
	})
	return out, e
}
func (s *Server) httpGraphRecord(w http.ResponseWriter, r *http.Request) {
	out, e := s.GraphRecord(r.Context(), r.PathValue("id"), r.PathValue("node"), r.PathValue("record"))
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, out)
}
