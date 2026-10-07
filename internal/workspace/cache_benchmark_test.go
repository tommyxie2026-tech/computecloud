package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

// This measures provider materialization, not full Job or real Runtime latency.
func TestCachePerformanceReport(t *testing.T) {
	if os.Getenv("COMPUTECLOUD_CACHE_BENCHMARK") != "1" {
		t.Skip("explicit performance gate")
	}
	repo, commit := preparedTestRepo(t)
	// Include a tracked dependency-like tree so the report identifies its workload.
	for i := 0; i < 128; i++ {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("dependency-%03d", i)), make([]byte, 16384), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Git(context.Background(), repo, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(context.Background(), repo, "commit", "-qm", "dependency fixture"); err != nil {
		t.Fatal(err)
	}
	raw, err := Git(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit = string(raw[:len(raw)-1])
	tmpl := testTemplate(commit)
	attempts := filepath.Join(t.TempDir(), "attempts")
	var cold, warm []float64
	for i := 0; i < 10; i++ {
		start := time.Now()
		if _, err = Prepare(context.Background(), attempts, fmt.Sprintf("cold-%d", i), repo, commit); err != nil {
			t.Fatal(err)
		}
		cold = append(cold, float64(time.Since(start).Microseconds())/1000)
	}
	p, _ := NewLocalPreparedProvider(filepath.Join(t.TempDir(), "cache"))
	hits := 0
	for i := 0; i < 10; i++ {
		start := time.Now()
		_, m, e := p.PrepareAttempt(context.Background(), tmpl, repo, attempts, fmt.Sprintf("warm-%d", i))
		if e != nil {
			t.Fatal(e)
		}
		if m.CacheHit {
			hits++
			warm = append(warm, float64(time.Since(start).Microseconds())/1000)
		}
	}
	sort.Float64s(cold)
	sort.Float64s(warm)
	if hits != 9 {
		t.Fatalf("cache hit count=%d", hits)
	}
	// Exercise the documented ten-Attempt load with independent providers. The
	// root lock is deliberately shared, so this exposes queueing as well as I/O.
	const concurrentAttempts = 10
	var wg sync.WaitGroup
	startGate := make(chan struct{})
	concurrent := make([]float64, concurrentAttempts)
	concurrentHits := make([]bool, concurrentAttempts)
	concurrentErrors := make([]error, concurrentAttempts)
	for i := 0; i < concurrentAttempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			provider, e := NewLocalPreparedProvider(p.root)
			if e != nil {
				concurrentErrors[i] = e
				return
			}
			<-startGate
			started := time.Now()
			path, m, e := provider.PrepareAttempt(context.Background(), tmpl, repo, attempts, fmt.Sprintf("concurrent-%d", i))
			concurrent[i] = float64(time.Since(started).Microseconds()) / 1000
			concurrentHits[i] = m.CacheHit
			if e == nil {
				var body []byte
				body, e = os.ReadFile(filepath.Join(path, "dependency-000"))
				if e == nil && len(body) != 16384 {
					e = fmt.Errorf("attempt %d materialized %d dependency bytes", i, len(body))
				}
			}
			concurrentErrors[i] = e
		}(i)
	}
	concurrentStarted := time.Now()
	close(startGate)
	wg.Wait()
	concurrentWallMS := float64(time.Since(concurrentStarted).Microseconds()) / 1000
	for i, e := range concurrentErrors {
		if e != nil || !concurrentHits[i] {
			t.Fatalf("concurrent attempt %d: cache_hit=%t error=%v", i, concurrentHits[i], e)
		}
	}
	sort.Float64s(concurrent)
	ratio := warm[len(warm)/2] / cold[len(cold)/2]
	report := map[string]any{"scope": "provider_fixture_not_job_benchmark", "os": runtime.GOOS, "arch": runtime.GOARCH, "cold_samples": len(cold), "warm_samples": len(warm), "jobs_executed": 0, "dependency_files": 128, "dependency_bytes": 128 * 16384, "cache_hit_ratio": float64(hits) / 10, "cold_p50_ms": cold[len(cold)/2], "cold_p95_ms": cold[len(cold)-1], "warm_p50_ms": warm[len(warm)/2], "warm_p95_ms": warm[len(warm)-1], "warm_over_cold_p50": ratio, "p50_target_met": ratio <= 0.4, "concurrent_attempts": concurrentAttempts, "concurrent_cache_hits": concurrentAttempts, "concurrent_p50_ms": concurrent[concurrentAttempts/2], "concurrent_p95_ms": concurrent[concurrentAttempts-1], "concurrent_wall_ms": concurrentWallMS}
	body, _ := json.Marshal(report)
	t.Log("CACHE_BENCHMARK " + string(body))
}
