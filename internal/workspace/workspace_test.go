package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"../escape", "a/b", "", ".", ".."} {
		if _, err := Path(root, id); err == nil {
			t.Fatalf("unsafe workspace id accepted: %q", id)
		}
	}
	if got, err := Path(root, "attempt-1"); err != nil || got != filepath.Join(root, "attempt-1") {
		t.Fatalf("valid path got=%q err=%v", got, err)
	}
}

func TestQuotaCheckAndWatcher(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckQuota(dir, 5); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota error=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err, ok := <-WatchQuota(ctx, dir, 5, time.Millisecond)
	if !ok || !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("watch quota error=%v ok=%v", err, ok)
	}
	if size, err := CheckQuota(dir, 0); err != nil || size != 0 {
		t.Fatalf("disabled quota size=%d err=%v", size, err)
	}
}
