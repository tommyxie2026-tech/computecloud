package store

import (
	"context"
	"testing"
)

func TestConversationRequestMigrationAndImmutableReplay(t *testing.T) {
	db, err := Open(t.TempDir(), ServerSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.SQL.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	_, err = db.SQL.ExecContext(context.Background(), `INSERT INTO conversation_requests(request_id,owner,profile_id,protocol,idem_key,request_hash,request_blob,state,created,updated) VALUES('r','o','p','messages','k','h',X'01','PENDING',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL.Exec("UPDATE conversation_requests SET request_hash='changed' WHERE request_id='r'"); err == nil {
		t.Fatal("expected immutable request data")
	}
}
