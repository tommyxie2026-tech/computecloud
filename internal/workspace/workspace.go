package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var ErrQuotaExceeded = errors.New("workspace quota exceeded")

type bounded struct {
	bytes.Buffer
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("command output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", a...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	out := &bounded{limit: 32 << 20}
	cmd.Stdout = out
	cmd.Stderr = out
	e := cmd.Run()
	if e != nil {
		return nil, fmt.Errorf("git command failed: %w", e)
	}
	return out.Bytes(), nil
}
func Path(root, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", errors.New("invalid workspace id")
	}
	return filepath.Join(root, id), nil
}
func Prepare(ctx context.Context, root, id, source, commit string) (string, error) {
	if source == "" || !filepath.IsAbs(source) {
		return "", errors.New("repository must be an authorized local path")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	target, e := Path(root, id)
	if e != nil {
		return "", e
	}
	if _, e := os.Lstat(target); !errors.Is(e, os.ErrNotExist) {
		return "", errors.New("workspace already exists; refusing unsafe restart")
	}
	if _, e := Git(ctx, "", "clone", "--no-hardlinks", "--no-checkout", "--", source, target); e != nil {
		return "", e
	}
	if _, e := Git(ctx, target, "checkout", "--detach", commit); e != nil {
		return "", e
	}
	return target, nil
}
func Diff(ctx context.Context, dir, commit string) ([]byte, error) {
	if _, e := Git(ctx, dir, "add", "--intent-to-add", "--all"); e != nil {
		return nil, e
	}
	return Git(ctx, dir, "diff", "--binary", "--no-ext-diff", commit, "--")
}


func Size(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func CheckQuota(dir string, limit int64) (int64, error) {
	size, err := Size(dir)
	if err != nil {
		return 0, err
	}
	if limit > 0 && size > limit {
		return size, fmt.Errorf("%w: %d > %d bytes", ErrQuotaExceeded, size, limit)
	}
	return size, nil
}

// WatchQuota periodically checks a writable workspace. It emits at most one
// error and then stops. A zero limit disables monitoring.
func WatchQuota(ctx context.Context, dir string, limit int64, interval time.Duration) <-chan error {
	out := make(chan error, 1)
	if limit <= 0 {
		close(out)
		return out
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	go func() {
		defer close(out)
		check := func() bool {
			_, err := CheckQuota(dir, limit)
			if err != nil {
				select {
				case out <- err:
				case <-ctx.Done():
				}
				return true
			}
			return false
		}
		if check() {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if check() {
					return
				}
			}
		}
	}()
	return out
}

func Remove(root, id string) error {
	path, err := Path(root, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
