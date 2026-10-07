package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// containerProvider delegates lifecycle proof to the same authenticated
// controller that owns the Attempt's Docker container. It never assumes that
// a successful Runtime call alone proves container cleanup.
type containerProvider struct{}

var containerIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func (containerProvider) Descriptor() Descriptor {
	return Descriptor{Name: "container", Version: "1", IsolationClass: "container", FilesystemMode: "isolated", NetworkMode: "restricted"}
}

type containerStatus struct {
	State   State        `json:"state"`
	Cleanup CleanupState `json:"cleanup"`
}

func containerRequest(ctx context.Context, ref Ref, method, path string, out any) error {
	if ref.Provider != "container" || !containerIDRE.MatchString(ref.ID) || !strings.HasPrefix(ref.Endpoint, "unix:///") || !filepath.IsAbs(ref.TokenFile) {
		return errors.New("invalid container environment reference")
	}
	socket := strings.TrimPrefix(ref.Endpoint, "unix://")
	if strings.ContainsAny(socket, "?#") {
		return errors.New("invalid container service socket")
	}
	token, err := os.ReadFile(ref.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return errors.New("container service credential unavailable")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent-runtime"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("container service %s: status %d", method, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(out); err != nil {
		return err
	}
	return nil
}

func (containerProvider) Prepare(ctx context.Context, req PrepareRequest, record func(Ref) error) (Prepared, error) {
	if !containerIDRE.MatchString(req.AttemptID) || req.TaskID == "" || req.Generation < 1 || !filepath.IsAbs(req.CWD) ||
		(req.RuntimeProfile != "codex_http" && req.RuntimeProfile != "claude_http") {
		return Prepared{}, errors.New("container environment requires HTTP runtime and valid Attempt workspace")
	}
	ref := Ref{Provider: "container", ID: req.AttemptID, Endpoint: req.RuntimeEndpoint, TokenFile: req.RuntimeTokenFile}
	if record == nil {
		return Prepared{}, errors.New("container environment requires durable reference")
	}
	if err := record(ref); err != nil {
		return Prepared{}, err
	}
	var health struct {
		Profile   string `json:"profile"`
		Isolation string `json:"isolation"`
	}
	if err := containerRequest(ctx, ref, http.MethodGet, "/v1/health?profile="+req.RuntimeProfile, &health); err != nil {
		return Prepared{}, err
	}
	if health.Profile != req.RuntimeProfile || health.Isolation != "container" {
		return Prepared{}, errors.New("container service isolation proof mismatch")
	}
	return Prepared{Ref: ref, CWD: req.CWD, Env: append([]string(nil), req.Env...)}, nil
}

func (containerProvider) Activate(context.Context, Prepared) error { return nil }

func (containerProvider) Inspect(ctx context.Context, ref Ref) (Inspection, error) {
	var status containerStatus
	err := containerRequest(ctx, ref, http.MethodGet, "/v1/environments/"+ref.ID, &status)
	if err != nil || !validContainerStatus(status) {
		if err == nil {
			err = errors.New("invalid container environment status")
		}
		return Inspection{State: StateUnknown, Cleanup: CleanupUnknown}, err
	}
	return Inspection{State: status.State, Cleanup: status.Cleanup}, nil
}

func (containerProvider) Release(ctx context.Context, ref Ref) (ReleaseResult, error) {
	var status containerStatus
	err := containerRequest(ctx, ref, http.MethodDelete, "/v1/environments/"+ref.ID, &status)
	if err != nil || status.State != StateReleased || status.Cleanup != CleanupConfirmed {
		if err == nil {
			err = errors.New("container cleanup unconfirmed")
		}
		return ReleaseResult{State: StateUnknown, Cleanup: CleanupUnknown, Err: err}, err
	}
	return ReleaseResult{State: status.State, Cleanup: status.Cleanup}, nil
}

func validContainerStatus(status containerStatus) bool {
	switch status.State {
	case StatePrepared, StateActive:
		return status.Cleanup == CleanupPending
	case StateReleased:
		return status.Cleanup == CleanupConfirmed
	case StateUnknown:
		return status.Cleanup == CleanupUnknown
	default:
		return false
	}
}

func init() {
	if err := RegisterProvider(containerProvider{}); err != nil {
		panic(err)
	}
}
