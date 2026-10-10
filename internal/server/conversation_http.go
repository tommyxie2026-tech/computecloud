package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/conversation"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) conversationHTTP(allowPlain bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent/v1/models", s.httpConversationModels)
	mux.HandleFunc("GET /agent/v1/models/{model}", s.httpConversationModel)
	mux.HandleFunc("POST /agent/v1/messages", s.httpConversationMessages)
	mux.HandleFunc("POST /agent/v1/responses", s.httpConversationResponses)
	mux.HandleFunc("POST /agent/v1/messages/count_tokens", s.httpConversationUnsupported)
	mux.HandleFunc("POST /agent/v1/responses/input_tokens", s.httpConversationUnsupported)
	mux.HandleFunc("POST /agent/v1/responses/compact", s.httpConversationUnsupported)
	mux.HandleFunc("GET /agent/v1/requests/{id}", s.httpConversationRequest)
	mux.HandleFunc("POST /agent/v1/requests/{id}/cancel", s.httpConversationCancel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.ConversationJobs.Enabled {
			http.NotFound(w, r)
			return
		}
		if allowPlain {
			if !rpcutil.Loopback(r.RemoteAddr) {
				httpError(w, status.Error(codes.PermissionDenied, "loopback connection required"))
				return
			}
		} else if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 {
			httpError(w, status.Error(codes.FailedPrecondition, "TLS 1.3 required"))
			return
		}
		if len(r.Header.Values("Authorization")) != 1 {
			httpError(w, status.Error(codes.Unauthenticated, "authentication required"))
			return
		}
		ctx, err := s.auth.Bearer(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			httpError(w, err)
			return
		}
		if _, err = rpcutil.User(ctx); err != nil {
			httpError(w, err)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) httpConversationUnsupported(w http.ResponseWriter, _ *http.Request) {
	httpError(w, status.Error(codes.Unimplemented, "CAPABILITY_UNSUPPORTED"))
}

func (s *Server) httpConversationModels(w http.ResponseWriter, r *http.Request) {
	_, p, e := s.conversationProfile(r.Context(), "conversations:read")
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": p.PublicModel, "object": "model", "owned_by": "computecloud"}}})
}
func (s *Server) httpConversationModel(w http.ResponseWriter, r *http.Request) {
	_, p, e := s.conversationProfile(r.Context(), "conversations:read")
	if e != nil {
		httpError(w, e)
		return
	}
	if r.PathValue("model") != p.PublicModel {
		httpError(w, status.Error(codes.NotFound, "MODEL_NOT_FOUND"))
		return
	}
	jsonResponse(w, 200, map[string]any{"id": p.PublicModel, "object": "model", "owned_by": "computecloud"})
}

func (s *Server) httpConversationMessages(w http.ResponseWriter, r *http.Request) {
	s.httpConversationSubmit(w, r, "messages")
}
func (s *Server) httpConversationResponses(w http.ResponseWriter, r *http.Request) {
	s.httpConversationSubmit(w, r, "responses")
}
func (s *Server) httpConversationSubmit(w http.ResponseWriter, r *http.Request, protocol string) {
	_, p, e := s.conversationProfile(r.Context(), "conversations:submit")
	if e != nil {
		httpError(w, e)
		return
	}
	body, e := readJSONBody(w, r, s.cfg.ConversationJobs.MaxRequestBytes)
	if e != nil {
		httpError(w, e)
		return
	}
	var req conversation.Request
	if protocol == "messages" {
		req, e = conversation.DecodeMessages(body, int(s.cfg.ConversationJobs.MaxRequestBytes), p.MaxOutputTokens)
	} else {
		req, e = conversation.DecodeResponses(body, int(s.cfg.ConversationJobs.MaxRequestBytes), p.MaxOutputTokens)
	}
	if e != nil {
		if errors.Is(e, conversation.ErrUnsupported) {
			httpError(w, status.Error(codes.Unimplemented, "CAPABILITY_UNSUPPORTED"))
			return
		}
		httpError(w, status.Error(codes.InvalidArgument, "invalid or unsupported conversation request"))
		return
	}
	if len(r.Header.Values("Idempotency-Key")) > 1 {
		httpError(w, status.Error(codes.InvalidArgument, "invalid idempotency key"))
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	rec, e := s.beginConversationRequest(r.Context(), req, idem)
	if e != nil {
		httpError(w, e)
		return
	}
	rec, _, e = s.ensureConversationJob(r.Context(), rec, rec.Request)
	if e != nil {
		httpError(w, e)
		return
	}
	w.Header().Set("X-ComputeCloud-Request-ID", rec.ID)
	w.Header().Set("X-ComputeCloud-Job-ID", rec.JobID)
	if req.Stream {
		s.streamConversation(w, r, rec, p)
		return
	}
	result, e := s.waitConversation(r.Context(), rec, p, 30*time.Minute)
	if e != nil {
		httpError(w, e)
		return
	}
	result, truncated := limitConversationOutput(result, p.MaxOutputBytes)
	if truncated {
		w.Header().Set("X-ComputeCloud-Output-Truncated", "true")
	}
	s.writeConversationResponse(w, protocol, rec.ID, p.PublicModel, result)
}

func (s *Server) loadConversation(ctx context.Context, id, scope string) (conversationRecord, *config.ConversationProfile, error) {
	caller, p, e := s.conversationProfile(ctx, scope)
	if e != nil {
		return conversationRecord{}, nil, e
	}
	var rec conversationRecord
	var raw []byte
	e = s.db.SQL.QueryRowContext(ctx, "SELECT request_id,owner,profile_id,protocol,request_blob,job_id,state FROM conversation_requests WHERE request_id=?", id).Scan(&rec.ID, &rec.Owner, &rec.Profile, &rec.Protocol, &raw, &rec.JobID, &rec.State)
	if e != nil {
		return rec, nil, dbErr(e)
	}
	if caller.Identity.Owner != rec.Owner || caller.Identity.ConversationProfile != rec.Profile {
		return rec, nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	if e = json.Unmarshal(raw, &rec.Request); e != nil {
		return rec, nil, dbErr(e)
	}
	return rec, &p, nil
}

func (s *Server) observeConversation(ctx context.Context, rec conversationRecord, p config.ConversationProfile, operation string) (context.Context, error) {
	_, _, err := s.conversationProfile(ctx, "conversations:"+operation)
	if err != nil {
		return ctx, err
	}
	for _, u := range s.cfg.Users {
		if u.Owner == p.ExecutionOwner && config.Contains(u.Projects, p.ProjectID) && config.Contains(u.Credentials, p.Execution.CredentialRef) {
			return rpcutil.ConversationJobContext(ctx, u, p.ID, operation)
		}
	}
	return ctx, status.Error(codes.FailedPrecondition, "conversation execution identity unavailable")
}

func (s *Server) waitConversation(ctx context.Context, rec conversationRecord, p config.ConversationProfile, timeout time.Duration) (string, error) {
	if rec.JobID == "" {
		return "", status.Error(codes.FailedPrecondition, "CONVERSATION_JOB_NOT_CREATED")
	}
	readCtx, err := s.observeConversation(ctx, rec, p, "read")
	if err != nil {
		return "", err
	}
	waitCtx, cancel := context.WithTimeout(readCtx, timeout)
	defer cancel()
	for {
		j, e := s.GetJob(waitCtx, rec.JobID)
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				return "", status.Error(codes.DeadlineExceeded, "conversation observation timed out")
			}
			if errors.Is(e, context.Canceled) {
				return "", status.Error(codes.Canceled, "conversation observation stopped")
			}
			return "", e
		}
		if terminal(j.State) {
			state := j.State
			switch state {
			case "SUCCEEDED":
				var result string
				e = s.db.SQL.QueryRowContext(waitCtx, "SELECT result FROM tasks WHERE job_id=? AND stage IN ('single','reduce') ORDER BY CASE stage WHEN 'reduce' THEN 0 ELSE 1 END LIMIT 1", rec.JobID).Scan(&result)
				if e != nil {
					return "", dbErr(e)
				}
				_, _ = s.db.SQL.ExecContext(waitCtx, "UPDATE conversation_requests SET state='COMPLETED',updated=? WHERE request_id=?", store.Now(), rec.ID)
				return result, nil
			case "CANCELED":
				_, _ = s.db.SQL.ExecContext(waitCtx, "UPDATE conversation_requests SET state='CANCELLED',updated=? WHERE request_id=?", store.Now(), rec.ID)
				return "", status.Error(codes.Canceled, "JOB_CANCELED")
			default:
				_, _ = s.db.SQL.ExecContext(waitCtx, "UPDATE conversation_requests SET state='FAILED',updated=? WHERE request_id=?", store.Now(), rec.ID)
				return "", status.Error(codes.FailedPrecondition, "JOB_FAILED")
			}
		}
		if err = wait(waitCtx, 250); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return "", status.Error(codes.DeadlineExceeded, "conversation observation timed out")
			}
			if errors.Is(err, context.Canceled) {
				return "", status.Error(codes.Canceled, "conversation observation stopped")
			}
			return "", err
		}
	}
}

