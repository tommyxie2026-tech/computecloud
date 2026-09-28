package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type TemplateSpec struct {
	RepositoryRef  string   `json:"repository_ref"`
	BaseCommit     string   `json:"base_commit"`
	RuntimeProfile string   `json:"runtime_profile"`
	Environment    string   `json:"environment"`
	Tools          []string `json:"tools,omitempty"`
}

func NormalizeTemplateSpec(spec TemplateSpec) (TemplateSpec, error) {
	if spec.RepositoryRef == "" || spec.BaseCommit == "" || spec.RuntimeProfile == "" || spec.Environment == "" {
		return TemplateSpec{}, errors.New("workspace template identity is incomplete")
	}
	spec.Tools = append([]string(nil), spec.Tools...)
	sort.Strings(spec.Tools)
	out := spec.Tools[:0]
	for _, tool := range spec.Tools {
		if tool == "" {
			return TemplateSpec{}, errors.New("workspace template tool name is empty")
		}
		if len(out) == 0 || out[len(out)-1] != tool {
			out = append(out, tool)
		}
	}
	spec.Tools = out
	return spec, nil
}

func TemplateID(spec TemplateSpec) (string, error) {
	spec, err := NormalizeTemplateSpec(spec)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func BuildSeed(ctx context.Context, root, id, source, commit string) (string, error) {
	if source == "" || !filepath.IsAbs(source) {
		return "", errors.New("repository must be an authorized local path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	target, err := Path(root, id)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("prepared workspace seed already exists")
	}
	tmp, err := os.MkdirTemp(root, "."+id+".building-")
	if err != nil {
		return "", err
	}
	_ = os.Remove(tmp)
	defer os.RemoveAll(tmp)

	if _, err = Git(ctx, "", "clone", "--bare", "--no-hardlinks", "--", source, tmp); err != nil {
		return "", err
	}
	if err = VerifySeed(ctx, tmp, commit); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, target); err != nil {
		return "", err
	}
	return target, nil
}

func VerifySeed(ctx context.Context, seed, commit string) error {
	if seed == "" || !filepath.IsAbs(seed) || commit == "" {
		return errors.New("prepared workspace seed/commit required")
	}
	info, err := os.Stat(seed)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("prepared workspace seed is not a directory")
	}
	if _, err = Git(ctx, "", "--git-dir", seed, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return errors.New("prepared workspace seed does not contain baseline commit")
	}
	return nil
}

func PrepareFromSeed(ctx context.Context, root, id, seed, origin, commit string) (string, error) {
	if seed == "" || !filepath.IsAbs(seed) {
		return "", errors.New("prepared workspace seed must be absolute")
	}
	if origin == "" || !filepath.IsAbs(origin) {
		return "", errors.New("repository must be an authorized local path")
	}
	if err := VerifySeed(ctx, seed, commit); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	target, err := Path(root, id)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("workspace already exists; refusing unsafe restart")
	}
	// --no-hardlinks makes the Attempt clone independent from the seed. The
	// seed can be GC'ed after this call without invalidating an active Attempt.
	if _, err = Git(ctx, "", "clone", "--no-hardlinks", "--no-checkout", "--", seed, target); err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(target)
		}
	}()
	if _, err = Git(ctx, target, "remote", "set-url", "origin", origin); err != nil {
		return "", err
	}
	if _, err = Git(ctx, target, "checkout", "--detach", commit); err != nil {
		return "", err
	}
	cleanup = false
	return target, nil
}
