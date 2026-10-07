package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const templateManifestName = ".computecloud-template.json"

type WorkspaceTemplate struct {
	TemplateID             string `json:"template_id"`
	RepositoryRef          string `json:"repository_ref"`
	BaseCommit             string `json:"base_commit"`
	DependencyFingerprint  string `json:"dependency_fingerprint,omitempty"`
	EnvironmentFingerprint string `json:"environment_fingerprint,omitempty"`
	RuntimeFingerprint     string `json:"runtime_fingerprint,omitempty"`
	ToolFingerprint        string `json:"tool_fingerprint,omitempty"`
	Version                int    `json:"version"`
}

func (t WorkspaceTemplate) Validate() error {
	if strings.TrimSpace(t.TemplateID) == "" {
		return errors.New("workspace template id required")
	}
	if strings.TrimSpace(t.RepositoryRef) == "" {
		return errors.New("workspace template repository_ref required")
	}
	if strings.TrimSpace(t.BaseCommit) == "" {
		return errors.New("workspace template base_commit required")
	}
	if t.Version < 1 {
		return errors.New("workspace template version must be positive")
	}
	if len(t.TemplateID) > 128 || len(t.RepositoryRef) > 512 || len(t.BaseCommit) > 256 {
		return errors.New("workspace template identity field too large")
	}
	for _, v := range []string{
		t.DependencyFingerprint,
		t.EnvironmentFingerprint,
		t.RuntimeFingerprint,
		t.ToolFingerprint,
	} {
		if len(v) > 512 {
			return errors.New("workspace template fingerprint field too large")
		}
	}
	return nil
}

func (t WorkspaceTemplate) Fingerprint() string {
	raw, _ := json.Marshal(t)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type PreparedWorkspaceRef struct {
	TemplateID   string `json:"template_id"`
	Provider     string `json:"provider"`
	ImmutableRef string `json:"immutable_ref"`
	PreparedAt   int64  `json:"prepared_at"`
}

type ProviderDescriptor struct {
	Name    string
	Version string
}

type InspectResult struct {
	Valid      bool
	Reason     string
	PreparedAt int64
}

type PreparedProvider interface {
	Describe() ProviderDescriptor
	PrepareTemplate(context.Context, WorkspaceTemplate, string) (PreparedWorkspaceRef, error)
	InspectTemplate(context.Context, WorkspaceTemplate, PreparedWorkspaceRef) (InspectResult, error)
	MaterializeAttempt(context.Context, WorkspaceTemplate, PreparedWorkspaceRef, string, string) (string, error)
	ReleaseTemplate(context.Context, PreparedWorkspaceRef) error
}

type LocalPreparedProvider struct {
	root string
}

func NewLocalPreparedProvider(root string) (*LocalPreparedProvider, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, errors.New("prepared workspace root must be an absolute path")
	}
	return &LocalPreparedProvider{root: root}, nil
}

func (p *LocalPreparedProvider) Describe() ProviderDescriptor {
	return ProviderDescriptor{Name: "local-prepared", Version: "v1"}
}

type templateManifest struct {
	Template      WorkspaceTemplate `json:"template"`
	Fingerprint   string            `json:"fingerprint"`
	ContentDigest string            `json:"content_digest"`
	PreparedAt    int64             `json:"prepared_at"`
}

func (p *LocalPreparedProvider) templatePath(fingerprint string) (string, error) {
	if len(fingerprint) != 64 {
		return "", errors.New("invalid prepared workspace fingerprint")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return "", errors.New("invalid prepared workspace fingerprint")
	}
	return filepath.Join(p.root, fingerprint), nil
}

