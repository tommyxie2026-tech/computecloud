package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/workspace"
)

type jobRun struct {
	prompt         string
	dir            string
	manifest       job.Manifest
	sourceFindings map[string]job.Finding
	expectedDiff   []byte
}

func (w *Worker) prepareJob(ctx context.Context, a *pb.Assignment, cwd string) (*jobRun, error) {
	run := &jobRun{prompt: a.Spec.Input.Text, sourceFindings: map[string]job.Finding{}}
	jc := a.Job
	if jc == nil {
		return run, nil
	}
	runtime := w.cfg.Runtimes[a.Spec.RuntimeProfile]
	if jc.TemplateDigest != config.TemplateDigest(runtime.Version, w.cfg.Policies[a.Spec.PolicyRef], w.cfg.Verifiers[a.Spec.AcceptanceProfile]) {
		return nil, fmt.Errorf("TEMPLATE_MISMATCH")
	}
	if jc.Stage != "single" && jc.Stage != "map" && jc.Stage != "reduce" {
		return nil, fmt.Errorf("invalid job stage")
	}
	if jc.Stage == "single" {
		return run, nil
	}
	if jc.Strategy != "report_merge_v1" && jc.Strategy != "patch_merge_v1" {
		return nil, fmt.Errorf("unsupported job strategy")
	}
	meta := map[string]any{"stage": jc.Stage, "partition_key": jc.PartitionKey, "base_commit": a.Spec.Workspace.BaseCommit, "scope_paths": jc.ScopePaths}
	if jc.Stage == "reduce" {
		if len(jc.InputManifestJson) > job.MaxManifestBytes || job.Hash(jc.InputManifestJson) != jc.InputManifestSha256 {
			return nil, fmt.Errorf("input manifest hash/size mismatch")
		}
		if e := jsonutil.Decode(jc.InputManifestJson, &run.manifest); e != nil {
			return nil, e
		}
		if run.manifest.Version != "inputs.v1" || run.manifest.BaseCommit != a.Spec.Workspace.BaseCommit || len(run.manifest.Items) == 0 || len(run.manifest.Items) > 32 {
			return nil, fmt.Errorf("invalid input manifest")
		}
		run.dir = filepath.Join(w.cfg.DataDir, "inputs", a.AttemptId)
		if e := os.MkdirAll(filepath.Dir(run.dir), 0700); e != nil {
			return nil, e
		}
		if e := os.Mkdir(run.dir, 0700); e != nil {
			return nil, e
		}
		var total int64
		seen := map[string]bool{}
		paths := map[string]bool{}
		for _, item := range run.manifest.Items {
			if !job.ValidKey(item.PartitionKey) || strings.ContainsAny(item.PartitionKey, "/\\.") || seen[item.PartitionKey] || item.Size < 0 || item.Size > 32<<20 || !job.ValidHash(item.SHA256) {
				return nil, fmt.Errorf("invalid manifest entry")
			}
			seen[item.PartitionKey] = true
			total += item.Size
			if jc.InputLimitBytes < 1 || jc.InputLimitBytes > job.MaxInputBytes || total > jc.InputLimitBytes {
				return nil, fmt.Errorf("reduce input limit exceeded")
			}
			dir, e := w.downloadInput(ctx, a, item, run.dir)
			if e != nil {
				return nil, e
			}
			if jc.Strategy == "report_merge_v1" {
				b, e := os.ReadFile(filepath.Join(dir, "findings.json"))
				if e != nil {
					return nil, e
				}
				var f job.Findings
				if e = jsonutil.Decode(b, &f); e != nil {
					return nil, e
				}
				if f.Version != "findings.v1" || f.BaseCommit != a.Spec.Workspace.BaseCommit || f.PartitionKey != item.PartitionKey {
					return nil, fmt.Errorf("input findings identity mismatch")
				}
				if e = validateFindings(ctx, cwd, a.Spec.Workspace.BaseCommit, f.Findings, false, nil); e != nil {
					return nil, e
				}
				for _, x := range f.Findings {
					run.sourceFindings[item.PartitionKey+"/"+x.ID] = x
				}
			} else {
				b, e := os.ReadFile(filepath.Join(dir, "changes.paths.json"))
				if e != nil {
					return nil, e
				}
				var declared []string
				if e = jsonutil.Decode(b, &declared); e != nil {
					return nil, e
				}
				for _, p := range declared {
					if !job.ValidPath(p) || !job.InScope(p, item.ScopePaths) || paths[p] {
						return nil, fmt.Errorf("PATCH_CONFLICT: invalid/overlapping paths")
					}
				}
				before, e := workspace.Git(ctx, cwd, "write-tree")
				if e != nil {
					return nil, e
				}
				patch := filepath.Join(dir, "changes.patch")
				info, e := os.Stat(patch)
				if e != nil {
					return nil, e
				}
				if info.Size() > 0 {
					if _, e = workspace.Git(ctx, cwd, "apply", "--check", "--index", "--", patch); e != nil {
						return nil, fmt.Errorf("PATCH_CONFLICT: %w", e)
					}
					if _, e = workspace.Git(ctx, cwd, "apply", "--index", "--", patch); e != nil {
						return nil, fmt.Errorf("PATCH_CONFLICT: %w", e)
					}
				}
				actual, e := changedPaths(ctx, cwd, strings.TrimSpace(string(before)))
				if e != nil {
					return nil, e
				}
				sort.Strings(declared)
				if !equalPaths(actual, declared) {
					return nil, fmt.Errorf("patch path manifest mismatch")
				}
				for _, p := range actual {
					paths[p] = true
				}
			}
		}
		if jc.Strategy == "patch_merge_v1" {
			var e error
			run.expectedDiff, e = workspace.Diff(ctx, cwd, a.Spec.Workspace.BaseCommit)
			if e != nil {
				return nil, e
			}
		}
		if e := os.WriteFile(filepath.Join(run.dir, "manifest.json"), jc.InputManifestJson, 0600); e != nil {
			return nil, e
		}
		meta["input_directory"] = run.dir
		meta["manifest_sha256"] = jc.InputManifestSha256
	}
	instructions := "Respect the supplied task and operator policy. Work only on the assigned repository. Do not submit nested jobs. "
	if jc.Strategy == "report_merge_v1" {
		instructions += "Do not modify the repository. Your final response MUST be a single JSON object without Markdown fences. Each finding has id, severity (info/low/medium/high/critical), summary, path, line_start, line_end, evidence (an exact code excerpt from those baseline lines). Empty findings is allowed; do not invent issues. "
		if jc.Stage == "map" {
			instructions += "Return {schema_version:\"findings.v1\",base_commit,partition_key,findings:[]}. Worker writes findings.json from your response; do not create it yourself."
		} else {
			instructions += "Read the supplied input findings and return {schema_version:\"merged-report.v1\",base_commit,manifest_sha256,summary,findings:[]}. Each finding also needs origin:\"map\" with sources:[{partition_key,finding_id}], or origin:\"reduce\" for newly discovered findings. Preserve evidence provenance."
		}
	} else if jc.Stage == "map" {
		instructions += "Modify only the listed scope_paths. Do not commit changes or create symlinks/submodules. Worker captures your patch; use your final response for a short summary."
	} else {
		instructions += "The trusted Worker already applied all input patches. Inspect and summarize them without making any repository changes. Worker runs the configured acceptance checks."
	}
	run.prompt += "\n\nCOMPUTECLOUD JOB CONTRACT:\n" + instructions + "\n" + string(job.JSON(meta))
	return run, nil
}
func (w *Worker) downloadInput(ctx context.Context, a *pb.Assignment, item job.ManifestItem, root string) (string, error) {
	dir := filepath.Join(root, item.PartitionKey)
	if e := os.Mkdir(dir, 0700); e != nil {
		return "", e
	}
	f, e := os.CreateTemp(root, "bundle-")
	if e != nil {
		return "", e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	stream, e := w.client.DownloadInputArtifact(ctx, &pb.InputArtifactRequest{Attempt: ref(a), ArtifactId: item.ArtifactID})
	if e != nil {
		return "", e
	}
	h := sha256.New()
	var n int64
	for {
		chunk, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		n += int64(len(chunk.Data))
		if n > item.Size {
			return "", fmt.Errorf("input exceeds declared size")
		}
		if _, e = io.MultiWriter(f, h).Write(chunk.Data); e != nil {
			return "", e
		}
	}
	if n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
		return "", fmt.Errorf("input artifact hash mismatch")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return "", e
	}
	if e = extractBundle(f, dir, item.Size); e != nil {
		return "", e
	}
	return dir, nil
}
func extractBundle(r io.Reader, dir string, max int64) error {
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if !config.Contains([]string{"report.json", "changes.patch", "stderr.log", "findings.json", "changes.paths.json"}, h.Name) || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || seen[h.Name] || h.Size < 0 {
			return fmt.Errorf("invalid/duplicate artifact member")
		}
		seen[h.Name] = true
		total += h.Size
		if total > max {
			return fmt.Errorf("artifact expansion limit")
		}
		f, e := os.OpenFile(filepath.Join(dir, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.CopyN(f, tr, h.Size)
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	for _, name := range []string{"report.json", "changes.patch", "stderr.log"} {
		if !seen[name] {
			return fmt.Errorf("missing bundle member %s", name)
		}
	}
	return nil
}
func changedPaths(ctx context.Context, cwd, base string) ([]string, error) {
	raw, e := workspace.Git(ctx, cwd, "diff", "--raw", "-z", "--no-renames", "--no-ext-diff", base, "--")
	if e != nil {
		return nil, e
	}
	items := bytes.Split(raw, []byte{0})
	var paths []string
	for i := 0; i+1 < len(items) && len(items[i]) > 0; i += 2 {
		fields := strings.Fields(string(items[i]))
		if len(fields) != 5 {
			return nil, fmt.Errorf("invalid Git diff metadata")
		}
		for _, mode := range []string{strings.TrimPrefix(fields[0], ":"), fields[1]} {
			if mode != "000000" && mode != "100644" && mode != "100755" {
				return nil, fmt.Errorf("symlink/submodule changes unsupported")
			}
		}
		p := string(items[i+1])
		if !job.ValidPath(p) {
			return nil, fmt.Errorf("invalid changed path")
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}
func equalPaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func validateFindings(ctx context.Context, cwd, base string, findings []job.Finding, merged bool, sources map[string]job.Finding) error {
	if findings == nil || len(findings) > 200 {
		return fmt.Errorf("findings must be an array with at most 200 items")
	}
	ids := map[string]bool{}
	for _, f := range findings {
		if !job.ValidKey(f.ID) || ids[f.ID] || !config.Contains([]string{"info", "low", "medium", "high", "critical"}, f.Severity) || f.Summary == "" || len(f.Summary) > 4096 || !job.ValidPath(f.Path) || f.LineStart < 1 || f.LineEnd < f.LineStart || f.Evidence == "" || len(f.Evidence) > 8192 {
			return fmt.Errorf("invalid finding")
		}
		ids[f.ID] = true
		if !merged && (f.Origin != "" || len(f.Sources) != 0) {
			return fmt.Errorf("Map findings cannot claim other sources")
		}
		if merged {
			if f.Origin != "map" && f.Origin != "reduce" {
				return fmt.Errorf("missing finding origin")
			}
			if f.Origin == "map" && len(f.Sources) == 0 {
				return fmt.Errorf("Map finding needs sources")
			}
			if len(f.Sources) > 200 {
				return fmt.Errorf("too many sources")
			}
			matched := false
			for _, src := range f.Sources {
				original, ok := sources[src.PartitionKey+"/"+src.FindingID]
				if !ok {
					return fmt.Errorf("unknown source finding")
				}
				matched = matched || (original.Path == f.Path && original.LineStart == f.LineStart && original.LineEnd == f.LineEnd && original.Evidence == f.Evidence)
			}
			if f.Origin == "map" && !matched {
				return fmt.Errorf("Map provenance does not match source evidence")
			}
		}
		raw, e := workspace.Git(ctx, cwd, "show", base+":"+f.Path)
		if e != nil {
			return fmt.Errorf("finding path missing at baseline")
		}
		if len(raw) > 1<<20 {
			return fmt.Errorf("evidence file exceeds 1 MiB verification limit")
		}
		lines := strings.Split(string(raw), "\n")
		if f.LineEnd > len(lines) || !strings.Contains(strings.Join(lines[f.LineStart-1:f.LineEnd], "\n"), f.Evidence) {
			return fmt.Errorf("finding evidence does not match baseline lines")
		}
	}
	return nil
}
func finishJobOutput(ctx context.Context, a *pb.Assignment, cwd, result string, diff []byte, run *jobRun, verification []map[string]any) (map[string][]byte, error) {
	out := map[string][]byte{}
	jc := a.Job
	if jc == nil || jc.Stage == "single" {
		return out, nil
	}
	if jc.Strategy == "report_merge_v1" {
		if len(diff) != 0 {
			return nil, fmt.Errorf("read-only report task modified repository")
		}
		if jc.Stage == "map" {
			var f job.Findings
			if e := jsonutil.Decode([]byte(result), &f); e != nil {
				return nil, e
			}
			if f.Version != "findings.v1" || f.BaseCommit != a.Spec.Workspace.BaseCommit || f.PartitionKey != jc.PartitionKey {
				return nil, fmt.Errorf("findings identity mismatch")
			}
			if e := validateFindings(ctx, cwd, a.Spec.Workspace.BaseCommit, f.Findings, false, nil); e != nil {
				return nil, e
			}
			out["findings.json"] = job.JSON(f)
		} else {
			var r job.MergedReport
			if e := jsonutil.Decode([]byte(result), &r); e != nil {
				return nil, e
			}
			if r.Version != "merged-report.v1" || r.BaseCommit != a.Spec.Workspace.BaseCommit || r.ManifestHash != jc.InputManifestSha256 || r.Summary == "" {
				return nil, fmt.Errorf("merged report identity mismatch")
			}
			if e := validateFindings(ctx, cwd, a.Spec.Workspace.BaseCommit, r.Findings, true, run.sourceFindings); e != nil {
				return nil, e
			}
			out["review.json"] = job.JSON(r)
			var md strings.Builder
			md.WriteString(r.Summary + "\n")
			for _, f := range r.Findings {
				fmt.Fprintf(&md, "\n- %s [%s] %s:%d–%d: %s\n", f.ID, f.Severity, f.Path, f.LineStart, f.LineEnd, f.Summary)
			}
			out["review.md"] = []byte(md.String())
		}
	} else {
		paths, e := changedPaths(ctx, cwd, a.Spec.Workspace.BaseCommit)
		if e != nil {
			return nil, e
		}
		if paths == nil {
			paths = []string{}
		}
		if jc.Stage == "map" {
			for _, p := range paths {
				if !job.InScope(p, jc.ScopePaths) {
					return nil, fmt.Errorf("modified path outside partition scope")
				}
			}
			out["changes.paths.json"] = job.JSON(paths)
		} else {
			if !bytes.Equal(diff, run.expectedDiff) {
				return nil, fmt.Errorf("Reduce changed trusted merged patch")
			}
			out["merge-report.json"] = job.JSON(map[string]any{"manifest_sha256": jc.InputManifestSha256, "inputs": run.manifest.Items, "final_diff_sha256": job.Hash(diff), "verification": verification})
		}
	}
	return out, nil
}

func jobInputCode(e error) string {
	if strings.HasPrefix(e.Error(), "PATCH_CONFLICT") {
		return "PATCH_CONFLICT"
	}
	if e.Error() == "TEMPLATE_MISMATCH" {
		return "TEMPLATE_MISMATCH"
	}
	return "JOB_INPUT_ERROR"
}
