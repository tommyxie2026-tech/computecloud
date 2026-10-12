package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/store"
)

type keyBootstrapOptions struct {
	DataDir     string
	Owner       string
	Projects    []string
	Scopes      []string
	Credentials []string
	OutputPath  string
	ExpiresIn   time.Duration
}

type stringFlags []string

func (s *stringFlags) String() string { return strings.Join(*s, ",") }
func (s *stringFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*s = append(*s, value)
	return nil
}

func runKeysBootstrap(ctx context.Context, options keyBootstrapOptions, output io.Writer) error {
	if options.DataDir == "" || options.Owner == "" || options.OutputPath == "" || options.ExpiresIn <= 0 || options.ExpiresIn > 90*24*time.Hour || len(options.Projects) == 0 || !validUniqueValues(options.Projects) || !validUniqueValues(options.Scopes) || !validUniqueValues(options.Credentials) {
		return fmt.Errorf("invalid bootstrap key options")
	}
	allowedScopes := map[string]bool{"jobs:submit": true, "jobs:read": true, "jobs:cancel": true, "jobs:extend": true, "jobs:control": true, "jobs:retry": true, "tasks:submit": true, "tasks:read": true, "tasks:cancel": true, "goals:approve": true, "goals:budget": true, "goals:constraints": true, "goals:propose": true}
	if len(options.Scopes) == 0 {
		return fmt.Errorf("at least one delegable scope is required")
	}
	for _, scope := range options.Scopes {
		if !allowedScopes[scope] {
			return fmt.Errorf("unsupported bootstrap scope %q", scope)
		}
	}
	db, err := store.Open(options.DataDir, store.ServerSchema)
	if err != nil {
		return err
	}
	defer db.Close()
	now := store.Now()
	existing, err := db.ListAPIKeys(ctx, options.Owner)
	if err != nil {
		return err
	}
	for _, key := range existing {
		if key.RevokedAt == 0 && key.ExpiresAt > now && containsString(key.Scopes, "api_keys:manage") {
			return fmt.Errorf("an active API key manager already exists for this owner")
		}
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return fmt.Errorf("generate bootstrap key: %w", err)
	}
	token := "cc_live_" + base64.RawURLEncoding.EncodeToString(secret)
	projects := append([]string(nil), options.Projects...)
	scopes := append([]string{"api_keys:manage"}, options.Scopes...)
	key := store.APIKey{ID: store.ID(), Digest: store.Hash([]byte(token)), Prefix: token[:20], Owner: options.Owner,
		Projects: projects, Scopes: scopes, Credentials: append([]string(nil), options.Credentials...), Label: "bootstrap-manager",
		CreatedAt: now, ExpiresAt: now + options.ExpiresIn.Milliseconds()}
	path, err := filepath.Abs(options.OutputPath)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create bootstrap key file: %w", err)
	}
	writeErr := file.Chmod(0600)
	if writeErr == nil {
		_, writeErr = file.WriteString(token + "\n")
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write bootstrap key file: %w", writeErr)
	}
	if err = db.CreateAPIKey(ctx, key); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("persist bootstrap key metadata: %w", err)
	}
	return json.NewEncoder(output).Encode(map[string]any{"id": key.ID, "prefix": key.Prefix, "owner": key.Owner, "projects": key.Projects, "expires_at_ms": key.ExpiresAt, "key_file": path})
}

func validUniqueValues(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
