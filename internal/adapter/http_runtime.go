package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

// The HTTP runtime contract is intentionally narrow: one Attempt creates one
// durable run, which can subsequently be inspected and stopped by its ID.
// The service owns process/container identity; the Worker never persists a PID.
type httpRuntimeProvider struct {
	profile string
	cli     Provider
}

type httpRunRequest struct {
	AttemptID  string   `json:"attempt_id"`
	Generation int64    `json:"generation"`
	Profile    string   `json:"profile"`
	Version    string   `json:"version"`
	Args       []string `json:"args"`
	Env        []string `json:"env"`
	CWD        string   `json:"cwd"`
	Input      string   `json:"input"`
}

type httpRunEvent struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

type httpRunStatus struct {
	ID       string         `json:"id"`
	State    RuntimeState   `json:"state"`
	Cleanup  CleanupState   `json:"cleanup"`
	ExitCode int            `json:"exit_code"`
	Outcome  Outcome        `json:"outcome"`
	Events   []httpRunEvent `json:"events"`
	HasMore  bool           `json:"has_more"`
}

func (p httpRuntimeProvider) Profile() string                 { return p.profile }
func (p httpRuntimeProvider) Version(r config.Runtime) string { return r.Version }
func (p httpRuntimeProvider) Transport() string               { return "remote_api" }
func (p httpRuntimeProvider) Args(spec *pb.TaskSpec, policy config.Policy) ([]string, error) {
	return p.cli.Args(spec, policy)
}
func (p httpRuntimeProvider) Parser(emit func(string, []byte) error) StreamParser {
	return p.cli.Parser(emit)
}
func (p httpRuntimeProvider) SupportsGateway() bool { return false }
func (p httpRuntimeProvider) Capabilities() CapabilitySet {
	return CapabilitySet{
		Runtime:     []string{"event_stream", "cancel", "remote_api"},
		Tools:       []string{"job_io_v1", "artifact_inputs_v1"},
		Environment: []string{"process", "container"},
		Legacy:      []string{"event_stream", "cancel", "job_io_v1", "artifact_inputs_v1"},
	}
}

func httpClient(r config.Runtime) (*http.Client, string, error) {
	if !strings.HasPrefix(r.Endpoint, "unix:///") || r.TokenFile == "" || r.Version == "" {
		return nil, "", errors.New("HTTP runtime requires absolute unix endpoint, token_file and version")
	}
	socket := strings.TrimPrefix(r.Endpoint, "unix://")
	if strings.ContainsAny(socket, "?#") {
		return nil, "", errors.New("HTTP runtime socket path must not contain query or fragment")
	}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}, Timeout: 45 * time.Second}, socket, nil
}

var newHTTPClient = httpClient

