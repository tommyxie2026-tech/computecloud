package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"strconv"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/control"
	"github.com/tommyxie2026-tech/computecloud/internal/rpcutil"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ControlRuntime struct {
	Profile      string               `json:"profile"`
	Version      string               `json:"version,omitempty"`
	Capabilities []control.Capability `json:"capabilities"`
}

type ControlBootstrap struct {
	ProtocolMin string           `json:"protocol_min"`
	ProtocolMax string           `json:"protocol_max"`
	ServerEpoch string           `json:"server_epoch"`
	ReadOnly    bool             `json:"read_only"`
	Runtimes    []ControlRuntime `json:"runtimes"`
}

func controlCapabilities(runtimeCapabilities []string) []control.Capability {
	seen := map[control.Capability]bool{}
	add := func(capability control.Capability) {
		if !seen[capability] {
			seen[capability] = true
		}
	}
	for _, value := range runtimeCapabilities {
		switch value {
		case "event_stream", "runtime:event_stream":
			add(control.CapabilityStreamOutput)
			add(control.CapabilityStructuredOutput)
		case "cancel", "runtime:cancel":
			add(control.CapabilityCancel)
		default:
			if strings.HasPrefix(value, "control:") {
				capability := control.Capability(strings.TrimPrefix(value, "control:"))
				if control.ValidateCapabilities([]control.Capability{capability}) == nil {
					add(capability)
				}
			}
		}
	}
	out := make([]control.Capability, 0, len(seen))
	for capability := range seen {
		out = append(out, capability)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Server) controlRuntimeSnapshot() []ControlRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	byProfile := map[string]ControlRuntime{}
	for _, peer := range s.peers {
		for _, runtime := range peer.hello.Runtimes {
			current, exists := byProfile[runtime.Profile]
			candidate := ControlRuntime{
				Profile:      runtime.Profile,
				Version:      runtime.Version,
				Capabilities: controlCapabilities(runtime.Capabilities),
			}
			// A profile may be present on multiple Workers. Keep a deterministic
			// representative projection; scheduling remains the source of truth.
			if !exists || candidate.Version < current.Version {
				byProfile[runtime.Profile] = candidate
			}
		}
	}
	out := make([]ControlRuntime, 0, len(byProfile))
	for _, runtime := range byProfile {
		out = append(out, runtime)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out
}

func (s *Server) ControlBootstrap(ctx context.Context) (*ControlBootstrap, error) {
	if _, err := rpcutil.Require(ctx, "jobs:read", false); err != nil {
		return nil, err
	}
	return &ControlBootstrap{
		ProtocolMin: control.ProtocolV1Alpha1,
		ProtocolMax: control.ProtocolV1Alpha1,
		ServerEpoch: s.controlEpoch,
		ReadOnly:    false,
		Runtimes:    s.controlRuntimeSnapshot(),
	}, nil
}

func controlSessionState(taskState string) control.SessionState {
	switch taskState {
	case "STARTING":
		return control.SessionStarting
	case "RUNNING", "VERIFYING":
		return control.SessionRunning
	case "CANCELING":
		return control.SessionInterrupting
	case "RECONCILING":
		return control.SessionUnverifiable
	case "SUCCEEDED":
		return control.SessionCompleted
	case "FAILED":
		return control.SessionFailed
	case "CANCELED":
		return control.SessionCanceled
	default:
		return control.SessionCreated
	}
}

func (s *Server) runtimeProjection(workerID, profile string) (string, []control.Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer := s.peers[workerID]
	if peer == nil {
		return "", nil
	}
	for _, runtime := range peer.hello.Runtimes {
		if runtime.Profile == profile {
			return runtime.Version, controlCapabilities(runtime.Capabilities)
		}
	}
	return "", nil
}

func (s *Server) JobSessions(ctx context.Context, jobID string) ([]control.AgentSession, error) {
	if _, err := s.jobAuthorized(ctx, jobID, "jobs:read"); err != nil {
		return nil, err
	}
	rows, err := s.db.SQL.QueryContext(ctx, `SELECT t.id,t.attempt,t.current_generation,t.worker,t.state,t.spec,t.native_session,t.created,t.updated
		FROM tasks t
		WHERE t.job_id=? AND t.attempt<>''
		ORDER BY t.stage,t.partition_key,t.id`, jobID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := []control.AgentSession{}
	for rows.Next() {
		var taskID, attemptID, workerID, state, nativeSession string
		var generation, created, updated int64
		var raw []byte
		if err = rows.Scan(&taskID, &attemptID, &generation, &workerID, &state, &raw, &nativeSession, &created, &updated); err != nil {
			return nil, dbErr(err)
		}
		spec := new(pb.TaskSpec)
		if err = decode(raw, spec); err != nil {
			return nil, status.Error(codes.Unavailable, "invalid persisted task spec")
		}
		version, capabilities := s.runtimeProjection(workerID, spec.RuntimeProfile)
		session := control.AgentSession{
			ProtocolVersion:   control.ProtocolV1Alpha1,
			SessionID:         attemptID,
			JobID:             jobID,
			TaskID:            taskID,
			AttemptID:         attemptID,
			Generation:        generation,
			WorkerID:          workerID,
			Runtime:           spec.RuntimeProfile,
			RuntimeVersion:    version,
			RuntimeSessionRef: nativeSession,
			State:             controlSessionState(state),
			Capabilities:      capabilities,
			CreatedAt:         time.UnixMilli(created).UTC(),
			UpdatedAt:         time.UnixMilli(updated).UTC(),
		}
		if err = session.Validate(); err != nil {
			return nil, status.Error(codes.Unavailable, "invalid control session projection")
		}
		out = append(out, session)
	}
	return out, dbErr(rows.Err())
}

func (s *Server) JobSession(ctx context.Context, jobID, sessionID string) (*control.AgentSession, error) {
	sessions, err := s.JobSessions(ctx, jobID)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if sessions[i].SessionID == sessionID {
			return &sessions[i], nil
		}
	}
	return nil, status.Error(codes.NotFound, "NOT_FOUND")
}

func (s *Server) httpControlBootstrap(w http.ResponseWriter, r *http.Request) {
	v, err := s.ControlBootstrap(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

func (s *Server) httpJobSessions(w http.ResponseWriter, r *http.Request) {
	v, err := s.JobSessions(r.Context(), r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"sessions": v})
}

func (s *Server) httpJobSession(w http.ResponseWriter, r *http.Request) {
	v, err := s.JobSession(r.Context(), r.PathValue("id"), r.PathValue("session"))
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

func sseEventName(value string) string {
	if value == "" {
		return "message"
	}
	for _, r := range value {
		if !(r == '.' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "message"
		}
	}
	return value
}

func (s *Server) httpJobEventStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if _, err := s.jobAuthorized(r.Context(), jobID, "jobs:read"); err != nil {
		httpError(w, err)
		return
	}
	cursorValue := r.URL.Query().Get("after_seq")
	if cursorValue == "" {
		cursorValue = r.Header.Get("Last-Event-ID")
	}
	after, err := decimal(cursorValue)
	if err != nil {
		httpError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, status.Error(codes.Unavailable, "SSE_UNAVAILABLE"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	for {
		events, eventErr := s.JobEvents(r.Context(), jobID, after, 100)
		if eventErr != nil {
			if after == 0 {
				httpError(w, eventErr)
			}
			return
		}
		for _, event := range events.Events {
			body, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return
			}
			if _, err = w.Write([]byte("id: " + strconv.FormatInt(event.Seq, 10) + "\n")); err != nil {
				return
			}
			if _, err = w.Write([]byte("event: " + sseEventName(event.Type) + "\n")); err != nil {
				return
			}
			if _, err = w.Write([]byte("data: " + string(body) + "\n\n")); err != nil {
				return
			}
			after = event.Seq
		}
		flusher.Flush()
		job, readErr := readJob(r.Context(), s.db.SQL, jobID)
		if readErr != nil {
			return
		}
		if terminal(job.State) && after >= job.LastSeq {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func mirrorRuntimeEventToJob(ctx context.Context, q store.Query, event *pb.Event) error {
	if event == nil || event.TaskId == "" {
		return nil
	}
	var jobID sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT job_id FROM tasks WHERE id=?", event.TaskId).Scan(&jobID); err != nil {
		return err
	}
	if !jobID.Valid {
		return nil
	}
	var payload any = map[string]any{}
	if len(event.PayloadJson) > 0 {
		var decoded any
		if err := json.Unmarshal(event.PayloadJson, &decoded); err != nil {
			return err
		}
		payload = decoded
	}
	body := map[string]any{
		"protocol_version": control.ProtocolV1Alpha1,
		"event_id": event.EventId,
		"task_id": event.TaskId,
		"attempt_id": event.AttemptId,
		"generation": event.Generation,
		"worker_seq": event.WorkerSeq,
		"type": event.Type,
		"payload": payload,
	}
	return appendJobEvent(ctx, q, jobID.String, "control.runtime_event", body, "", "")
}

