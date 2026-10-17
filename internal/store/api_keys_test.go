package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestAPIKeyCreateAndLookup(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	record := APIKey{ID: "key-1", Digest: Hash([]byte("cc_live_secret")), Prefix: "cc_live_abc", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:submit"}, Label: "laptop", CreatedAt: 100, ExpiresAt: 200}
	if err = db.CreateAPIKey(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, err := db.LookupAPIKeyByDigest(context.Background(), record.Digest, 150)
	if err != nil || got.ID != record.ID || got.Owner != record.Owner || len(got.Scopes) != 1 {
		t.Fatalf("lookup=%+v err=%v", got, err)
	}
}

func TestAPIKeyRejectsMalformedMetadata(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := APIKey{ID: "bad", Digest: Hash([]byte("key")), Prefix: "cc_live_bad", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, CreatedAt: 100, ExpiresAt: 200}
	for name, mutate := range map[string]func(*APIKey){
		"digest":              func(k *APIKey) { k.Digest = "plaintext" },
		"empty projects":      func(k *APIKey) { k.Projects = nil },
		"duplicate scope":     func(k *APIKey) { k.Scopes = []string{"jobs:read", "jobs:read"} },
		"expired at creation": func(k *APIKey) { k.ExpiresAt = k.CreatedAt },
	} {
		t.Run(name, func(t *testing.T) {
			key := base
			key.Projects = append([]string(nil), base.Projects...)
			key.Scopes = append([]string(nil), base.Scopes...)
			mutate(&key)
			if err := db.CreateAPIKey(context.Background(), key); err == nil {
				t.Fatal("malformed API key accepted")
			}
		})
	}
}

func TestAPIKeyExpiredAndRevokedNotFound(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	expired := APIKey{ID: "expired", Digest: Hash([]byte("expired")), Prefix: "cc_live_exp", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, CreatedAt: 100, ExpiresAt: 200}
	revoked := APIKey{ID: "revoked", Digest: Hash([]byte("revoked")), Prefix: "cc_live_rev", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, CreatedAt: 100, ExpiresAt: 300}
	if err = db.CreateAPIKey(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateAPIKey(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeAPIKey(ctx, "alice", revoked.ID, 150); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{expired.Digest, revoked.Digest} {
		if _, err = db.LookupAPIKeyByDigest(ctx, digest, 200); err != sql.ErrNoRows {
			t.Fatalf("digest lookup err=%v, want not found", err)
		}
	}
}

func TestAPIKeyRevokeRequiresOwner(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := APIKey{ID: "owned", Digest: Hash([]byte("owned")), Prefix: "cc_live_own", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, CreatedAt: 100, ExpiresAt: 300}
	if err = db.CreateAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeAPIKey(context.Background(), "mallory", key.ID, 150); err != sql.ErrNoRows {
		t.Fatalf("revoke err=%v, want not found", err)
	}
}

func TestAPIKeyListOmitsDigest(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := APIKey{ID: "listed", Digest: Hash([]byte("listed-secret")), Prefix: "cc_live_lis", Owner: "alice", Projects: []string{"p"}, Scopes: []string{"jobs:read"}, CreatedAt: 100, ExpiresAt: 300}
	if err = db.CreateAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	keys, err := db.ListAPIKeys(context.Background(), "alice")
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	if keys[0].Digest != "" {
		t.Fatal("key list exposed digest")
	}
}

func TestServerV19APIMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	var version, tables int
	if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != ServerVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = db.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='api_keys'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("api_keys=%d err=%v", tables, err)
	}
	var integrity string
	if err = db.SQL.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	rows, err := db.SQL.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		t.Fatal("foreign key violation after v19 migration")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerV18UpgradePreservesJobs(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec(`INSERT INTO jobs(id,owner,project,idem,request_hash,spec_hash,spec,mode,state,created,updated,deadline,parallelism) VALUES('keep','alice','p','k','h','h','{}','single','SUCCEEDED',1,1,10,1); PRAGMA user_version=18; DROP TABLE api_keys`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, version int
	if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != ServerVersion {
		t.Fatalf("upgraded version=%d err=%v", version, err)
	}
	if err = db.SQL.QueryRow("SELECT count(*) FROM jobs WHERE id='keep'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("preserved jobs=%d err=%v", count, err)
	}
}
