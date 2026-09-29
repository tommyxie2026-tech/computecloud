package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func preparedTestRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "fixture")
	return dir, run("rev-parse", "HEAD")
}

func testTemplate(commit string) WorkspaceTemplate {
	return WorkspaceTemplate{
		TemplateID: "repo-baseline",
		RepositoryRef: "repo",
		BaseCommit: commit,
		DependencyFingerprint: "deps:v1",
		EnvironmentFingerprint: "environment:process",
		RuntimeFingerprint: "runtime:codex",
		ToolFingerprint: "tool:git",
		Version: 1,
	}
}

func TestPreparedWorkspaceTemplateFingerprintStable(t *testing.T) {
	tmpl := testTemplate("abc")
	if err := tmpl.Validate(); err != nil {
		t.Fatal(err)
	}
	if tmpl.Fingerprint() != tmpl.Fingerprint() {
		t.Fatal("template fingerprint is not stable")
	}
	changed := tmpl
	changed.BaseCommit = "def"
	if tmpl.Fingerprint() == changed.Fingerprint() {
		t.Fatal("template identity change did not change fingerprint")
	}
}

func TestPreparedWorkspaceLocalProviderPrepareInspectAndRestart(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	root := filepath.Join(t.TempDir(), "prepared")
	provider, err := NewLocalPreparedProvider(root)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate(commit)
	ref, err := provider.PrepareTemplate(context.Background(), tmpl, repo)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provider != "local-prepared" || ref.ImmutableRef != tmpl.Fingerprint() || ref.PreparedAt == 0 {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	inspect, err := provider.InspectTemplate(context.Background(), tmpl, ref)
	if err != nil || !inspect.Valid {
		t.Fatalf("inspect=%+v err=%v", inspect, err)
	}

	restarted, err := NewLocalPreparedProvider(root)
	if err != nil {
		t.Fatal(err)
	}
	inspect, err = restarted.InspectTemplate(context.Background(), tmpl, ref)
	if err != nil || !inspect.Valid || inspect.PreparedAt != ref.PreparedAt {
		t.Fatalf("restart inspect=%+v err=%v", inspect, err)
	}

	templatePath := filepath.Join(root, ref.ImmutableRef)
	info, err := os.Stat(filepath.Join(templatePath, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0222 != 0 {
		t.Fatalf("prepared template remained writable: mode=%o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Join(templatePath, ".git", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0200 == 0 {
		t.Fatalf("prepared template directory is not owner-writable for safe cleanup: mode=%o", dirInfo.Mode().Perm())
	}
}

func TestPreparedWorkspaceMaterializesIsolatedWritableAttempts(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	provider, err := NewLocalPreparedProvider(filepath.Join(t.TempDir(), "prepared"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate(commit)
	ref, err := provider.PrepareTemplate(context.Background(), tmpl, repo)
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := filepath.Join(t.TempDir(), "attempts")
	a1, err := provider.MaterializeAttempt(context.Background(), tmpl, ref, attemptRoot, "attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := provider.MaterializeAttempt(context.Background(), tmpl, ref, attemptRoot, "attempt-2")
	if err != nil {
		t.Fatal(err)
	}
	if a1 == a2 {
		t.Fatal("two attempts shared writable workspace")
	}
	if err = os.WriteFile(filepath.Join(a1, "README.md"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(a2, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b2) != "fixture\n" {
		t.Fatalf("attempt mutation leaked across workspaces: %q", b2)
	}
	templateBody, err := os.ReadFile(filepath.Join(provider.root, ref.ImmutableRef, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(templateBody) != "fixture\n" {
		t.Fatalf("attempt mutation changed immutable template: %q", templateBody)
	}
}

func TestPreparedWorkspaceDetectsTemplateTampering(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	provider, err := NewLocalPreparedProvider(filepath.Join(t.TempDir(), "prepared"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate(commit)
	ref, err := provider.PrepareTemplate(context.Background(), tmpl, repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(provider.root, ref.ImmutableRef, "README.md")
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := provider.InspectTemplate(context.Background(), tmpl, ref)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !strings.Contains(result.Reason, "digest") {
		t.Fatalf("tampered template accepted: %+v", result)
	}
	if _, err = provider.MaterializeAttempt(context.Background(), tmpl, ref, filepath.Join(t.TempDir(), "attempts"), "attempt"); err == nil {
		t.Fatal("materialized a corrupted template")
	}
}

func TestPreparedWorkspaceRejectsReferenceMismatchAndExistingAttempt(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	provider, err := NewLocalPreparedProvider(filepath.Join(t.TempDir(), "prepared"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate(commit)
	ref, err := provider.PrepareTemplate(context.Background(), tmpl, repo)
	if err != nil {
		t.Fatal(err)
	}
	bad := ref
	bad.TemplateID = "other"
	result, err := provider.InspectTemplate(context.Background(), tmpl, bad)
	if err != nil || result.Valid {
		t.Fatalf("mismatched ref accepted: result=%+v err=%v", result, err)
	}

	attemptRoot := filepath.Join(t.TempDir(), "attempts")
	if _, err = provider.MaterializeAttempt(context.Background(), tmpl, ref, attemptRoot, "attempt"); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.MaterializeAttempt(context.Background(), tmpl, ref, attemptRoot, "attempt"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing attempt workspace was not rejected: %v", err)
	}
}
