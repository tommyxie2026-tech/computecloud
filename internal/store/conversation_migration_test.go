package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestConversationRequestMigrationAndImmutableReplay(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 20 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	_, err = db.SQL.ExecContext(context.Background(), `INSERT INTO conversation_requests(request_id,owner,profile_id,protocol,idem_key,request_hash,profile_hash,request_blob,state,created,updated) VALUES('r','o','p','messages','k','h','ph',X'01','PENDING',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec("UPDATE conversation_requests SET request_hash='changed' WHERE request_id='r'"); err == nil {
		t.Fatal("expected immutable request data")
	}
}

func TestV18AddsConversationRequestBytes(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{ServerSchema, serverV2, serverV3, serverV4, serverV5, serverV6, serverV7, serverV8, serverV9, serverV10, serverV11, serverV12, serverV13, serverV14, serverV15, serverV16, serverV17} {
		if _, err = raw.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec(`INSERT INTO conversation_requests(request_id,owner,profile_id,protocol,idem_key,request_hash,profile_hash,request_blob,state,created,updated) VALUES('old','owner','profile','messages','key','hash','profile-hash',X'01','PENDING',1,1); PRAGMA user_version=17`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err = db.SQL.QueryRow("SELECT request_bytes FROM conversation_requests WHERE request_id='old'").Scan(&bytes); err != nil || bytes != 0 {
		t.Fatalf("old request_bytes=%d err=%v", bytes, err)
	}
	if _, err = db.SQL.Exec("INSERT INTO conversation_requests(request_id,owner,profile_id,protocol,idem_key,request_hash,profile_hash,request_blob,state,created,updated,request_bytes) VALUES('new','owner','profile','messages','key-new','hash','profile-hash',X'01','PENDING',2,2,123)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec("UPDATE conversation_requests SET request_bytes=124 WHERE request_id='new'"); err == nil {
		t.Fatal("request_bytes must be immutable")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir, ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.SQL.QueryRow("SELECT request_bytes FROM conversation_requests WHERE request_id='new'").Scan(&bytes); err != nil || bytes != 123 {
		t.Fatalf("reopened request_bytes=%d err=%v", bytes, err)
	}
}
