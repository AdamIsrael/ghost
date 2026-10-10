// Package store persists PRs, reviews and the audit trail in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

// migrations are applied in order; PRAGMA user_version records how many ran.
// Append only — never edit a migration that has shipped.
var migrations = []string{
	`CREATE TABLE prs (
		id         INTEGER PRIMARY KEY,
		repo       TEXT    NOT NULL,
		number     INTEGER NOT NULL,
		title      TEXT    NOT NULL DEFAULT '',
		author     TEXT    NOT NULL DEFAULT '',
		url        TEXT    NOT NULL DEFAULT '',
		head_sha   TEXT    NOT NULL DEFAULT '',
		status     TEXT    NOT NULL DEFAULT '',
		updated_at TEXT    NOT NULL,
		UNIQUE (repo, number)
	);
	CREATE TABLE reviews (
		id          INTEGER PRIMARY KEY,
		pr_id       INTEGER NOT NULL REFERENCES prs(id),
		head_sha    TEXT    NOT NULL,
		agent       TEXT    NOT NULL,
		state       TEXT    NOT NULL,
		prompt      TEXT    NOT NULL DEFAULT '',
		verdict     TEXT    NOT NULL DEFAULT '',
		summary     TEXT    NOT NULL DEFAULT '',
		cost_usd    REAL    NOT NULL DEFAULT 0,
		created_at  TEXT    NOT NULL,
		finished_at TEXT
	);
	CREATE INDEX reviews_pr_sha ON reviews (pr_id, head_sha);
	CREATE INDEX reviews_created ON reviews (created_at);
	CREATE TABLE audit_events (
		id         INTEGER PRIMARY KEY,
		pr_id      INTEGER NOT NULL REFERENCES prs(id),
		review_id  INTEGER REFERENCES reviews(id),
		kind       TEXT    NOT NULL,
		message    TEXT    NOT NULL DEFAULT '',
		created_at TEXT    NOT NULL
	);
	CREATE INDEX audit_pr ON audit_events (pr_id, created_at);`,
	`ALTER TABLE reviews ADD COLUMN comments TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE reviews ADD COLUMN error TEXT NOT NULL DEFAULT '';
	CREATE INDEX reviews_state ON reviews (state, created_at);
	CREATE INDEX reviews_finished ON reviews (finished_at);`,
}

// Open opens (creating if needed) the database at path and applies migrations.
// The database holds private PR content, so new files are owner-only.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	dsn := url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
	}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

func (s *Store) migrate() error {
	v, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this ghost supports (%d); upgrade ghost", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SetNow replaces the clock (tests).
func (s *Store) SetNow(now func() time.Time) { s.now = now }
