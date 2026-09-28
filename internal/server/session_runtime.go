package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type runtimeSessionPayload struct {
	SessionRef string `json:"session_ref"`
}

func persistRuntimeSessionRef(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || (event.Type != "session.started" && event.Type != "session.resumed") {
		return nil
	}
	var payload runtimeSessionPayload
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil {
		return status.Error(codes.InvalidArgument, "invalid runtime session payload")
	}
	payload.SessionRef = strings.TrimSpace(payload.SessionRef)
	if payload.SessionRef == "" || len(payload.SessionRef) > 2048 {
		return status.Error(codes.InvalidArgument, "runtime session_ref required")
	}
	res, err := q.ExecContext(ctx, `UPDATE tasks
		SET native_session=?,updated=?
		WHERE id=? AND attempt=? AND current_generation=?`,
		payload.SessionRef, store.Now(), event.TaskId, event.AttemptId, event.Generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("runtime session event lost current Attempt ownership")
	}
	return nil
}