func httpRequest(ctx context.Context, r config.Runtime, method, path string, in, out any) error {
	client, _, err := newHTTPClient(r)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	token, err := os.ReadFile(r.TokenFile)
	if err != nil {
		return fmt.Errorf("HTTP runtime token: %w", err)
	}
	if len(bytes.TrimSpace(token)) == 0 {
		return errors.New("HTTP runtime token is empty")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if len(b) > 4<<20 {
			return errors.New("HTTP runtime request exceeds 4 MiB")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent-runtime"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP runtime %s: status %d", method, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}

func (p httpRuntimeProvider) Probe(ctx context.Context, r config.Runtime) error {
	var health struct {
		Profile   string `json:"profile"`
		Version   string `json:"version"`
		Isolation string `json:"isolation"`
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := httpRequest(probeCtx, r, http.MethodGet, "/v1/health?profile="+url.QueryEscape(p.profile), nil, &health); err != nil {
		return err
	}
	if health.Profile != p.profile || health.Version != r.Version || health.Isolation != "container" {
		return fmt.Errorf("HTTP runtime identity mismatch: got %q %q", health.Profile, health.Version)
	}
	return nil
}

func (p httpRuntimeProvider) Prepare(req PrepareRequest) (PreparedExecution, error) {
	if req.Gateway != nil {
		return PreparedExecution{}, errors.New("HTTP runtime does not support model gateway")
	}
	if _, _, err := httpClient(req.Runtime); err != nil {
		return PreparedExecution{}, err
	}
	prepared, err := prepareCLI(p, req)
	if err != nil {
		return PreparedExecution{}, err
	}
	if prepared.CWD == "" || prepared.Input == "" || prepared.AttemptID == "" || prepared.Generation < 1 {
		return PreparedExecution{}, errors.New("HTTP runtime requires attempt identity, workspace and input")
	}
	return prepared, nil
}

func (p httpRuntimeProvider) runStatus(ctx context.Context, r config.Runtime, id string, after int64) (httpRunStatus, error) {
	var status httpRunStatus
	if id == "" {
		return status, errors.New("HTTP runtime run ID required")
	}
	err := httpRequest(ctx, r, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"?after="+strconv.FormatInt(after, 10), nil, &status)
	if err == nil && status.ID != id {
		err = errors.New("HTTP runtime run ID mismatch")
	}
	return status, err
}

func (p httpRuntimeProvider) Start(ctx context.Context, prepared PreparedExecution, started func(ExecutionRef) error) StartResult {
	ref := ExecutionRef{Provider: p.profile, Transport: p.Transport(), ID: prepared.AttemptID}
	abort := func(err error, protocol bool) StartResult {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopped, stopErr := p.Stop(stopCtx, prepared.Runtime, ref, prepared.StopGrace)
		result := StartResult{Ref: ref, State: stopped.State, Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: stopErr}
		if protocol {
			result.ProtocolErr = err
		} else {
			result.Err = errors.Join(err, stopErr)
		}
		return result
	}
	if started != nil {
		if err := started(ref); err != nil {
			return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: err}
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	err := httpRequest(ctx, prepared.Runtime, http.MethodPut, "/v1/runs/"+url.PathEscape(ref.ID), httpRunRequest{
		AttemptID: prepared.AttemptID, Generation: prepared.Generation,
		Profile: p.profile, Version: prepared.Runtime.Version, Args: prepared.Args,
		Env: prepared.Env, CWD: prepared.CWD, Input: prepared.Input,
	}, &created)
	if err != nil {
		return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: err}
	}
	if created.ID != ref.ID {
		return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: errors.New("HTTP runtime returned different run ID")}
	}
	var sequence int64
	for {
		status, err := p.runStatus(ctx, prepared.Runtime, ref.ID, sequence)
		if err != nil {
			if ctx.Err() != nil {
				stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				stopped, stopErr := p.Stop(stopCtx, prepared.Runtime, ref, prepared.StopGrace)
				cancel()
				return StartResult{Ref: ref, State: stopped.State, Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: errors.Join(ctx.Err(), stopErr)}
			}
			return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: err}
		}
		for _, event := range status.Events {
			if event.Sequence <= sequence {
				continue
			}
			if event.Sequence != sequence+1 || event.Kind == "" || len(event.Payload) == 0 || !json.Valid(event.Payload) {
				return abort(errors.New("HTTP runtime event sequence or payload invalid"), true)
			}
			if prepared.Emit != nil {
				if err := prepared.Emit(event.Kind, event.Payload); err != nil {
					return abort(err, false)
				}
			}
			sequence = event.Sequence
		}
		switch status.State {
		case RuntimeExited:
			if status.HasMore {
				continue
			}
			return StartResult{Ref: ref, State: status.State, Cleanup: status.Cleanup, ExitCode: status.ExitCode, Outcome: status.Outcome}
		case RuntimeStarting, RuntimeRunning:
		case RuntimeUnknown:
			return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, Err: errors.New("HTTP runtime state unknown")}
		default:
			return StartResult{Ref: ref, State: RuntimeUnknown, Cleanup: CleanupUnknown, ProtocolErr: errors.New("HTTP runtime returned invalid state")}
		}
		select {
		case <-ctx.Done():
			continue
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (p httpRuntimeProvider) Inspect(ctx context.Context, r config.Runtime, ref ExecutionRef) (Inspection, error) {
	if ref.Provider != p.profile || ref.Transport != p.Transport() {
		return Inspection{State: RuntimeUnknown, Cleanup: CleanupUnknown}, errors.New("HTTP runtime reference mismatch")
	}
	status, err := p.runStatus(ctx, r, ref.ID, 0)
	if err != nil {
		return Inspection{State: RuntimeUnknown, Cleanup: CleanupUnknown}, err
	}
	return Inspection{State: status.State, Cleanup: status.Cleanup}, nil
}

func (p httpRuntimeProvider) Stop(ctx context.Context, r config.Runtime, ref ExecutionRef, grace time.Duration) (StopResult, error) {
	if ref.Provider != p.profile || ref.Transport != p.Transport() || ref.ID == "" {
		return StopResult{State: RuntimeUnknown, Cleanup: CleanupUnknown}, errors.New("HTTP runtime reference mismatch")
	}
	var response struct {
		State    RuntimeState `json:"state"`
		Cleanup  CleanupState `json:"cleanup"`
		TermSent bool         `json:"term_sent"`
		KillSent bool         `json:"kill_sent"`
	}
	err := httpRequest(ctx, r, http.MethodDelete, "/v1/runs/"+url.PathEscape(ref.ID), map[string]int64{"grace_ms": grace.Milliseconds()}, &response)
	if err != nil {
		return StopResult{State: RuntimeUnknown, Cleanup: CleanupUnknown}, err
	}
	return StopResult{State: response.State, Cleanup: response.Cleanup, TermSent: response.TermSent, KillSent: response.KillSent}, nil
}

func init() {
	for _, p := range []Provider{
		httpRuntimeProvider{profile: "codex_http", cli: codexProvider{}},
		httpRuntimeProvider{profile: "claude_http", cli: claudeProvider{}},
	} {
		if err := Register(p); err != nil {
			panic(err)
		}
	}
}
