package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Each inspection hashes many small files. Scratch space must be bounded per
// tree, rather than allocating a fresh 32 KiB copy buffer for every file.
func TestTreeDigestBoundedScratchMemory(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 64; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d", i)), bytes.Repeat([]byte("1"), 16384), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	digest, err := treeDigest(context.Background(), root)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "63d3bff9ffe662dc078ee032eab7af3f925691ede2b9aab0ede51aca853aa0b5" {
		t.Fatalf("digest format changed: %s", digest)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 512*1024 {
		t.Fatalf("digest allocated %d bytes for 64 files; limit 512 KiB", allocated)
	}
	t.Logf("digest scratch and metadata: %d bytes", allocated)
}
