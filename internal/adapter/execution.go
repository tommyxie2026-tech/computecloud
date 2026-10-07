package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
)

type RuntimeState string

const (
	RuntimeStarting RuntimeState = "STARTING"
	RuntimeRunning  RuntimeState = "RUNNING"
	RuntimeExited   RuntimeState = "EXITED"
	RuntimeUnknown  RuntimeState = "UNKNOWN"
)

type CleanupState string

const (
	CleanupPending   CleanupState = "PENDING"
	CleanupConfirmed CleanupState = "CONFIRMED"
	CleanupUnknown   CleanupState = "UNKNOWN"
)

type ExecutionRef struct {
	Provider  string `json:"provider"`
	Transport string `json:"transport"`
	ID        string `json:"id"`
	PID       int    `json:"pid,omitempty"`
	StartID   string `json:"start_id,omitempty"`
}

type Inspection struct {
	State   RuntimeState
	Cleanup CleanupState
}

type StopResult struct {
	State    RuntimeState
	Cleanup  CleanupState
	TermSent bool
	KillSent bool
	Err      error
}

type PrepareRequest struct {
	AttemptID  string
	Generation int64
	Runtime    config.Runtime
	Spec       *pb.TaskSpec
	Policy     config.Policy
	Gateway    *pb.GatewayAccess
	Env        []string
	CWD        string
	Input      string
	Emit       func(string, []byte) error
	Stderr     io.Writer
	StopGrace  time.Duration
}

type PreparedExecution struct {
	AttemptID  string
	Generation int64
	Profile    string
	Runtime    config.Runtime
	Args       []string
	Env        []string
	CWD        string
	Input      string
	Emit       func(string, []byte) error
	Stderr     io.Writer
	StopGrace  time.Duration
	Sensitive  []string
}

type StartResult struct {
	Metrics     *telemetry.Process
	Ref         ExecutionRef
	State       RuntimeState
	Cleanup     CleanupState
	ExitCode    int
	TermSent    bool
	KillSent    bool
	Outcome     Outcome
	ProtocolErr error
	Err         error
}

func EncodeExecutionRef(ref ExecutionRef) ([]byte, error) {
	if strings.TrimSpace(ref.Provider) == "" || strings.TrimSpace(ref.Transport) == "" || strings.TrimSpace(ref.ID) == "" {
		return nil, errors.New("runtime execution reference is incomplete")
	}
	return json.Marshal(ref)
}

func DecodeExecutionRef(b []byte) (ExecutionRef, error) {
	var ref ExecutionRef
	if len(b) == 0 {
		return ref, errors.New("runtime execution reference is empty")
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		return ref, err
	}
	if _, err := EncodeExecutionRef(ref); err != nil {
		return ExecutionRef{}, err
	}
	return ref, nil
}

func copyEnv(in []string) []string {
	return append([]string(nil), in...)
}

func dropEnv(env []string, keys ...string) []string {
	blocked := map[string]bool{}
	for _, key := range keys {
		blocked[key] = true
	}
	out := env[:0]
	for _, item := range env {
		key := item
		if i := strings.IndexByte(item, '='); i >= 0 {
			key = item[:i]
		}
		if !blocked[key] {
			out = append(out, item)
		}
	}
	return out
}

func prepareCLI(p Provider, req PrepareRequest) (PreparedExecution, error) {
	if req.Spec == nil {
		return PreparedExecution{}, errors.New("runtime task spec required")
	}
	args, err := p.Args(req.Spec, req.Policy)
	if err != nil {
		return PreparedExecution{}, err
	}
	return PreparedExecution{
		AttemptID:  req.AttemptID,
		Generation: req.Generation,
		Profile:    p.Profile(),
		Runtime:    req.Runtime,
		Args:       append([]string(nil), args...),
		Env:        copyEnv(req.Env),
		CWD:        req.CWD,
		Input:      req.Input,
		Emit:       req.Emit,
		Stderr:     req.Stderr,
		StopGrace:  req.StopGrace,
	}, nil
}

func (p codexProvider) Version(r config.Runtime) string { return r.Version }
func (p codexProvider) Transport() string               { return "local_cli" }
func (p codexProvider) Prepare(req PrepareRequest) (PreparedExecution, error) {
	prepared, err := prepareCLI(p, req)
	if err != nil {
		return PreparedExecution{}, err
	}
	if req.Gateway == nil {
		return prepared, nil
	}
	if req.Gateway.Token == "" || req.Gateway.BaseUrl == "" {
		return PreparedExecution{}, errors.New("gateway token/base_url required")
	}
	prepared.Env = dropEnv(prepared.Env, "COMPUTECLOUD_MODEL_TOKEN", "OPENAI_API_KEY")
	prepared.Env = append(prepared.Env, "COMPUTECLOUD_MODEL_TOKEN="+req.Gateway.Token)
	prepared.Sensitive = append(prepared.Sensitive, req.Gateway.Token)
	if n := len(prepared.Args); n == 0 || prepared.Args[n-1] != "-" {
		return PreparedExecution{}, errors.New("codex stdin argument missing")
	}
	prepared.Args = prepared.Args[:len(prepared.Args)-1]
	for _, setting := range []string{
		`model_provider="computecloud"`,
		`model_providers.computecloud.name="computecloud"`,
		"model_providers.computecloud.base_url=" + strconv.Quote(req.Gateway.BaseUrl),
		`model_providers.computecloud.env_key="COMPUTECLOUD_MODEL_TOKEN"`,
		`model_providers.computecloud.wire_api="responses"`,
		`model_providers.computecloud.requires_openai_auth=false`,
		`model_providers.computecloud.request_max_retries=0`,
		`model_providers.computecloud.stream_max_retries=0`,
	} {
		prepared.Args = append(prepared.Args, "-c", setting)
	}
	prepared.Args = append(prepared.Args, "-")
	return prepared, nil
}
func (p codexProvider) Start(ctx context.Context, prepared PreparedExecution, started func(ExecutionRef) error) StartResult {
	return startLocalCLI(ctx, p, prepared, started)
}
func (p codexProvider) Inspect(_ context.Context, _ config.Runtime, ref ExecutionRef) (Inspection, error) {
	return inspectLocal(ref), nil
}
func (p codexProvider) Stop(_ context.Context, _ config.Runtime, ref ExecutionRef, grace time.Duration) (StopResult, error) {
	return stopLocal(ref, grace), nil
}

