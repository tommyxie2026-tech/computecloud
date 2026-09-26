package process

import (
	"context"
	"io"
	"os"
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
