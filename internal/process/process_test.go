package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestProcessStopEscalatesToKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := Run(ctx, "python3", []string{"-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"}, os.Environ(), "", nil, io.Discard, io.Discard, 50*time.Millisecond, func(int, string) error {
		// Let the Python fixture install its SIGTERM handler before canceling.
		time.Sleep(200 * time.Millisecond)
		cancel()
		return nil
	})
	if !result.TermSent || !result.KillSent || !result.Cleanup {
		t.Fatalf("escalation evidence: %+v", result)
	}
	if result.Err == nil {
		t.Fatal("canceled process returned nil error")
	}
}

func TestProcessInspectTracksIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	id, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	running := Inspect(cmd.Process.Pid, id)
	if !running.Running || running.Cleanup || running.Unknown {
		t.Fatalf("running inspect=%+v", running)
	}
	if got := Inspect(cmd.Process.Pid, id+"-wrong"); !got.Unknown || got.Cleanup {
		t.Fatalf("identity mismatch was not fail-closed: %+v", got)
	}
	stopped := StopDetailed(cmd.Process.Pid, id, 50*time.Millisecond)
	if !stopped.Cleanup {
		t.Fatalf("cleanup failed: %+v", stopped)
	}
	exited := Inspect(cmd.Process.Pid, id)
	if !exited.Exited || !exited.Cleanup {
		t.Fatalf("exited inspect=%+v", exited)
	}
}

func TestIdentityStableAndStopRejectsReusedPID(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	id, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Identity(cmd.Process.Pid)
	if err != nil || again != id {
		t.Fatalf("unstable identity: %q %q %v", id, again, err)
	}
	if Stop(cmd.Process.Pid, id+"0", time.Millisecond) {
		t.Fatal("accepted mismatched start time")
	}
	if alive, err := Alive(cmd.Process.Pid); err != nil || !alive {
		t.Fatalf("mismatch killed process: %v %v", alive, err)
	}
	if !Stop(cmd.Process.Pid, id, 50*time.Millisecond) {
		t.Fatal("cleanup not confirmed")
	}
	if alive, err := Alive(cmd.Process.Pid); err != nil || alive {
		t.Fatalf("still alive: %v %v", alive, err)
	}
	_ = cmd.Wait()
	if _, err := Identity(cmd.Process.Pid); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing process: %v", err)
	}
}

func TestRunCleansGroupAfterLeaderExit(t *testing.T) {
	var pid int
	result := Run(context.Background(), "/bin/sh", []string{"-c", "sleep 60 >/dev/null 2>&1 & exit 0"}, os.Environ(), t.TempDir(), nil, io.Discard, io.Discard, 50*time.Millisecond, func(p int, _ string) error { pid = p; return nil })
	if result.Err != nil || result.ExitCode != 0 || !result.Cleanup {
		t.Fatalf("result: %+v", result)
	}
	if groupAlive(pid) {
		t.Fatal("descendant remains after leader exit")
	}
}

func TestRunCancellationKillsResistantGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var pid int
	result := Run(ctx, "/bin/sh", []string{"-c", "trap '' TERM; sleep 60 & wait"}, os.Environ(), t.TempDir(), nil, io.Discard, io.Discard, 50*time.Millisecond, func(p int, _ string) error { pid = p; return nil })
	if !errors.Is(result.Err, context.DeadlineExceeded) || !result.Cleanup {
		t.Fatalf("result: %+v", result)
	}
	if groupAlive(pid) {
		t.Fatal("group remains after cancellation")
	}
}
