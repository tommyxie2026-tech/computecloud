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
func Prepare(ctx context.Context, root, id, source, commit string) (string, error) {
	if source == "" || !filepath.IsAbs(source) {
		return "", errors.New("repository must be an authorized local path")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	target := filepath.Join(root, id)
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