func (s *Server) streamConversation(w http.ResponseWriter, r *http.Request, rec conversationRecord, p config.ConversationProfile) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, status.Error(codes.Internal, "streaming unavailable"))
		return
	}
	fmt.Fprintf(w, ": job submitted\n\n")
	flusher.Flush()
	deadline := time.Now().Add(30 * time.Minute)
	var result string
	var e error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			e = status.Error(codes.DeadlineExceeded, "conversation observation timed out")
			break
		}
		result, e = s.waitConversation(r.Context(), rec, p, min(15*time.Second, remaining))
		if status.Code(e) == codes.DeadlineExceeded {
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
			continue
		}
		break
	}
	if e != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", job.JSON(map[string]string{"code": cleanCode(e)}))
		flusher.Flush()
		return
	}
	result, truncated := limitConversationOutput(result, p.MaxOutputBytes)
	if truncated {
		fmt.Fprint(w, "event: computecloud.output_truncated\ndata: {\"truncated\":true}\n\n")
	}
	s.writeConversationStream(w, rec.Protocol, rec.ID, p.PublicModel, result)
	flusher.Flush()
}

func (s *Server) httpConversationRequest(w http.ResponseWriter, r *http.Request) {
	rec, p, e := s.loadConversation(r.Context(), r.PathValue("id"), "conversations:read")
	if e != nil {
		httpError(w, e)
		return
	}
	result, e := s.waitConversation(r.Context(), rec, *p, 2*time.Second)
	if e != nil && status.Code(e) != codes.DeadlineExceeded {
		httpError(w, e)
		return
	}
	state, usage := rec.State, map[string]any(nil)
	if rec.JobID != "" {
		ctx, e := s.observeConversation(r.Context(), rec, *p, "read")
		if e != nil {
			httpError(w, e)
			return
		}
		j, e := s.GetJob(ctx, rec.JobID)
		if e != nil {
			httpError(w, e)
			return
		}
		state, usage = j.State, j.Usage
	}
	result, truncated := limitConversationOutput(result, p.MaxOutputBytes)
	code := 200
	if !terminal(state) {
		code = 202
	}
	jsonResponse(w, code, map[string]any{"id": rec.ID, "job_id": rec.JobID, "state": state, "result": result, "usage": usage, "truncated": truncated, "provenance": map[string]string{"job_id": rec.JobID, "profile_id": rec.Profile}})
}
func (s *Server) httpConversationCancel(w http.ResponseWriter, r *http.Request) {
	rec, p, e := s.loadConversation(r.Context(), r.PathValue("id"), "conversations:cancel")
	if e != nil {
		httpError(w, e)
		return
	}
	reason := "client requested cancellation"
	if r.ContentLength != 0 {
		body, err := readJSONBody(w, r, 4096)
		if err != nil {
			httpError(w, err)
			return
		}
		var in struct {
			Reason string `json:"reason"`
		}
		if err = jsonutil.Decode(body, &in); err != nil || len(in.Reason) > 1000 {
			httpError(w, status.Error(codes.InvalidArgument, "invalid cancellation"))
			return
		}
		if in.Reason != "" {
			reason = in.Reason
		}
	}
	ctx, e := s.observeConversation(r.Context(), rec, *p, "cancel")
	if e != nil {
		httpError(w, e)
		return
	}
	_, e = s.CancelJob(ctx, rec.JobID, CancelJobRequest{ControlID: "conversation-cancel_" + rec.ID, Reason: reason})
	if e != nil {
		httpError(w, e)
		return
	}
	jsonResponse(w, 202, map[string]any{"id": rec.ID, "job_id": rec.JobID, "state": "cancellation_requested"})
}

