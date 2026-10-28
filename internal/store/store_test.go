package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/store"
)

func open(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("a freshly opened store must be ready: %v", err)
	}
	return s, ctx
}

func tableNames(t *testing.T, ctx context.Context, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

func TestOpen_CreatesTheSevenBusinessTablesPlusGooseLedger(t *testing.T) {
	s, ctx := open(t)
	tables := tableNames(t, ctx, s.Write())
	for _, want := range []string{
		"batches", "tasks", "task_runs", "task_files", "file_rows", "step_runs", "source_runs",
	} {
		if !tables[want] {
			t.Errorf("table %q is missing", want)
		}
	}
	if !tables["goose_db_version"] {
		t.Errorf("goose_db_version is missing")
	}
	if s.Migrations() < 1 {
		t.Errorf("migrations = %d, want at least 1", s.Migrations())
	}
}

func TestOpen_PragmasAreEffectiveOnBothPools(t *testing.T) {
	s, ctx := open(t)
	for name, db := range map[string]*sql.DB{"write": s.Write()} {
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" {
			t.Errorf("%s journal_mode = %q, want wal", name, mode)
		}
		var busy int
		if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if busy != 5000 {
			t.Errorf("%s busy_timeout = %d, want 5000", name, busy)
		}
		var sync int
		if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if sync != 1 { // 1 = NORMAL
			t.Errorf("%s synchronous = %d, want 1 (NORMAL)", name, sync)
		}
	}

	read, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := read.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("read pool sees journal_mode = %q", mode)
	}
}

func TestWritePoolIsOneConnection(t *testing.T) {
	s, _ := open(t)
	if got := s.Write().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("write MaxOpenConnections = %d, want 1", got)
	}
}

func TestReadPoolIsReadOnly(t *testing.T) {
	s, ctx := open(t)
	read, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := read.Stats().MaxOpenConnections; got != 4 {
		t.Errorf("read MaxOpenConnections = %d, want 4", got)
	}
	if _, err := read.ExecContext(ctx, `INSERT INTO batches (trigger, pipeline_id, status, started_at) VALUES ('manual','main','running','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("the read pool must refuse writes")
	}
}

func TestOpen_ReopeningKeepsTheMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mdc.db")
	ctx := context.Background()

	s1, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v := s1.Migrations()
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if s2.Migrations() != v {
		t.Errorf("migrations after reopen = %d, want %d (Up is idempotent)", s2.Migrations(), v)
	}
	if err := s2.Ready(ctx); err != nil {
		t.Errorf("reopened store must be ready: %v", err)
	}
}

func TestOpen_UnusablePathIsAnError(t *testing.T) {
	// A directory can never be a SQLite file.
	dir := t.TempDir()
	if _, err := store.Open(context.Background(), dir); err == nil {
		t.Fatal("opening a directory as the database must fail")
	}
}

func TestReady_ReportsMissingSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.db")
	// A plain SQLite file with no schema: Open would migrate it, so open the raw
	// handle first and check Ready on a store that never migrated.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE unrelated (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Point the store at a database whose migration ledger says "done" but whose
	// tables are absent: the health check must not trust the ledger alone.
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Open runs the real migrations, so the schema is complete here; drop one
	// table to model a half-applied database.
	if _, err := s.Write().ExecContext(ctx, `DROP TABLE source_runs`); err != nil {
		t.Fatal(err)
	}
	err = s.Ready(ctx)
	if err == nil {
		t.Fatal("Ready must fail when a business table is missing")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("unexpected error: %v", err)
	}
}
