package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type DB struct {
	SQL  *sql.DB
	dir  string
	lock *os.File
}
type Query interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(dir, schema string) (*DB, error) { return open(dir, schema, true) }

// OpenForBackup preserves the on-disk schema and never upgrades a backup source.
func OpenForBackup(dir string) (*DB, error) { return open(dir, ServerSchema, false) }

func open(dir, schema string, migrate bool) (*DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, e := os.OpenFile(filepath.Join(dir, "instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("data directory already in use: %w", e)
	}
	fail := func(e error) (*DB, error) { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close(); return nil, e }
	db, e := sql.Open("sqlite", filepath.Join(dir, "state.db")+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(1000)&_txlock=immediate")
	if e != nil {
		return fail(e)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	target := 1
	if schema == ServerSchema {
		target = 3
	}
	if schema == WorkerSchema {
		target = 2
	}
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version > target {
		db.Close()
		if e == nil {
			e = errors.New("database schema is newer than this binary")
		}
		return fail(e)
	}
	if e = migrateSchema(db, schema, version, target, migrate); e != nil {
		db.Close()
		return fail(e)
	}
	if e = os.Chmod(filepath.Join(dir, "state.db"), 0600); e != nil {
		db.Close()
		return fail(e)
	}
	return &DB{SQL: db, dir: dir, lock: f}, nil
}
func (d *DB) Close() error {
	e := d.SQL.Close()
	unix.Flock(int(d.lock.Fd()), unix.LOCK_UN)
	return errors.Join(e, d.lock.Close())
}
func (d *DB) Tx(ctx context.Context, fn func(Query) error) error {
	tx, e := d.SQL.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func Now() int64           { return time.Now().UnixMilli() }

const ServerSchema = `
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, owner TEXT NOT NULL, project TEXT NOT NULL, idem TEXT NOT NULL, hash TEXT NOT NULL, spec BLOB NOT NULL, state TEXT NOT NULL, attempt TEXT NOT NULL DEFAULT '', worker TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL, seq INTEGER NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', native_session TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', priority INTEGER NOT NULL DEFAULT 0, deadline INTEGER NOT NULL, UNIQUE(owner,project,idem));
CREATE INDEX IF NOT EXISTS tasks_queue ON tasks(state,priority DESC,created);
CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, task TEXT UNIQUE NOT NULL REFERENCES tasks(id), worker TEXT NOT NULL, epoch TEXT NOT NULL, generation INTEGER NOT NULL, token TEXT NOT NULL, lease_until INTEGER NOT NULL, released INTEGER NOT NULL DEFAULT 0, final_hash TEXT NOT NULL DEFAULT '', worker_seq INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS workers(id TEXT PRIMARY KEY, epoch TEXT NOT NULL, hello BLOB NOT NULL, seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY, task TEXT NOT NULL, attempt TEXT NOT NULL, worker TEXT NOT NULL, kind TEXT NOT NULL, body BLOB NOT NULL, acked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS commands_delivery ON commands(worker,acked);
CREATE TABLE IF NOT EXISTS controls(task TEXT NOT NULL, id TEXT NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(task,id));
CREATE TABLE IF NOT EXISTS events(task TEXT NOT NULL REFERENCES tasks(id), seq INTEGER NOT NULL, attempt TEXT NOT NULL, worker_seq INTEGER, id TEXT NOT NULL, hash TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(task,seq), UNIQUE(attempt,worker_seq), UNIQUE(task,id));
CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY, task TEXT NOT NULL REFERENCES tasks(id), attempt TEXT NOT NULL, kind TEXT NOT NULL, hash TEXT NOT NULL, size INTEGER NOT NULL, path TEXT NOT NULL);
`
const WorkerSchema = `
CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, assignment BLOB NOT NULL, state TEXT NOT NULL, pid INTEGER NOT NULL DEFAULT 0, start_id TEXT NOT NULL DEFAULT '', stop INTEGER NOT NULL DEFAULT 0, seq INTEGER NOT NULL DEFAULT 0, ack INTEGER NOT NULL DEFAULT 0, completion BLOB, uploaded INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY, hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events(attempt TEXT NOT NULL, seq INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY(attempt,seq));
`
