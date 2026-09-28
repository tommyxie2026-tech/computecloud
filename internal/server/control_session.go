package server

import (
	"context"
	"encoding/json"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func persistRuntimeSessionRef(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || event.Type != "session.started" {
		return nil
	}
	var payload struct {
		SessionRef string `json:"session_ref"`
	}
	if err := json.Unmarshal(event.PayloadJson, &payload); err != nil || payload.SessionRef == "" {
		return status.Error(codes.InvalidArgument, "invalid session.started payload")
	}
	var currentAttempt, currentSession string
	var currentGeneration int64
	if err := q.QueryRowContext(ctx,
		"SELECT attempt,current_generation,native_session FROM tasks WHERE id=?",
		event.TaskId,
	).Scan(&currentAttempt, &currentGeneration, &currentSession); err != nil {
		return err
	}
	if currentAttempt != event.AttemptId || currentGeneration != event.Generation {
		return status.Error(codes.FailedPrecondition, control.ErrorAttemptFenced)
	}
	if currentSession != "" && currentSession != payload.SessionRef {
		return status.Error(codes.AlreadyExists, "RUNTIME_SESSION_CONFLICT")
	}
	_, err := q.ExecContext(ctx,
		"UPDATE tasks SET native_session=?,updated=? WHERE id=? AND attempt=? AND current_generation=?",
		payload.SessionRef, store.Now(), event.TaskId, event.AttemptId, event.Generation)
	return err
}
