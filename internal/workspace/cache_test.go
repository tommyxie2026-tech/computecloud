package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCacheConcurrentMaterializationAndGC(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	root := filepath.Join(t.TempDir(), "cache")
	attempts := filepath.Join(t.TempDir(), "attempts")
	tmpl := testTemplate(commit)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := NewLocalPreparedProvider(root)
			if err != nil {
				t.Error(err)
				return
			}
			path, _, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, fmt.Sprintf("attempt-%d", i))
			if err != nil {
				t.Error(err)
				return
			}
			if err = os.WriteFile(filepath.Join(path, "README.md"), []byte(fmt.Sprint(i)), 0600); err != nil {
				t.Error(err)
			}
			if _, err = p.CollectCache(context.Background(), CachePolicy{time.Hour, 1, 1}); err != nil {
				t.Error(err)
			}
			data, err := os.ReadFile(filepath.Join(path, "README.md"))
			if err != nil || string(data) != fmt.Sprint(i) {
				t.Errorf("isolated attempt: %q %v", data, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCacheHitRestartTombstoneAndUnknownMetadata(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	root := filepath.Join(t.TempDir(), "cache")
	attempts := filepath.Join(t.TempDir(), "attempts")
	p, _ := NewLocalPreparedProvider(root)
	tmpl := testTemplate(commit)
	_, first, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "a")
	if err != nil || first.CacheHit {
		t.Fatalf("first=%+v %v", first, err)
	}
	p, _ = NewLocalPreparedProvider(root)
	_, second, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "b")
	if err != nil || !second.CacheHit {
		t.Fatalf("second=%+v %v", second, err)
	}
	access := filepath.Join(root, ".access-"+tmpl.Fingerprint())
	if err = os.WriteFile(access, []byte(`{"last_used":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.CollectCache(context.Background(), CachePolicy{time.Hour, 1, 1}); err == nil {
		t.Fatal("invalid metadata authorized deletion")
	}
	if err = p.touchTemplate(tmpl.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(root, tmpl.Fingerprint()), filepath.Join(root, ".deleting-"+tmpl.Fingerprint())); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(root, "unknown")
	if err = os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	p, _ = NewLocalPreparedProvider(root)
	result, err := p.CollectCache(context.Background(), CachePolicy{time.Hour, 1, 1})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("recovery=%+v %v", result, err)
	}
	if _, err = os.Stat(unknown); err != nil {
		t.Fatal("deleted unknown cache directory")
	}
}

func TestCacheCancellationAndSymlinkRoot(t *testing.T) {
	p, _ := NewLocalPreparedProvider(filepath.Join(t.TempDir(), "cache"))
	unlock, err := p.cacheLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = p.cacheLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(p.root, link); err != nil {
		t.Fatal(err)
	}
	other, _ := NewLocalPreparedProvider(link)
	if _, err = other.cacheLock(context.Background()); err == nil {
		t.Fatal("symlink cache root accepted")
	}
}

func TestCacheTamperFailsClosed(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	root := filepath.Join(t.TempDir(), "cache")
	p, _ := NewLocalPreparedProvider(root)
	tmpl := testTemplate(commit)
	attempts := filepath.Join(t.TempDir(), "attempts")
	if _, _, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "a"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, tmpl.Fingerprint(), "README.md")
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "b"); err == nil {
		t.Fatal("corrupt cache accepted")
	}
}

func TestCacheWarmPoolIsolationCorruptionAndGC(t *testing.T) {
	repo, commit := preparedTestRepo(t)
	root := filepath.Join(t.TempDir(), "cache")
	p, _ := NewLocalPreparedProvider(root)
	tmpl := testTemplate(commit)
	ref, err := p.PrepareTemplate(context.Background(), tmpl, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Prewarm(context.Background(), tmpl, ref, 2); err != nil {
		t.Fatal(err)
	}
	attempts := filepath.Join(t.TempDir(), "attempts")
	target, m, err := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "first")
	if err != nil || m.Strategy != "warm-pool" {
		t.Fatalf("warm use=%+v %v", m, err)
	}
	if err = os.WriteFile(filepath.Join(target, "README.md"), []byte("attempt only"), 0600); err != nil {
		t.Fatal(err)
	}
	untouched := filepath.Join(root, ".warm-"+ref.ImmutableRef, "1", "README.md")
	data, err := os.ReadFile(untouched)
	if err != nil || string(data) != "fixture\n" {
		t.Fatalf("warm isolation=%q %v", data, err)
	}
	if err = os.Chmod(untouched, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(untouched, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.PrepareAttempt(context.Background(), tmpl, repo, attempts, "second"); err == nil {
		t.Fatal("corrupt warm slot accepted")
	}
	result, err := p.CollectCache(context.Background(), CachePolicy{time.Hour, 1, 1})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("warm GC=%+v %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(root, ".warm-"+ref.ImmutableRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("warm cache not deleted")
	}
	if data, err = os.ReadFile(filepath.Join(target, "README.md")); err != nil || string(data) != "attempt only" {
		t.Fatal("GC damaged attempt")
	}
}
