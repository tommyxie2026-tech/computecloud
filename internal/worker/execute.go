package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
)

type capped struct {
	mu        sync.Mutex
	b         bytes.Buffer
	limit     int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	left := c.limit - c.b.Len()
	if left < n {
		c.truncated = true
	}
	if left > 0 {
		if left > n {
			left = n
		}
		c.b.Write(p[:left])
	}
	return n, nil
}
func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.b.Bytes()...)
}
func runtimeEnv(r config.Runtime, credential string) ([]string, []string, error) {
	// Only operator-configured variables augment a small inherited runtime environment.
	env := map[string]string{}
	for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range r.Env {
		env[k] = v
	}
	var secrets []string
	for k, file := range r.CredentialEnv[credential] {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, nil, e
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return nil, nil, errors.New("empty credential file")
		}
		env[k] = v
		secrets = append(secrets, v)
	}
	out := make([]string, 0, len(env))
	for _, k := range keys(env) {
		out = append(out, k+"="+env[k])
	}
	return out, secrets, nil
}
func redact(b []byte, secrets []string) []byte {
	for _, s := range secrets {
		b = bytes.ReplaceAll(b, []byte(s), []byte("[REDACTED]"))
	}
	return b
}
func (w *Worker) execute(parent context.Context, a *pb.Assignment) {
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(a.DeadlineMs))
	defer cancel()
	persistCtx := context.Background()
	complete := func(c *pb.CompleteRequest) {
		if e := w.completion(persistCtx, a, c); e != nil {
			slog.Error("persist completion failed", "attempt", a.AttemptId, "error", e)
		}
	}
	res, e := w.db.SQL.ExecContext(ctx, "UPDATE runs SET state='STARTING' WHERE id=? AND state='ACCEPTED' AND stop=0 AND completion IS NULL", a.AttemptId)
	if e != nil {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "STORAGE_UNAVAILABLE"})
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return
	}
	if a.Spec == nil || a.Spec.Workspace == nil {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "INVALID_SPEC"})
		return
	}
	r, ok := w.cfg.Runtimes[a.Spec.RuntimeProfile]
	policy, policyOK := w.cfg.Policies[a.Spec.PolicyRef]
	verify, verifyOK := w.cfg.Verifiers[a.Spec.AcceptanceProfile]
	if !ok || !policyOK || !verifyOK || !config.Contains(r.Models, a.Spec.Model) || !config.Contains(r.Credentials, a.Spec.CredentialRef) {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "CAPABILITY_UNAVAILABLE"})
		return
	}
	args, e := adapter.Args(a.Spec, policy)
	if e != nil {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "INVALID_POLICY", ErrorMessage: e.Error()})
		return
	}
	runtimeConfig := r
	if a.Gateway != nil {
		runtimeConfig.CredentialEnv = nil
	}
	env, secrets, e := runtimeEnv(runtimeConfig, a.Spec.CredentialRef)
	if e != nil {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "AUTHENTICATION_REQUIRED", ErrorMessage: "credential file unavailable"})
		return
	}
	if a.Gateway != nil {
		if a.Spec.RuntimeProfile != "codex_exec" || a.Gateway.Token == "" || a.Gateway.BaseUrl == "" {
			complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "INVALID_GATEWAY"})
			return
		}
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], "COMPUTECLOUD_MODEL_TOKEN=") || strings.HasPrefix(env[i], "OPENAI_API_KEY=") {
				env = append(env[:i], env[i+1:]...)
			}
		}
		env = append(env, "COMPUTECLOUD_MODEL_TOKEN="+a.Gateway.Token)
		secrets = append(secrets, a.Gateway.Token)
		args = args[:len(args)-1]
		for _, setting := range []string{`model_provider="computecloud"`, `model_providers.computecloud.name="computecloud"`, "model_providers.computecloud.base_url=" + strconv.Quote(a.Gateway.BaseUrl), `model_providers.computecloud.env_key="COMPUTECLOUD_MODEL_TOKEN"`, `model_providers.computecloud.wire_api="responses"`, `model_providers.computecloud.requires_openai_auth=false`, `model_providers.computecloud.request_max_retries=0`, `model_providers.computecloud.stream_max_retries=0`} {
			args = append(args, "-c", setting)
		}
		args = append(args, "-")
	}
	cwd, e := w.prepareWorkspace(ctx, a)
	if e != nil {
		code := "WORKSPACE_ERROR"
		if errors.Is(e, workspace.ErrQuotaExceeded) {
			code = "WORKSPACE_QUOTA_EXCEEDED"
		}
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: code, ErrorMessage: e.Error()})
		return
	}
	if e = w.markWorkspaceInUse(ctx, a); e != nil {
		complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: "WORKSPACE_ERROR", ErrorMessage: e.Error()})
		return
	}

	execCtx, execCancel := context.WithCancel(ctx)
	var quotaMu sync.Mutex
	var quotaErr error
	quotaDone := make(chan struct{})
	go func() {
		defer close(quotaDone)
		if qe, ok := <-workspace.WatchQuota(execCtx, cwd, w.cfg.WorkspaceMaxBytes, 2*time.Second); ok {
			quotaMu.Lock()
			quotaErr = qe
			quotaMu.Unlock()
			execCancel()
		}
	}()
	var quotaOnce sync.Once
	var finalQuotaErr error
	stopQuota := func() error {
		quotaOnce.Do(func() {
			execCancel()
			<-quotaDone
			quotaMu.Lock()
			finalQuotaErr = quotaErr
			quotaMu.Unlock()
			if finalQuotaErr == nil {
				_, finalQuotaErr = workspace.CheckQuota(cwd, w.cfg.WorkspaceMaxBytes)
			}
		})
		return finalQuotaErr
	}
	defer stopQuota()

	jobRun, e := w.prepareJob(execCtx, a, cwd)
	if e != nil {
		if qe := stopQuota(); qe != nil {
			code := "WORKSPACE_ERROR"
			if errors.Is(qe, workspace.ErrQuotaExceeded) {
				code = "WORKSPACE_QUOTA_EXCEEDED"
			}
			complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: code, ErrorMessage: qe.Error()})
		} else {
			complete(&pb.CompleteRequest{CleanupConfirmed: true, ErrorCode: jobInputCode(e), ErrorMessage: e.Error()})
		}
		return
	}
	parser := &adapter.Parser{Profile: a.Spec.RuntimeProfile, Emit: func(kind string, b []byte) error { return w.emit(persistCtx, a, kind, redact(b, secrets)) }}
	lines := &adapter.Lines{Limit: 4 << 20, OnLine: parser.Line, OnError: cancel}
	stderr := &capped{limit: 1 << 20}
	run := process.Run(execCtx, r.Executable, args, env, cwd, strings.NewReader(jobRun.prompt), lines, stderr, time.Duration(w.cfg.StopGraceMS)*time.Millisecond, func(pid int, id string) error {
		if _, e := w.db.SQL.ExecContext(persistCtx, "UPDATE runs SET state='RUNNING',pid=?,start_id=? WHERE id=?", pid, id, a.AttemptId); e != nil {
			return e
		}
		return w.emit(persistCtx, a, "attempt.started", config.JSON(map[string]any{"runtime_version": r.Version, "workspace": a.AttemptId, "model": a.Spec.Model}))
	})
	parseErr := lines.Flush()
	out := parser.Outcome()
	success := run.Err == nil && run.ExitCode == 0 && run.Cleanup && parseErr == nil && out.Final && out.Success
	code, msg := "", ""
	if !success {
		code = choose(out.Code, "RUNTIME_FAILED")
		msg = "runtime failed or did not produce a successful final event"
		if !out.Final {
			code = "MISSING_FINAL"
		}
		if parseErr != nil {
			code = "PROTOCOL_ERROR"
			msg = parseErr.Error()
		}
		if ctx.Err() != nil {
			code = "CANCELED"
			msg = "execution stopped"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = "DEADLINE_EXCEEDED"
			}
		}
		if !run.Cleanup {
			code = "CLEANUP_UNCONFIRMED"
		}
	}
	verification := []map[string]any{}
	for _, cmd := range verify {
		if !success {
			break
		}
		if len(cmd) == 0 {
			success = false
			code = "INVALID_VERIFIER"
			break
		}
		log := &capped{limit: 1 << 20}
		// Fence the next spawn window before replacing the persisted process identity.
		if _, e = w.db.SQL.ExecContext(persistCtx, "UPDATE runs SET state='STARTING',pid=0,start_id='' WHERE id=?", a.AttemptId); e != nil {
			success = false
			code = "STORAGE_UNAVAILABLE"
			break
		}
		// Verification uses the trusted operator command, without credential injection.
		vr := process.Run(execCtx, cmd[0], cmd[1:], verificationEnv(), cwd, nil, log, log, time.Duration(w.cfg.StopGraceMS)*time.Millisecond, func(pid int, id string) error {
			_, e := w.db.SQL.ExecContext(persistCtx, "UPDATE runs SET state='RUNNING',pid=?,start_id=? WHERE id=?", pid, id, a.AttemptId)
			return e
		})
		verification = append(verification, map[string]any{"command": cmd, "exit_code": vr.ExitCode, "cleanup": vr.Cleanup, "output": string(redact(log.Bytes(), secrets)), "truncated": log.truncated})
		if vr.Err != nil || vr.ExitCode != 0 || !vr.Cleanup {
			success = false
			code = "VERIFICATION_FAILED"
			msg = "trusted verification failed"
		}
		run.Cleanup = run.Cleanup && vr.Cleanup
	}
	diffCtx, diffCancel := context.WithTimeout(execCtx, 30*time.Second)
	diff, diffErr := workspace.Diff(diffCtx, cwd, a.Spec.Workspace.BaseCommit)
	diffCancel()
	if diffErr != nil {
		success = false
		code = "ARTIFACT_ERROR"
		msg = diffErr.Error()
	}
	files := map[string][]byte{}
	if success {
		extra, je := finishJobOutput(execCtx, a, cwd, string(redact([]byte(out.Result), secrets)), diff, jobRun, verification)
		if je != nil {
			success = false
			code = "JOB_OUTPUT_INVALID"
			msg = je.Error()
		} else {
			for name, b := range extra {
				files[name] = redact(b, secrets)
			}
		}
	}
	if a.Job != nil && !bytes.Equal(diff, redact(diff, secrets)) {
		success = false
		code = "SENSITIVE_PATCH"
		msg = "patch contained injected credential"
	}
	if qe := stopQuota(); qe != nil {
		success = false
		code = "WORKSPACE_ERROR"
		if errors.Is(qe, workspace.ErrQuotaExceeded) {
			code = "WORKSPACE_QUOTA_EXCEEDED"
		}
		msg = qe.Error()
	}
	report := config.JSON(map[string]any{"task_id": a.TaskId, "attempt_id": a.AttemptId, "base_commit": a.Spec.Workspace.BaseCommit, "model": a.Spec.Model, "runtime_version": r.Version, "template_digest": a.GetJob().GetTemplateDigest(), "native_final": out.Final, "native_success": out.Success, "exit_code": run.ExitCode, "cleanup_confirmed": run.Cleanup, "verification": verification, "result": string(redact([]byte(out.Result), secrets)), "stderr_truncated": stderr.truncated, "manifest_sha256": a.GetJob().GetInputManifestSha256()})
	files["report.json"] = report
	files["changes.patch"] = redact(diff, secrets)
	files["stderr.log"] = redact(stderr.Bytes(), secrets)
	artifact, e := w.bundle(a, files)
	var ids []string
	if e != nil {
		success = false
		code = "ARTIFACT_ERROR"
		msg = "could not persist result bundle"
	} else {
		ids = []string{artifact.ArtifactId}
	}
	complete(&pb.CompleteRequest{Success: success, CleanupConfirmed: run.Cleanup, ErrorCode: code, ErrorMessage: msg, Result: string(redact([]byte(out.Result), secrets)), NativeSessionId: out.Session, ArtifactIds: ids})
}
func choose(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func (w *Worker) bundle(a *pb.Assignment, files map[string][]byte) (*pb.Artifact, error) {
	dir := filepath.Join(w.cfg.DataDir, "artifacts")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	id := store.ID()
	f, e := os.OpenFile(filepath.Join(dir, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	h := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(f, h))
	for _, name := range keys(files) {
		b := files[name]
		if e = tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(b)), Mode: 0600}); e != nil {
			f.Close()
			return nil, e
		}
		if _, e = tw.Write(b); e != nil {
			f.Close()
			return nil, e
		}
	}
	if e = tw.Close(); e != nil {
		f.Close()
		return nil, e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return nil, e
	}
	info, e := f.Stat()
	f.Close()
	if e != nil {
		return nil, e
	}
	if info.Size() > 32<<20 {
		return nil, fmt.Errorf("artifact exceeds 32 MiB limit")
	}
	meta := &pb.Artifact{ArtifactId: id, TaskId: a.TaskId, AttemptId: a.AttemptId, Kind: "result-bundle", Sha256: hex.EncodeToString(h.Sum(nil)), Size: info.Size()}
	if e = atomicFile(filepath.Join(dir, id+".json"), enc(meta)); e != nil {
		return nil, e
	}
	return meta, nil
}
func atomicFile(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func verificationEnv() []string { env, _, _ := runtimeEnv(config.Runtime{}, ""); return env }
