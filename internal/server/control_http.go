package server

import (
	"encoding/json"
	"net/http"

	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type controlHTTPWriteRequest struct {
	OperationID             string          `json:"operation_id"`
	TaskID                  string          `json:"task_id"`
	ExpectedAttemptID       string          `json:"expected_attempt_id"`
	ExpectedGeneration      int64           `json:"expected_generation"`
	ExpectedResourceVersion int64           `json:"expected_resource_version"`
	Payload                 json.RawMessage `json:"payload,omitempty"`
}

func (s *Server) httpControlWrite(w http.ResponseWriter, r *http.Request, operationType, resourceType, resourceID string) {
	body, err := readJSONBody(w, r, 64<<10)
	if err != nil {
		httpError(w, err)
		return
	}
	var wire controlHTTPWriteRequest
	if err = jsonutil.Decode(body, &wire); err != nil {
		httpError(w, status.Error(codes.InvalidArgument, "invalid control operation JSON"))
		return
	}
	in := ControlOperationRequest{
		OperationID: wire.OperationID,
		OperationType: operationType,
		ResourceType: resourceType,
		ResourceID: resourceID,
		TaskID: wire.TaskID,
		ExpectedAttemptID: wire.ExpectedAttemptID,
		ExpectedGeneration: wire.ExpectedGeneration,
		ExpectedResourceVersion: wire.ExpectedResourceVersion,
		Payload: wire.Payload,
	}
	if resourceType == "session" && resourceID != wire.ExpectedAttemptID {
		httpError(w, status.Error(codes.InvalidArgument, "session path does not match expected attempt"))
		return
	}
	receipt, err := s.acceptAndDispatchControlOperation(r.Context(), r.PathValue("id"), in)
	if err != nil {
		httpError(w, err)
		return
	}
	code := http.StatusAccepted
	if receipt.Existing && (receipt.State == "COMPLETED" || receipt.State == "REJECTED") {
		code = http.StatusOK
	}
	jsonResponse(w, code, receipt)
}

func (s *Server) httpSessionInput(w http.ResponseWriter, r *http.Request) {
	s.httpControlWrite(w, r, "input", "session", r.PathValue("session"))
}

func (s *Server) httpSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	s.httpControlWrite(w, r, "interrupt", "session", r.PathValue("session"))
}

func (s *Server) httpSessionResume(w http.ResponseWriter, r *http.Request) {
	s.httpControlWrite(w, r, "resume", "session", r.PathValue("session"))
}

func (s *Server) httpApprovalDecision(w http.ResponseWriter, r *http.Request) {
	s.httpControlWrite(w, r, "approval", "approval", r.PathValue("approval"))
}
