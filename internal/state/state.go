// Package state holds the one piece of state the switch persists: the local
// time at which reassurance last arrived.
//
// It is on disk rather than in memory so that a crash and a respawn cannot be
// mistaken for silence (see docs/adr/0005-single-daemon.md). Nothing else is
// persisted. There is no latch, no counter and no budget: a switch that
// re-arms on every boot needs no state of its own
// (see docs/adr/0001-accept-unattended-reboot-loops.md).
package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// fileMode is the mode the state file is created with. It is root's
	// business only; the client reaches the switch over HTTP and never reads
	// this file.
	fileMode fs.FileMode = 0o600
	// Layout is the on-disk format: one RFC3339 timestamp to nanosecond
	// precision, on a single line.
	Layout = time.RFC3339Nano
)

// The three ways there can be no reassurance to report. Callers distinguish
// them because they mean different things: not-yet-installed is expected at
// first boot, whereas unreadable and malformed both mean the machine may have
// lost its state and must be treated as having never been reassured.
var (
	// ErrNotReassured means there is no state file at all.
	ErrNotReassured = errors.New("no reassurance recorded yet")
	// ErrUnreadable means there is a state file that cannot be read.
	ErrUnreadable = errors.New("state file is unreadable")
	// ErrMalformed means the state file does not contain a timestamp.
	ErrMalformed = errors.New("state file is malformed")
)

// Store reads and writes the reassurance timestamp at a fixed path.
type Store struct {
	path string
}

// New returns a Store backed by path. It does not touch the filesystem;
// nothing is created until Record is called.
func New(path string) *Store {
	return &Store{path: path}
}

// Path is the file the timestamp lives in, for logging and for /health.
func (s *Store) Path() string { return s.path }

// ReassuredAt returns the time of the last reassurance.
//
// A missing, unreadable or malformed state file is reported as the zero time
// together with a non-nil error, and callers must treat that as stale. This
// is fail-safe and deliberate: a lost state file must never disarm the switch
// (see docs/adr/0001-accept-unattended-reboot-loops.md). The error is
// returned as well as the zero time only so that it can be logged distinctly
// from ordinary silence.
func (s *Store) ReassuredAt() (time.Time, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, fmt.Errorf("%w: %s", ErrNotReassured, s.path)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %w", ErrUnreadable, s.path, err)
	}
	stamp, perr := time.Parse(Layout, strings.TrimSpace(string(raw)))
	if perr != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %w", ErrMalformed, s.path, perr)
	}
	return stamp, nil
}

// Record writes t as the time of the last reassurance.
//
// The write is atomic: a temporary file in the same directory is written,
// flushed and renamed over the target, so a reader either sees the previous
// timestamp or the new one and never a half-written line. The directory is
// flushed too, so the rename survives a power cut.
func (s *Store) Record(t time.Time) (err error) {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file beside %s: %w", s.path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			// A rename that did not happen must not leave litter behind.
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.WriteString(t.Format(Layout) + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err = tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err = os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, s.path, err)
	}

	// Best effort: without this the rename can be lost to a power cut even
	// though the file contents were flushed.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Stale reports whether the reassurance deadline has passed as of now.
//
// A zero last means the machine has never been reassured. A small future
// timestamp is tolerated as clock skew. A timestamp farther ahead than one
// deadline is stale: otherwise a large backward clock correction could keep
// the switch disarmed indefinitely after all pokes stop.
func Stale(last, now time.Time, deadline time.Duration) bool {
	if last.IsZero() {
		return true
	}
	if last.After(now.Add(deadline)) {
		return true
	}
	return now.Sub(last) > deadline
}
