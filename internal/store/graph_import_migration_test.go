package store

import "testing"

func TestImportMigrationPreservesV21History(t *testing.T) {
	dir := t.TempDir()
	db, e := Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL.Exec(`DROP TABLE iu_import_snapshots; DROP TABLE iu_import_grant_state; DROP TABLE iu_import_grants; PRAGMA user_version=21;`); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(dir, ServerSchema)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	if e = db.SQL.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil || version != ServerVersion {
		t.Fatal(version, e)
	}
	rows, e := db.SQL.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
