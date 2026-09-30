// Package process supervises trusted Linux and macOS subprocess groups. It is not a sandbox.
package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
)

type Result struct {
	Metrics  *telemetry.Process
	ExitCode int
	Cleanup  bool
	TermSent bool
	KillSent bool
	Err      error
}

type StopResult struct {
	Cleanup  bool
	TermSent bool
	KillSent bool
}

type InspectResult struct {
	Running bool
	Exited  bool
	Unknown bool
	Cleanup bool
}

func Inspect(pid int, identity string) InspectResult {
	if pid <= 1 || identity == "" {
		return InspectResult{Unknown: true}
	}
	boot, err := bootIdentity()
	if err != nil {
		return InspectResult{Unknown: true}
	}
	if !strings.HasPrefix(identity, boot+":") {
		// A process identity from another boot cannot still be running.
		return InspectResult{Exited: true, Cleanup: true}
	}
	current, err := Identity(pid)
	if errors.Is(err, os.ErrNotExist) {
		return InspectResult{Exited: true, Cleanup: true}
	}
	if err != nil || current != identity {
		// PID reuse or unreadable process identity is intentionally fail-closed.
		return InspectResult{Unknown: true}
	}
	if groupAlive(pid) {
		return InspectResult{Running: true}
	}
	return InspectResult{Exited: true, Cleanup: true}
}

func StopDetailed(pid int, identity string, grace time.Duration) StopResult {
	if pid <= 1 || identity == "" {
		return StopResult{}
	}
	boot, e := bootIdentity()
	if e != nil {
		return StopResult{}
	}
	if !strings.HasPrefix(identity, boot+":") {
		return StopResult{Cleanup: true}
	}
	current, e := Identity(pid)
	if e == nil && current != identity {
		return StopResult{}
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return StopResult{}
	}
	if !groupAlive(pid) {
		return StopResult{Cleanup: true}
	}
	out := StopResult{TermSent: true}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	end := time.Now().Add(grace)
	for time.Now().Before(end) {
		if !groupAlive(pid) {
			out.Cleanup = true
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	out.KillSent = true
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	end = time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		if !groupAlive(pid) {
			out.Cleanup = true
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	out.Cleanup = !groupAlive(pid)
	return out
}

func Stop(pid int, identity string, grace time.Duration) bool {
	return StopDetailed(pid, identity, grace).Cleanup
}
func Run(ctx context.Context, exe string, args, env []string, cwd string, stdin io.Reader, stdout, stderr io.Writer, grace time.Duration, onStart func(int, string) error) (result Result) {
	started := time.Now()
	var state *os.ProcessState
	defer func() {
		if state != nil {
			m := &telemetry.Process{WallMS: time.Since(started).Milliseconds(), UserCPUMS: state.UserTime().Milliseconds(), SystemCPUMS: state.SystemTime().Milliseconds()}
			if u, ok := state.SysUsage().(*syscall.Rusage); ok {
				m.PeakRSSBytes = u.Maxrss
				if runtime.GOOS == "linux" {
					m.PeakRSSBytes *= 1024
				}
			}
			result.Metrics = m
		}
	}()
	if e := ctx.Err(); e != nil {
		return Result{ExitCode: -1, Cleanup: true, Err: e}
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if e := cmd.Start(); e != nil {
		return Result{ExitCode: -1, Cleanup: true, Err: e}
	}
	id, e := Identity(cmd.Process.Pid)
	if e != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		state = cmd.ProcessState
		return Result{ExitCode: -1, Cleanup: false, Err: e}
	}
	if onStart != nil {
		if e = onStart(cmd.Process.Pid, id); e != nil {
			clean := Stop(cmd.Process.Pid, id, grace)
			_ = cmd.Wait()
			state = cmd.ProcessState
			return Result{ExitCode: -1, Cleanup: clean, Err: e}
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e = <-done:
	case <-ctx.Done():
		stopped := StopDetailed(cmd.Process.Pid, id, grace)
		e = <-done
		state = cmd.ProcessState
		return Result{ExitCode: cmd.ProcessState.ExitCode(), Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: errors.Join(ctx.Err(), e)}
	}
	state = cmd.ProcessState
	stopped := StopDetailed(cmd.Process.Pid, id, grace)
	return Result{ExitCode: cmd.ProcessState.ExitCode(), Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: e}
}
