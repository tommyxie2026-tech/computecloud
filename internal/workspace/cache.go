package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// cacheLock coordinates providers and GC without creating another execution ledger.
func (p *LocalPreparedProvider) cacheLock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(p.root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(p.root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cache root must be a directory, not a symlink")
	}
	fd, err := unix.Open(filepath.Join(p.root, ".cache.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "cache lock")
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type PrepareMetrics struct {
	TemplateFingerprint string `json:"template_fingerprint"`
	CacheHit            bool   `json:"cache_hit"`
	PrepareMS           int64  `json:"prepare_ms"`
	MaterializeMS       int64  `json:"materialize_ms"`
	Strategy            string `json:"strategy"`
}

func (p *LocalPreparedProvider) PrepareAttempt(ctx context.Context, tmpl WorkspaceTemplate, source, root, attempt string) (string, PrepareMetrics, error) {
	started := time.Now()
	m := PrepareMetrics{TemplateFingerprint: tmpl.Fingerprint(), Strategy: "copy-on-write-or-copy"}
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return "", m, err
	}
	defer unlock()
	path, err := p.templatePath(tmpl.Fingerprint())
	if err != nil {
		return "", m, err
	}
	_, err = os.Lstat(path)
	hit := err == nil
	ref, err := p.prepareTemplate(ctx, tmpl, source)
	if err != nil {
		return "", m, err
	}
	m.PrepareMS = time.Since(started).Milliseconds()
	started = time.Now()
	for i := 0; i < 2; i++ {
		if _, e := os.Lstat(filepath.Join(p.root, ".warm-"+ref.ImmutableRef, fmt.Sprint(i))); e == nil {
			m.Strategy = "warm-pool"
			break
		}
	}
	target, err := p.materializeValidated(ctx, ref, root, attempt)
	m.MaterializeMS = time.Since(started).Milliseconds()
	if err != nil {
		return "", m, err
	}
	m.CacheHit = hit
	if err = p.touchTemplate(ref.ImmutableRef); err != nil {
		// The workspace is still pre-spawn and exclusively owned by this call.
		_ = os.RemoveAll(target)
		return "", m, err
	}
	return target, m, nil
}

type cacheAccess struct {
	LastUsed int64 `json:"last_used"`
}

func (p *LocalPreparedProvider) touchTemplate(fingerprint string) error {
	_, err := p.templatePath(fingerprint)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(cacheAccess{LastUsed: time.Now().UnixMilli()})
	// Access metadata lives outside the immutable template tree.
	tmp := filepath.Join(p.root, ".access-"+fingerprint+".tmp")
	if err = os.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.root, ".access-"+fingerprint))
}

type CachePolicy struct {
	Retention  time.Duration
	MaxBytes   int64
	MaxDeletes int
}
type CacheGCResult struct {
	Bytes      int64 `json:"bytes"`
	Deleted    int   `json:"deleted"`
	OverBudget bool  `json:"over_budget"`
}
type cacheEntry struct {
	name string
	used int64
	size int64
}

func (p *LocalPreparedProvider) CollectCache(ctx context.Context, policy CachePolicy) (CacheGCResult, error) {
	var result CacheGCResult
	if policy.Retention <= 0 || policy.MaxBytes <= 0 || policy.MaxDeletes < 1 || policy.MaxDeletes > 128 {
		return result, errors.New("invalid bounded cache policy")
	}
	unlock, err := p.cacheLock(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	dirs, err := os.ReadDir(p.root)
	if err != nil {
		return result, err
	}
	var entries []cacheEntry
	for _, entry := range dirs {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".deleting-") {
			fingerprint := strings.TrimPrefix(name, ".deleting-")
			if _, e := p.templatePath(fingerprint); e != nil || !entry.IsDir() {
				continue
			}
			if result.Deleted >= policy.MaxDeletes {
				size, e := Size(filepath.Join(p.root, name))
				if e != nil {
					return result, e
				}
				result.Bytes += size
			}
			if result.Deleted < policy.MaxDeletes {
				if err = os.RemoveAll(filepath.Join(p.root, ".warm-"+fingerprint)); err != nil {
					return result, err
				}
				if err = os.RemoveAll(filepath.Join(p.root, name)); err != nil {
					return result, err
				}
				if err = os.Remove(filepath.Join(p.root, ".access-"+fingerprint)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return result, err
				}
				result.Deleted++
			}
			continue
		}
		if _, e := p.templatePath(name); e != nil || !entry.IsDir() {
			continue
		}
		body, e := os.ReadFile(filepath.Join(p.root, name, templateManifestName))
		if e != nil {
			return result, e
		}
		var manifest templateManifest
		if e = json.Unmarshal(body, &manifest); e != nil {
			return result, e
		}
		if manifest.Template.Validate() != nil || manifest.Fingerprint != name || manifest.Template.Fingerprint() != name || manifest.PreparedAt <= 0 {
			return result, errors.New("invalid cache manifest; refusing GC")
		}
		used := manifest.PreparedAt
		body, e = os.ReadFile(filepath.Join(p.root, ".access-"+name))
		if e == nil {
			var access cacheAccess
			if json.Unmarshal(body, &access) != nil || access.LastUsed <= 0 {
				return result, errors.New("invalid cache access record; refusing GC")
			}
			used = access.LastUsed
		} else if !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
		size, e := Size(filepath.Join(p.root, name))
		if e != nil {
			return result, e
		}
		warmBytes, e := Size(filepath.Join(p.root, ".warm-"+name))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
		size += warmBytes
		result.Bytes += size
		entries = append(entries, cacheEntry{name, used, size})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].used == entries[j].used {
			return entries[i].name < entries[j].name
		}
		return entries[i].used < entries[j].used
	})
	cutoff := time.Now().Add(-policy.Retention).UnixMilli()
	for _, entry := range entries {
		if result.Deleted >= policy.MaxDeletes {
			break
		}
		if entry.used > cutoff && result.Bytes <= policy.MaxBytes {
			continue
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		source := filepath.Join(p.root, entry.name)
		tombstone := filepath.Join(p.root, ".deleting-"+entry.name)
		if _, e := os.Lstat(tombstone); !errors.Is(e, os.ErrNotExist) {
			return result, fmt.Errorf("cache tombstone already exists")
		}
		if err = os.Rename(source, tombstone); err != nil {
			return result, err
		}
		if err = os.RemoveAll(filepath.Join(p.root, ".warm-"+entry.name)); err != nil {
			return result, err
		}
		if err = os.RemoveAll(tombstone); err != nil {
			return result, err
		}
		if err = os.Remove(filepath.Join(p.root, ".access-"+entry.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		result.Deleted++
		result.Bytes -= entry.size
	}
	result.OverBudget = result.Bytes > policy.MaxBytes
	return result, nil
}
