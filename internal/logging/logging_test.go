package logging

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWritesAndCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ghost", "ghost.log")
	log, closer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("started", "version", "1.2.3")
	log.Error("poll failed", "view", "Team", "err", "search: HTTP 502")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "level=INFO msg=started version=1.2.3") || !strings.Contains(s, `level=ERROR msg="poll failed" view=Team err="search: HTTP 502"`) {
		t.Errorf("log =\n%s", s)
	}
}

func TestOpenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.log")
	for i := 0; i < 2; i++ {
		log, closer, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		log.Info("run")
		closer.Close()
	}
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "msg=run"); n != 2 {
		t.Errorf("runs logged = %d, want 2 (append, not truncate)", n)
	}
}

func TestEmptyPathDiscards(t *testing.T) {
	log, closer, err := Open("")
	if err != nil || log == nil || closer == nil {
		t.Fatalf("Open(\"\") = %v, %v, %v", log, closer, err)
	}
	log.Error("nowhere")
	closer.Close()
}

func TestControlCharactersAreEscaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.log")
	log, closer, _ := Open(path)
	log.Warn("posted", "detail", "evil\x1b[31mred\nfake=line\r")
	closer.Close()
	data, _ := os.ReadFile(path)
	if bytes.ContainsAny(data, "\x1b\r") || bytes.Count(data, []byte("\n")) != 1 {
		t.Errorf("control characters reached the file: %q", data)
	}
}

func TestRotatesAtMaxSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.log")
	w, err := openRotating(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 59) + "\n" // 60 bytes
	for i := 0; i < 4; i++ {               // 240 bytes: the 4th write rotates
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	cur, _ := os.ReadFile(path)
	old, err := os.ReadFile(path + ".1")
	if err != nil || len(old) != 180 || len(cur) != 60 {
		t.Errorf("after rotation: current %d bytes, .1 %d bytes (err %v); want 60 and 180", len(cur), len(old), err)
	}
	// An existing large file rotates on the first write after reopening.
	w, _ = openRotating(path, 50)
	w.Write([]byte(line))
	w.Close()
	if cur, _ := os.ReadFile(path); len(cur) != 60 {
		t.Errorf("reopened over the cap: current %d bytes, want 60", len(cur))
	}
}

func TestRotationSurvivesDeletedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.log")
	w, err := openRotating(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	line := []byte(strings.Repeat("x", 59) + "\n")
	w.Write(line)
	os.Remove(path) // someone clears the log while Ghost runs
	for i := 0; i < 3; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write %d after the file was deleted: %v", i, err)
		}
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Errorf("logging stopped after the file was deleted: %v, %d bytes", err, len(data))
	}
}

func TestLogDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "ghost")
	_, closer, err := Open(filepath.Join(dir, "ghost.log"))
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("log dir mode = %v, want 0700", st.Mode().Perm())
	}
}

func TestWriteAfterCloseIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.log")
	w, _ := openRotating(path, 1000)
	w.Write([]byte("before\n"))
	w.Close()
	if _, err := w.Write([]byte("after\n")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after Close: err = %v, want os.ErrClosed", err)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "after") {
		t.Errorf("a write after Close reopened the file: %q", data)
	}
}
