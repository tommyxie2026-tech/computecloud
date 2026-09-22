// Package testutil provides protocol fixtures; it never calls a model provider.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func Repository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s: %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	run("init")
	if e := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test repository\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "README.md")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	return dir, run("rev-parse", "HEAD")
}
func Token(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(p, []byte(name+"-012345678901234567890123456789"), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func CLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent-fixture")
	body := `#!/bin/sh
if [ "$1" = '--version' ]; then echo fixture-1; exit 0; fi
prompt=$(cat)
if [ "$1" = 'exec' ]; then
  echo '{"type":"thread.started","thread_id":"fixture-session"}'
  echo '{"type":"turn.started"}'
else
  echo '{"type":"system","session_id":"fixture-session"}'
fi
case "$prompt" in
  *slow*) sleep 60 & echo $! > descendant.pid; wait ;;
  *fail*) echo '{"type":"turn.failed"}'; exit 2 ;;
  *missing*) exit 0 ;;
  *) sleep 0.2 ;;
esac
printf 'fixture result\n' > result.txt
if [ "$1" = 'exec' ]; then
  echo '{"type":"item.completed","item":{"type":"agent_message","text":"fixture completed"}}'
  echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
else
  echo '{"type":"result","subtype":"success","is_error":false,"result":"fixture completed","session_id":"fixture-session"}'
fi
`
	if e := os.WriteFile(p, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