func (s *Server) writeConversationResponse(w http.ResponseWriter, protocol, id, model, result string) {
	if protocol == "messages" {
		jsonResponse(w, 200, map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "model": model, "content": []any{map[string]string{"type": "text", "text": result}}, "stop_reason": "end_turn"})
		return
	}
	jsonResponse(w, 200, map[string]any{"id": "resp_" + id, "object": "response", "model": model, "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": result}}}}})
}
func (s *Server) writeConversationStream(w io.Writer, protocol, id, model, result string) {
	if protocol == "messages" {
		events := []any{map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil}}, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": result}}, map[string]any{"type": "content_block_stop", "index": 0}, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}}, map[string]any{"type": "message_stop"}}
		for _, ev := range events {
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.(map[string]any)["type"], b)
		}
		return
	}
	for _, ev := range []struct {
		typ  string
		body any
	}{{"response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_" + id, "object": "response", "status": "in_progress", "model": model}}}, {"response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": result}}, {"response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_" + id, "object": "response", "status": "completed", "model": model, "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": result}}}}}}}} {
		b, _ := json.Marshal(ev.body)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.typ, b)
	}
}

func limitConversationOutput(text string, maxBytes int) (string, bool) {
	if maxBytes < 1 || len(text) <= maxBytes {
		return text, false
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end], true
}
