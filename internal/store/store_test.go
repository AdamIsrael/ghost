package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesParentDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "ghost.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		v, err := s.SchemaVersion()
		if err != nil {
			t.Fatal(err)
		}
		if v != len(migrations) {
			t.Errorf("open #%d: version = %d, want %d", i+1, v, len(migrations))
		}
		s.Close()
	}
}

func TestSchemaSupportsReviewFlow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-10-09T10:00:00Z"
	res, err := s.db.Exec(`INSERT INTO prs (repo, number, title, updated_at) VALUES ('o/r', 1, 't', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	prID, _ := res.LastInsertId()
	res, err = s.db.Exec(`INSERT INTO reviews (pr_id, head_sha, agent, state, cost_usd, created_at) VALUES (?, 'abc', 'a', 'done', 0.42, ?)`, prID, now)
	if err != nil {
		t.Fatal(err)
	}
	revID, _ := res.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO audit_events (pr_id, review_id, kind, created_at) VALUES (?, ?, 'posted', ?)`, prID, revID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO prs (repo, number, updated_at) VALUES ('o/r', 1, ?)`, now); err == nil {
		t.Error("duplicate (repo, number) should violate UNIQUE")
	}
	if _, err := s.db.Exec(`INSERT INTO reviews (pr_id, head_sha, agent, state, created_at) VALUES (999, 'x', 'a', 'queued', ?)`, now); err == nil {
		t.Error("review for missing PR should violate foreign key")
	}
}

func TestOpenPathWithURIMetacharacters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new dir", "a?b#c.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("db not created at exact path: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), filepath.Base(p)) {
			t.Errorf("sqlite opened a different file: %s", e.Name())
		}
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("want error about newer schema, got %v", err)
	}
}

func TestOpenRestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	p := filepath.Join(dir, "ghost.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for path, want := range map[string]os.FileMode{dir: 0o700, p: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}
