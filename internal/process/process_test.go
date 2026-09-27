package process

import (
	"context"
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
