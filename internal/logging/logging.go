// Package logging writes Ghost's log file: errors in full and the actions
// Ghost takes, so text the TUI has to cut short can still be read.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// MaxSize is when the log rotates: the current file becomes <path>.1
// (replacing any older copy) and a new file starts.
const MaxSize = 5 << 20

// Open returns a logger that appends to path, creating its directory. An
// empty path returns a logger that discards everything. Close the returned
// io.Closer when done. Records use slog's text format, which escapes control
// characters, so a hostile message can't mess up a terminal tailing the log.
func Open(path string) (*slog.Logger, io.Closer, error) {
	if path == "" {
		return slog.New(slog.DiscardHandler), io.NopCloser(nil), nil
	}
	w, err := openRotating(path, MaxSize)
	if err != nil {
		return nil, nil, err
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})), w, nil
}

// rotatingFile is an append-only file that rotates once it would grow past max.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotating(path string, max int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &rotatingFile{path: path, max: max}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	return w.open()
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
