// Package process supervises trusted Linux subprocess groups. It is not a sandbox.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Result struct {
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

func Identity(pid int) (string, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", errors.New("invalid process stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return "", errors.New("short process stat")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(boot)) + ":" + f[19], nil
}
func groupAlive(pgid int) bool {
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return true
	}
	for _, v := range entries {
		pid, e := strconv.Atoi(v.Name())
		if e != nil {
			continue
		}
		b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if e != nil {
			continue
		}
		i := strings.LastIndexByte(string(b), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) > 2 && f[0] != "Z" && f[0] != "X" && f[2] == strconv.Itoa(pgid) {
			return true
		}
	}
	return false
}
func StopDetailed(pid int, identity string, grace time.Duration) StopResult {
	if pid <= 1 || identity == "" {
		return StopResult{}
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return StopResult{}
	}
	if !strings.HasPrefix(identity, strings.TrimSpace(string(boot))+":") {
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
func Run(ctx context.Context, exe string, args, env []string, cwd string, stdin io.Reader, stdout, stderr io.Writer, grace time.Duration, onStart func(int, string) error) Result {
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
		return Result{ExitCode: -1, Cleanup: false, Err: e}
	}
	if onStart != nil {
		if e = onStart(cmd.Process.Pid, id); e != nil {
			clean := Stop(cmd.Process.Pid, id, grace)
			_ = cmd.Wait()
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
		return Result{ExitCode: cmd.ProcessState.ExitCode(), Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: errors.Join(ctx.Err(), e)}
	}
	stopped := StopDetailed(cmd.Process.Pid, id, grace)
	return Result{ExitCode: cmd.ProcessState.ExitCode(), Cleanup: stopped.Cleanup, TermSent: stopped.TermSent, KillSent: stopped.KillSent, Err: e}
}
