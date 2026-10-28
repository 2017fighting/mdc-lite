// Package store owns the SQLite file: the connection pools, the embedded
// migrations, and the readiness the health endpoint reports.
//
// There is one writer and a separate read-only pool. SQLite allows one writer at
// a time; with more than one connection open the app spends its time on
// SQLITE_BUSY instead of on work (measured: 109–153 busy errors in 250
// concurrent writes at MaxOpenConns >= 2, none at 1). The pragmas therefore all
// travel in the DSN, so every pooled connection gets them — a connection-level
// `Exec("pragma …")` would only reach whichever connection ran it.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // the pure-Go driver; CGO_ENABLED=0 must stay possible
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrations is the embedded migration set, rooted where goose expects it.
var Migrations fs.FS = mustSub(migrationFS, "migrations")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Store is an open database: one write handle, one read-only pool.
type Store struct {
	path string

	write *sql.DB
	read  *sql.DB

	mu         sync.Mutex
	migrations int64
	migErr     error
}

// MigrateError reports a migration that could not be applied. It is kept so the
// health endpoint can say why it is not ready instead of only that it is not.
type MigrateError struct{ Err error }

func (e *MigrateError) Error() string { return "migrations failed: " + e.Err.Error() }
func (e *MigrateError) Unwrap() error { return e.Err }

// Open opens (creating if needed) the SQLite file at path and runs the embedded
// migrations.
//
// A migration failure does not close the store: the process must still come up
// far enough to answer /api/health with 503 db_unavailable and say why.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store: db_path is empty")
	}
	s := &Store{path: path}
	write, err := sql.Open("sqlite", writeDSN(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	if err := write.PingContext(ctx); err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s.write = write

	if err := s.migrate(ctx); err != nil {
		s.migErr = err
	}
	return s, nil
}

// writeDSN builds the writer's DSN. `_txlock=immediate` makes a write
// transaction take the write lock at BEGIN, which is what keeps a
// read-then-write transaction from upgrading into SQLITE_BUSY.
func writeDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// readDSN builds the reader's DSN: same pragmas, plus query_only so a stray write
// on the read pool fails loudly instead of taking the write lock.
func readDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "query_only(1)")
	return "file:" + path + "?" + q.Encode()
}

func (s *Store) migrate(ctx context.Context) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, s.write, Migrations)
	if err != nil {
		return &MigrateError{Err: fmt.Errorf("build migration provider: %w", err)}
	}
	if _, err := p.Up(ctx); err != nil {
		return &MigrateError{Err: err}
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return &MigrateError{Err: fmt.Errorf("migration status: %w", err)}
	}
	var applied int64
	for _, st := range statuses {
		if st.State == goose.StateApplied && st.Source != nil && st.Source.Version > applied {
			applied = st.Source.Version
		}
	}
	s.mu.Lock()
	s.migrations = applied
	s.mu.Unlock()
	return nil
}

// Path is the database file this store owns.
func (s *Store) Path() string { return s.path }

// Write returns the single-connection write handle.
func (s *Store) Write() *sql.DB { return s.write }

// Read returns the read-only pool. It is opened lazily, after migrations, so a
// reader never opens a database the migrations have not finished shaping.
func (s *Store) Read() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read != nil {
		return s.read, nil
	}
	if s.migErr != nil {
		return nil, s.migErr
	}
	db, err := sql.Open("sqlite", readDSN(s.path))
	if err != nil {
		return nil, fmt.Errorf("store: open read pool: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s.read = db
	return db, nil
}

// Migrations returns the highest applied migration version.
func (s *Store) Migrations() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.migrations
}

// Ready reports whether the database is usable right now: it opens, the
// migrations ran, and the business tables exist.
func (s *Store) Ready(ctx context.Context) error {
	s.mu.Lock()
	migErr := s.migErr
	s.mu.Unlock()
	if migErr != nil {
		return migErr
	}
	if s.write == nil {
		return errors.New("store: not open")
	}
	if err := s.write.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	var n int
	// The 7 business tables of V1 plus goose's own ledger.
	const want = 7
	err := s.write.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN
		 ('batches','tasks','task_runs','task_files','file_rows','step_runs','source_runs')`,
	).Scan(&n)
	if err != nil {
		return fmt.Errorf("store: inspect schema: %w", err)
	}
	if n != want {
		return fmt.Errorf("store: schema is incomplete: %d of %d business tables exist", n, want)
	}
	return nil
}

// Close closes both pools.
func (s *Store) Close() error {
	s.mu.Lock()
	read := s.read
	s.read = nil
	s.mu.Unlock()
	var errs []error
	if read != nil {
		errs = append(errs, read.Close())
	}
	if s.write != nil {
		errs = append(errs, s.write.Close())
	}
	return errors.Join(errs...)
}
