// Package telemetry contains bounded, source-labelled runtime measurements.
package telemetry

import "encoding/json"

type Tokens struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CachedInput int64 `json:"cached_input_tokens"`
}

func ReadTokens(raw []byte) *Tokens {
	var u struct {
		Input      *int64 `json:"input_tokens"`
		Output     *int64 `json:"output_tokens"`
		Cached     int64  `json:"cached_input_tokens"`
		CacheRead  int64  `json:"cache_read_input_tokens"`
		CacheWrite int64  `json:"cache_creation_input_tokens"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 || u.Cached < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
		return nil
	}
	// Limit untrusted protocol counters to keep aggregate arithmetic safe.
	const max = int64(1_000_000_000_000)
	if *u.Input > max || *u.Output > max || u.Cached > max || u.CacheRead > max || u.CacheWrite > max {
		return nil
	}
	return &Tokens{Input: *u.Input + u.CacheRead + u.CacheWrite, Output: *u.Output, CachedInput: u.Cached + u.CacheRead}
}

type Process struct {
	WallMS       int64 `json:"wall_ms"`
	UserCPUMS    int64 `json:"user_cpu_ms"`
	SystemCPUMS  int64 `json:"system_cpu_ms"`
	PeakRSSBytes int64 `json:"peak_rss_bytes"`
}
type Attempt struct {
	UsageComplete bool     `json:"usage_complete"`
	Source        string   `json:"source"`
	Usage         *Tokens  `json:"usage"`
	Process       *Process `json:"process"`
	NativeFinal   bool     `json:"native_final"`
}