func (p *LocalPreparedProvider) prepareTemplate(ctx context.Context, tmpl WorkspaceTemplate, source string) (PreparedWorkspaceRef, error) {
	if err := tmpl.Validate(); err != nil {
		return PreparedWorkspaceRef{}, err
	}
	if source == "" || !filepath.IsAbs(source) {
		return PreparedWorkspaceRef{}, errors.New("template repository must be an authorized local path")
	}
	if err := os.MkdirAll(p.root, 0700); err != nil {
		return PreparedWorkspaceRef{}, err
	}
	fingerprint := tmpl.Fingerprint()
	finalPath, err := p.templatePath(fingerprint)
	if err != nil {
		return PreparedWorkspaceRef{}, err
	}
	if _, err = os.Stat(finalPath); err == nil {
		ref := PreparedWorkspaceRef{
			TemplateID:   tmpl.TemplateID,
			Provider:     p.Describe().Name,
			ImmutableRef: fingerprint,
		}
		result, inspectErr := p.inspectTemplate(ctx, tmpl, ref)
		if inspectErr != nil {
			return PreparedWorkspaceRef{}, inspectErr
		}
		if !result.Valid {
			return PreparedWorkspaceRef{}, fmt.Errorf("prepared workspace exists but is invalid: %s", result.Reason)
		}
		ref.PreparedAt = result.PreparedAt
		return ref, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return PreparedWorkspaceRef{}, err
	}

	stageRoot, err := os.MkdirTemp(p.root, ".prepare-")
	if err != nil {
		return PreparedWorkspaceRef{}, err
	}
	defer os.RemoveAll(stageRoot)
	stagePath := filepath.Join(stageRoot, "template")
	if _, err = Git(ctx, "", "clone", "--no-hardlinks", "--no-checkout", "--", source, stagePath); err != nil {
		return PreparedWorkspaceRef{}, err
	}
	if _, err = Git(ctx, stagePath, "checkout", "--detach", tmpl.BaseCommit); err != nil {
		return PreparedWorkspaceRef{}, err
	}
	digest, err := treeDigest(ctx, stagePath)
	if err != nil {
		return PreparedWorkspaceRef{}, err
	}
	preparedAt := timeNowUnixMilli()
	manifest := templateManifest{
		Template:      tmpl,
		Fingerprint:   fingerprint,
		ContentDigest: digest,
		PreparedAt:    preparedAt,
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return PreparedWorkspaceRef{}, err
	}
	if err = os.WriteFile(filepath.Join(stagePath, templateManifestName), body, 0600); err != nil {
		return PreparedWorkspaceRef{}, err
	}
	if err = os.Rename(stagePath, finalPath); err != nil {
		if _, statErr := os.Stat(finalPath); statErr == nil {
			ref := PreparedWorkspaceRef{TemplateID: tmpl.TemplateID, Provider: p.Describe().Name, ImmutableRef: fingerprint}
			result, inspectErr := p.inspectTemplate(ctx, tmpl, ref)
			if inspectErr == nil && result.Valid {
				ref.PreparedAt = result.PreparedAt
				return ref, nil
			}
		}
		return PreparedWorkspaceRef{}, err
	}
	if err = freezeTree(finalPath); err != nil {
		_ = makeTreeWritable(finalPath)
		_ = os.RemoveAll(finalPath)
		return PreparedWorkspaceRef{}, err
	}
	return PreparedWorkspaceRef{
		TemplateID:   tmpl.TemplateID,
		Provider:     p.Describe().Name,
		ImmutableRef: fingerprint,
		PreparedAt:   preparedAt,
	}, nil
}

func (p *LocalPreparedProvider) inspectTemplate(ctx context.Context, tmpl WorkspaceTemplate, ref PreparedWorkspaceRef) (InspectResult, error) {
	if err := tmpl.Validate(); err != nil {
		return InspectResult{}, err
	}
	if ref.Provider != p.Describe().Name || ref.TemplateID != tmpl.TemplateID || ref.ImmutableRef != tmpl.Fingerprint() {
		return InspectResult{Valid: false, Reason: "template reference mismatch"}, nil
	}
	path, err := p.templatePath(ref.ImmutableRef)
	if err != nil {
		return InspectResult{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return InspectResult{Valid: false, Reason: "template missing"}, nil
	}
	if err != nil {
		return InspectResult{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return InspectResult{Valid: false, Reason: "template is not a directory"}, nil
	}
	body, err := os.ReadFile(filepath.Join(path, templateManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return InspectResult{Valid: false, Reason: "template manifest missing"}, nil
	}
	if err != nil {
		return InspectResult{}, err
	}
	var manifest templateManifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		return InspectResult{Valid: false, Reason: "template manifest invalid"}, nil
	}
	if manifest.Template != tmpl || manifest.Fingerprint != tmpl.Fingerprint() {
		return InspectResult{Valid: false, Reason: "template manifest mismatch"}, nil
	}
	digest, err := treeDigest(ctx, path)
	if err != nil {
		return InspectResult{}, err
	}
	if digest != manifest.ContentDigest {
		return InspectResult{Valid: false, Reason: "template content digest mismatch", PreparedAt: manifest.PreparedAt}, nil
	}
	return InspectResult{Valid: true, PreparedAt: manifest.PreparedAt}, nil
}

func (p *LocalPreparedProvider) materializeAttempt(ctx context.Context, tmpl WorkspaceTemplate, ref PreparedWorkspaceRef, attemptRoot, attemptID string) (string, error) {
	result, err := p.inspectTemplate(ctx, tmpl, ref)
	if err != nil {
		return "", err
	}
	if !result.Valid {
		return "", fmt.Errorf("cannot materialize invalid prepared workspace: %s", result.Reason)
	}
	return p.materializeValidated(ctx, ref, attemptRoot, attemptID)
}

// Called only while holding the root lock after successful template validation.
func (p *LocalPreparedProvider) materializeValidated(ctx context.Context, ref PreparedWorkspaceRef, attemptRoot, attemptID string) (string, error) {
	source, err := p.templatePath(ref.ImmutableRef)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(attemptRoot, 0700); err != nil {
		return "", err
	}
	target, err := Path(attemptRoot, attemptID)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("attempt workspace already exists")
	}
	if hit, err := p.takeWarm(ctx, ref, target); err != nil {
		return "", err
	} else if hit {
		return target, nil
	}
	if err = copyWritableTree(ctx, source, target); err != nil {
		_ = os.RemoveAll(target)
		return "", err
	}
	return target, nil
}

func (p *LocalPreparedProvider) ReleaseTemplate(ctx context.Context, ref PreparedWorkspaceRef) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if ref.Provider != p.Describe().Name {
		return errors.New("prepared workspace provider mismatch")
	}
	_, err := p.templatePath(ref.ImmutableRef)
	return err
}