func (p claudeProvider) Version(r config.Runtime) string { return r.Version }
func (p claudeProvider) Transport() string               { return "local_cli" }
func (p claudeProvider) Prepare(req PrepareRequest) (PreparedExecution, error) {
	if req.Gateway != nil {
		return PreparedExecution{}, errors.New("runtime provider does not support model gateway")
	}
	return prepareCLI(p, req)
}
func (p claudeProvider) Start(ctx context.Context, prepared PreparedExecution, started func(ExecutionRef) error) StartResult {
	return startLocalCLI(ctx, p, prepared, started)
}
func (p claudeProvider) Inspect(_ context.Context, _ config.Runtime, ref ExecutionRef) (Inspection, error) {
	return inspectLocal(ref), nil
}
func (p claudeProvider) Stop(_ context.Context, _ config.Runtime, ref ExecutionRef, grace time.Duration) (StopResult, error) {
	return stopLocal(ref, grace), nil
}

func (p geminiProvider) Version(r config.Runtime) string { return r.Version }
func (p geminiProvider) Transport() string               { return "local_cli" }
func (p geminiProvider) Prepare(req PrepareRequest) (PreparedExecution, error) {
	if req.Gateway != nil {
		return PreparedExecution{}, errors.New("runtime provider does not support model gateway")
	}
	prepared, err := prepareCLI(p, req)
	if err != nil {
		return PreparedExecution{}, err
	}
	if strings.TrimSpace(req.Input) == "" {
		return PreparedExecution{}, errors.New("gemini prompt required")
	}
	prepared.Args = append(prepared.Args, "--prompt", req.Input)
	prepared.Input = ""
	return prepared, nil
}
func (p geminiProvider) Start(ctx context.Context, prepared PreparedExecution, started func(ExecutionRef) error) StartResult {
	return startLocalCLI(ctx, p, prepared, started)
}
func (p geminiProvider) Inspect(_ context.Context, _ config.Runtime, ref ExecutionRef) (Inspection, error) {
	return inspectLocal(ref), nil
}
func (p geminiProvider) Stop(_ context.Context, _ config.Runtime, ref ExecutionRef, grace time.Duration) (StopResult, error) {
	return stopLocal(ref, grace), nil
}

func startLocalCLI(ctx context.Context, p Provider, prepared PreparedExecution, started func(ExecutionRef) error) StartResult {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	parser := p.Parser(prepared.Emit)
	lines := &Lines{Limit: 4 << 20, OnLine: parser.Line, OnError: cancel}
	var ref ExecutionRef
	run := process.Run(runCtx, prepared.Runtime.Executable, prepared.Args, prepared.Env, prepared.CWD,
		strings.NewReader(prepared.Input), lines, prepared.Stderr, prepared.StopGrace,
		func(pid int, startID string) error {
			ref = ExecutionRef{
				Provider: p.Profile(), Transport: p.Transport(),
				ID: fmt.Sprintf("%d:%s", pid, startID), PID: pid, StartID: startID,
			}
			if started != nil {
				return started(ref)
			}
			return nil
		})
	protocolErr := lines.Flush()
	cleanup := CleanupUnknown
	if run.Cleanup {
		cleanup = CleanupConfirmed
	}
	if ref.ID == "" {
		ref = ExecutionRef{Provider: p.Profile(), Transport: p.Transport(), ID: "not-started"}
	}
	return StartResult{
		Metrics: run.Metrics, Ref: ref, State: RuntimeExited, Cleanup: cleanup, ExitCode: run.ExitCode,
		TermSent: run.TermSent, KillSent: run.KillSent, Outcome: parser.Outcome(),
		ProtocolErr: protocolErr, Err: run.Err,
	}
}

func inspectLocal(ref ExecutionRef) Inspection {
	if ref.Transport != "local_cli" {
		return Inspection{State: RuntimeUnknown, Cleanup: CleanupUnknown}
	}
	got := process.Inspect(ref.PID, ref.StartID)
	switch {
	case got.Running:
		return Inspection{State: RuntimeRunning, Cleanup: CleanupPending}
	case got.Exited && got.Cleanup:
		return Inspection{State: RuntimeExited, Cleanup: CleanupConfirmed}
	default:
		return Inspection{State: RuntimeUnknown, Cleanup: CleanupUnknown}
	}
}

func stopLocal(ref ExecutionRef, grace time.Duration) StopResult {
	if ref.Transport != "local_cli" {
		return StopResult{State: RuntimeUnknown, Cleanup: CleanupUnknown}
	}
	got := process.StopDetailed(ref.PID, ref.StartID, grace)
	if got.Cleanup {
		return StopResult{
			State: RuntimeExited, Cleanup: CleanupConfirmed,
			TermSent: got.TermSent, KillSent: got.KillSent,
		}
	}
	return StopResult{
		State: RuntimeUnknown, Cleanup: CleanupUnknown,
		TermSent: got.TermSent, KillSent: got.KillSent,
	}
}
