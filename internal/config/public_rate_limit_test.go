package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func loadRateLimitConfig(t *testing.T, rate, burst, entries, idle int) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	text := "server:\n" +
		"  data_dir: " + strconv.Quote(filepath.Join(dir, "data")) + "\n" +
		"  http:\n    rate_limit:\n" +
		"      rate_per_minute: " + strconv.Itoa(rate) + "\n" +
		"      burst: " + strconv.Itoa(burst) + "\n" +
		"      max_entries: " + strconv.Itoa(entries) + "\n" +
		"      idle_timeout_seconds: " + strconv.Itoa(idle) + "\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadAndValidatePublicRequestLimit(t *testing.T) {
	c, err := loadRateLimitConfig(t, 120, 30, 5000, 600)
	if err != nil {
		t.Fatalf("load request limit: %v", err)
	}
	c.Server.DefaultV02()
	if err := c.Server.Validate(); err != nil {
		t.Fatalf("validate request limit: %v", err)
	}
	limit := reflect.ValueOf(c.Server.HTTP).FieldByName("RateLimit")
	if !limit.IsValid() {
		t.Fatal("HTTP RateLimit config missing")
	}
	for name, want := range map[string]int{"RatePerMinute": 120, "Burst": 30, "MaxEntries": 5000, "IdleTimeoutSeconds": 600} {
		field := limit.FieldByName(name)
		if !field.IsValid() || int(field.Int()) != want {
			t.Fatalf("RateLimit.%s=%v, want %d", name, field, want)
		}
	}
}

func TestValidateRejectsInvalidPublicRequestLimit(t *testing.T) {
	c, err := loadRateLimitConfig(t, 120, 0, 5000, 600)
	if err != nil {
		t.Fatalf("load partial request limit: %v", err)
	}
	c.Server.DefaultV02()
	if err := c.Server.Validate(); err == nil {
		t.Fatal("invalid request limit accepted")
	}
}
