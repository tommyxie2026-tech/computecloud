package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Prewarm prepares independent, read-only copies. It never binds Attempt ownership.
func (p *LocalPreparedProvider) Prewarm(ctx context.Context, tmpl WorkspaceTemplate, ref PreparedWorkspaceRef, slots int) error {
	if slots < 0 || slots > 2 {
		return errors.New("warm slots must be 0..2")
	}
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	result, err := p.inspectTemplate(ctx, tmpl, ref)
	if err != nil {
		return err
	}
	if !result.Valid {
		return errors.New("cannot prewarm invalid template")
	}
	source, _ := p.templatePath(ref.ImmutableRef)
	root := filepath.Join(p.root, ".warm-"+ref.ImmutableRef)
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid warm pool root")
	}
	for i := 0; i < slots; i++ {
		target := filepath.Join(root, fmt.Sprint(i))
		if _, err = os.Lstat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		stage, err := os.MkdirTemp(root, ".prepare-")
		if err != nil {
			return err
		}
		tree := filepath.Join(stage, "tree")
		err = copyWritableTree(ctx, source, tree)
		if err == nil {
			err = freezeTree(tree)
		}
		if err == nil {
			err = os.Rename(tree, target)
		}
		_ = os.RemoveAll(stage)
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *LocalPreparedProvider) takeWarm(ctx context.Context, ref PreparedWorkspaceRef, target string) (bool, error) {
	root := filepath.Join(p.root, ".warm-"+ref.ImmutableRef)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("invalid warm pool root")
	}
	for i := 0; i < 2; i++ {
		slot := filepath.Join(root, fmt.Sprint(i))
		info, err = os.Lstat(slot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("invalid warm slot")
		}
		body, err := os.ReadFile(filepath.Join(p.root, ref.ImmutableRef, templateManifestName))
		if err != nil {
			return false, err
		}
		var manifest templateManifest
		if err = json.Unmarshal(body, &manifest); err != nil {
			return false, err
		}
		digest, err := treeDigest(ctx, slot)
		if err != nil {
			return false, err
		}
		if digest != manifest.ContentDigest {
			return false, errors.New("warm workspace digest mismatch")
		}
		if err = os.Rename(slot, target); err != nil {
			return false, err
		}
		if err = makeTreeWritable(target); err != nil {
			_ = os.RemoveAll(target)
			return false, err
		}
		return true, nil
	}
	return false, nil
}
