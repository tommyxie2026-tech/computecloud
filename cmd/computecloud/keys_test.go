package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

func TestKeyBootstrapCreatesManagerKeyWithoutPrintingSecret(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "manager.token")
	var output bytes.Buffer
	err := runKeysBootstrap(context.Background(), keyBootstrapOptions{DataDir: dir, Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, Credentials: []string{"account"}, OutputPath: keyPath, ExpiresIn: 24 * time.Hour}, &output)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(secret), "cc_live_") {
		t.Fatal("bootstrap key has unexpected format")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode=%v", info.Mode().Perm())
	}
	if strings.Contains(output.String(), string(secret)) {
		t.Fatal("bootstrap command printed plaintext key")
	}
	db, err := store.OpenForBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys, err := db.ListAPIKeys(context.Background(), "alice")
	if err != nil || len(keys) != 1 || !containsString(keys[0].Scopes, "api_keys:manage") || keys[0].Digest != "" {
		t.Fatalf("bootstrap record=%+v err=%v", keys, err)
	}
}

func TestKeysBootstrapCommandDispatch(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "manager.token")
	err := run(context.Background(), []string{"keys", "bootstrap", "--data-dir", dir, "--owner", "alice", "--project", "p", "--scope", "jobs:read", "--out", out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

func TestKeyBootstrapRefusesSecondActiveManagerAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(t.TempDir(), "manager.token")
	options := keyBootstrapOptions{DataDir: dir, Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, OutputPath: firstPath, ExpiresIn: time.Hour}
	if err := runKeysBootstrap(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runKeysBootstrap(context.Background(), options, &bytes.Buffer{}); err == nil {
		t.Fatal("second active manager key accepted")
	}
	if err := runKeysBootstrap(context.Background(), keyBootstrapOptions{DataDir: dir, Owner: "bob", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, OutputPath: firstPath, ExpiresIn: time.Hour}, &bytes.Buffer{}); err == nil {
		t.Fatal("existing key file overwritten")
	}
}

func TestKeyBootstrapRejectsUnsafeScopeAndExpiry(t *testing.T) {
	for name, options := range map[string]keyBootstrapOptions{
		"model scope": {DataDir: t.TempDir(), Owner: "alice", Projects: []string{"p"}, Scopes: []string{"models:invoke"}, OutputPath: filepath.Join(t.TempDir(), "key"), ExpiresIn: time.Hour},
		"long expiry": {DataDir: t.TempDir(), Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, OutputPath: filepath.Join(t.TempDir(), "key"), ExpiresIn: 91 * 24 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runKeysBootstrap(context.Background(), options, &bytes.Buffer{}); err == nil {
				t.Fatal("unsafe bootstrap input accepted")
			}
		})
	}
}
