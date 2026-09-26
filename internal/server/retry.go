package server

import (
	"context"
	"database/sql"
	"fmt"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type retryPlan struct {
	retry       bool
	exhausted   bool
	retryAt     int64
	maxAttempts int
	reason      string
	jobID       string
}

func retryableAttemptError(code string) bool {
	switch code {
	case "RUNTIME_FAILED", "WORKER_RESTARTED", "STORAGE_UNAVAILABLE", "ARTIFACT_ERROR":
		return true
	default:
		return false
	}
}

func retryBackoffMS(generation int64) int64 {
	if generation < 1 {
		generation = 1
	}
	shift := generation - 1
	if shift > 6 {
		shift = 6
	}
	ms := int64(500) << shift
	if ms > 30000 {
		return 30000
	}
	return ms
}

func (s *Server) planRetry(ctx context.Context, q store.Query, taskID, code string, generation int64) (retryPlan, error) {
	var plan retryPlan
	if !retryableAttemptError(code) {
		return plan, nil
	}
	var jid sql.NullString
	var stage, key string
	var deadline int64
	if err := q.QueryRowContext(ctx, "SELECT job_id,stage,partition_key,deadline FROM tasks WHERE id=?", taskID).Scan(&jid, &stage, &key, &deadline); err != nil {
		return plan, err
	}
	if !jid.Valid {
		return plan, nil
	}
	j, err := readJob(ctx, q, jid.String)
	if err != nil {
		return plan, err
	}
	if terminal(j.State) || j.StopReason != "" || j.State == "RECONCILING" {
		return plan, nil
	}
	ex, ok := j.frozen.Spec.ExecutionFor(stage, key)
	if !ok || !ex.ReplaySafe || j.frozen.Spec.Limits.MaxAttemptsPerTask <= 1 {
		return plan, nil
	}
	plan.jobID = j.ID
	plan.maxAttempts = j.frozen.Spec.Limits.MaxAttemptsPerTask
	if generation >= int64(plan.maxAttempts) {
		plan.exhausted = true
		plan.reason = "MAX_ATTEMPTS"
		return plan, nil
	}
	plan.retryAt = store.Now() + retryBackoffMS(generation)
	if plan.retryAt >= deadline {
		plan.exhausted = true
		plan.reason = "DEADLINE"
		return plan, nil
	}
	plan.retry = true
	return plan, nil
}

func (s *Server) scheduleRetry(ctx context.Context, q store.Query, t *pb.Task, attemptID string, generation int64, code, message string, plan retryPlan) error {
	now := store.Now()
	res, err := q.ExecContext(ctx, `UPDATE tasks
		SET state='QUEUED',attempt='',worker='',error_code='',error_message='',result='',native_session='',
		    blocker='RETRY_BACKOFF',retry_after=?,updated=?
		WHERE id=? AND attempt=? AND current_generation=?`,
		plan.retryAt, now, t.TaskId, attemptID, generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("retry lost current attempt ownership")
	}
	if err = appendEvent(ctx, q, &pb.Event{
		TaskId: t.TaskId,
		Type:   "task.state_changed",
		PayloadJson: config.JSON(map[string]any{
			"from": t.State,
			"to":   "QUEUED",
		}),
	}, nil); err != nil {
		return err
	}
	if err = appendEvent(ctx, q, &pb.Event{
		TaskId:     t.TaskId,
		AttemptId:  attemptID,
		Generation: generation,
		Type:       "task.retry_scheduled",
		PayloadJson: config.JSON(map[string]any{
			"error_code":       code,
			"error_message":    message,
			"retry_after_ms":   plan.retryAt,
			"max_attempts":     plan.maxAttempts,
			"next_generation":  generation + 1,
		}),
	}, nil); err != nil {
		return err
	}
	return appendJobEvent(ctx, q, plan.jobID, "job.task_retry_scheduled", map[string]any{
		"task_id":          t.TaskId,
		"attempt_id":       attemptID,
		"generation":       generation,
		"error_code":       code,
		"retry_after_ms":   plan.retryAt,
		"max_attempts":     plan.maxAttempts,
		"next_generation":  generation + 1,
	}, "", "")
}

func appendRetryExhausted(ctx context.Context, q store.Query, t *pb.Task, attemptID string, generation int64, code string, plan retryPlan) error {
	if !plan.exhausted {
		return nil
	}
	if err := appendEvent(ctx, q, &pb.Event{
		TaskId:     t.TaskId,
		AttemptId:  attemptID,
		Generation: generation,
		Type:       "task.retry_exhausted",
		PayloadJson: job.JSON(map[string]any{
			"error_code":   code,
			"reason":       plan.reason,
			"max_attempts": plan.maxAttempts,
		}),
	}, nil); err != nil {
		return err
	}
	return appendJobEvent(ctx, q, plan.jobID, "job.task_retry_exhausted", map[string]any{
		"task_id":      t.TaskId,
		"attempt_id":   attemptID,
		"generation":   generation,
		"error_code":   code,
		"reason":       plan.reason,
		"max_attempts": plan.maxAttempts,
	}, "", "")
}
