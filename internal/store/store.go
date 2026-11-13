// Package store keeps Discern's state in SQLite: one file, no server, and
// transactions, so a crash can never leave a half-saved job (decision D3).
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the SQLite implementation of jobs.Store.
//
// It uses a single database connection (see Open). The rule that follows:
// no Store method may run a query while it still has rows open or a
// transaction going on another query. It would wait forever for the one
// connection. Every method reads its rows completely before returning, and
// queries inside a transaction go through the transaction.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (or creates) the database at path and brings its schema up to
// date.
func Open(ctx context.Context, path string) (*Store, error) {
	// The driver takes its settings from after a '?' in the name, so a '?'
	// or '#' in the path itself would be read as settings and the pragmas
	// below silently lost.
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("database path %q must not contain '?' or '#'", path)
	}
	// WAL: readers don't block the writer, and a crash mid-write leaves the
	// database as it was before that transaction. busy_timeout: wait for a
	// lock instead of failing at once. foreign_keys: SQLite leaves them off
	// unless asked.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite allows one writer at a time anyway, and a single
	// connection rules out "database is locked" between Discern's own
	// goroutines. Discern's load (a few jobs, a progress update a second) is
	// far below what that serves.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies the files in migrations/ that the database hasn't seen.
// SQLite's user_version records how many have been applied. Each file runs in
// its own transaction with the version bump, so a migration is applied
// completely or not at all.
func (s *Store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names) // 001_..., 002_...: the order they were written
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(names) {
		return fmt.Errorf("database schema version %d is newer than this Discern knows (%d); was it opened by a newer version?", version, len(names))
	}
	for i := version; i < len(names); i++ {
		body, err := migrations.ReadFile(names[i])
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", names[i], err)
		}
		// PRAGMA takes no parameters, so the number is formatted in; it is
		// an int, never user input.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