func treeDigest(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." || rel == templateManifestName {
			return nil
		}
		if _, err = io.WriteString(h, rel+"\x00"); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if _, err = io.WriteString(h, info.Mode().Type().String()+"\x00"); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = io.WriteString(h, target+"\x00")
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func makeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() | 0200
		if entry.IsDir() {
			mode |= 0700
		} else {
			mode |= 0600
		}
		return os.Chmod(path, mode)
	})
}

func freezeTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if entry.IsDir() {
			// Keep directories owner-writable so cache trees remain safely
			// removable by cleanup/tests. Immutability is enforced on file
			// contents plus digest verification, not by making directory
			// entries undeletable.
			mode |= 0700
			return os.Chmod(path, mode)
		}
		mode &^= 0222
		mode |= 0400
		return os.Chmod(path, mode)
	})
}

func copyWritableTree(ctx context.Context, source, target string) error {
	type fileCopy struct {
		source, target string
		mode           os.FileMode
	}
	files := make(chan fileCopy, 32)
	var workers sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range files {
				errMu.Lock()
				skip := firstErr != nil
				errMu.Unlock()
				if skip {
					continue
				}
				if err := copyWritableFile(ctx, item.source, item.target, item.mode); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
			}
		}()
	}
	walkErr := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == templateManifestName {
			return nil
		}
		dst := target
		if rel != "." {
			dst = filepath.Join(target, rel)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, dst)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm()|0700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		select {
		case files <- fileCopy{path, dst, info.Mode().Perm() | 0600}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(files)
	workers.Wait()
	if walkErr != nil {
		return walkErr
	}
	return firstErr
}

func copyWritableFile(ctx context.Context, path, dst string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cloneFile(path, dst, mode); err == nil {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		_ = src.Close()
		return err
	}
	_, copyErr := io.Copy(out, src)
	srcCloseErr := src.Close()
	outCloseErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if srcCloseErr != nil {
		return srcCloseErr
	}
	return outCloseErr
}

var timeNowUnixMilli = func() int64 {
	return time.Now().UnixMilli()
}

func (p *LocalPreparedProvider) PrepareTemplate(ctx context.Context, tmpl WorkspaceTemplate, source string) (PreparedWorkspaceRef, error) {
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return PreparedWorkspaceRef{}, err
	}
	defer unlock()
	ref, err := p.prepareTemplate(ctx, tmpl, source)
	if err == nil {
		err = p.touchTemplate(ref.ImmutableRef)
	}
	return ref, err
}
func (p *LocalPreparedProvider) InspectTemplate(ctx context.Context, tmpl WorkspaceTemplate, ref PreparedWorkspaceRef) (InspectResult, error) {
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return InspectResult{}, err
	}
	defer unlock()
	return p.inspectTemplate(ctx, tmpl, ref)
}
func (p *LocalPreparedProvider) MaterializeAttempt(ctx context.Context, tmpl WorkspaceTemplate, ref PreparedWorkspaceRef, root, attempt string) (string, error) {
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	return p.materializeAttempt(ctx, tmpl, ref, root, attempt)
}
