package server

import (
	"encoding/json"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sessionControlHTTPInput struct {
	OperationID             string `json:"operation_id"`
	TaskID                  string `json:"task_id"`
	ExpectedAttemptID       string `json:"expected_attempt_id"`
	ExpectedGeneration      int64  `json:"expected_generation"`
	ExpectedResourceVersion int64  `json:"expected_resource_version"`
	Mode                    string `json:"mode,omitempty"`
	Content                 string `json:"content,omitempty"`
}

func writeControlReceipt(w http.ResponseWriter, receipt *ControlOperationReceipt) {
	code := http.StatusAccepted
	if receipt.Existing || receipt.State == "COMPLETED" || receipt.State == "REJECTED" {
		code = http.StatusOK
	}
	jsonResponse(w, code, receipt)
}

func (s *Server) httpSessionInput(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	b, err := readJSONBody(w, r, 64<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var in sessionControlHTTPInput
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid session input JSON"))
		return
	}
	sessionID := r.PathValue("session")
	if sessionID == "" || in.ExpectedAttemptID != sessionID || in.Content == "" {
		httpError(w, status.Error(codes.InvalidArgument, "session input identity mismatch"))
		return
	}
	payload := job.JSON(controlDispatchPayload{Mode: in.Mode, Content: in.Content})
	req := ControlOperationRequest{
		OperationID: in.OperationID,
		OperationType: "input",
		ResourceType: "session",
		ResourceID: sessionID,
		TaskID: in.TaskID,
		ExpectedAttemptID: in.ExpectedAttemptID,
		ExpectedGeneration: in.ExpectedGeneration,
		ExpectedResourceVersion: in.ExpectedResourceVersion,
		Payload: payload,
	}
	receipt, err := s.acceptAndDispatchControlOperation(r.Context(), r.PathValue("id"), req)
	if err != nil {
		httpError(w, err)
		return
	}
	writeControlReceipt(w, receipt)
}

func (s *Server) httpSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	b, err := readJSONBody(w, r, 16<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var in sessionControlHTTPInput
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid session interrupt JSON"))
		return
	}
	sessionID := r.PathValue("session")
	if sessionID == "" || in.ExpectedAttemptID != sessionID {
		httpError(w, status.Error(codes.InvalidArgument, "session interrupt identity mismatch"))
		return
	}
	req := ControlOperationRequest{
		OperationID: in.OperationID,
		OperationType: "interrupt",
		ResourceType: "session",
		ResourceID: sessionID,
		TaskID: in.TaskID,
		ExpectedAttemptID: in.ExpectedAttemptID,
		ExpectedGeneration: in.ExpectedGeneration,
		ExpectedResourceVersion: in.ExpectedResourceVersion,
	}
	receipt, err := s.acceptAndDispatchControlOperation(r.Context(), r.PathValue("id"), req)
	if err != nil {
		httpError(w, err)
		return
	}
	writeControlReceipt(w, receipt)
}


func (s *Server) httpSessionResume(w http.ResponseWriter, r *http.Request) {
	if err := s.requireControlWriteLease(r, r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	b, err := readJSONBody(w, r, 16<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var in sessionControlHTTPInput
	if err = json.Unmarshal(b, &in); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid session resume JSON"))
		return
	}
	sessionID := r.PathValue("session")
	if sessionID == "" || in.ExpectedAttemptID != sessionID {
		httpError(w, status.Error(codes.InvalidArgument, "session resume identity mismatch"))
		return
	}
	req := ControlOperationRequest{
		OperationID: in.OperationID,
		OperationType: "resume",
		ResourceType: "session",
		ResourceID: sessionID,
		TaskID: in.TaskID,
		ExpectedAttemptID: in.ExpectedAttemptID,
		ExpectedGeneration: in.ExpectedGeneration,
		ExpectedResourceVersion: in.ExpectedResourceVersion,
	}
	receipt, err := s.acceptAndDispatchControlOperation(r.Context(), r.PathValue("id"), req)
	if err != nil {
		httpError(w, err)
		return
	}
	writeControlReceipt(w, receipt)
}
