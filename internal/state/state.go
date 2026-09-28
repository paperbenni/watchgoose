// Package state holds the local time at which reassurance last arrived. Before
// the first poke, it holds an explicit waiting marker written at installation.
//
// It is on disk rather than in memory so that a crash and a respawn cannot be
// mistaken for silence (see docs/adr/0005-single-daemon.md). There is no
// post-reboot latch, counter or budget: a switch that
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
	// AwaitingFirstPoke is written only during installation. A missing file
	// remains a failure after installation, rather than silently disarming an
	// already active switch.
	AwaitingFirstPoke = "awaiting-first-reassurance"
	// fileMode is the mode the state file is created with. It is root's
	// business only; the client reaches the switch over HTTP and never reads
	// this file.
	fileMode fs.FileMode = 0o600
	// Layout is the on-disk format: one RFC3339 timestamp to nanosecond
	// precision, on a single line.
	Layout = time.RFC3339Nano
)

// Callers distinguish the explicit first-poke marker from missing, unreadable
// or malformed state. Only the explicit marker holds repair and reboot.
var (
	// ErrNotReassured means there is no state file at all.
	ErrNotReassured      = errors.New("no reassurance recorded yet")
	ErrAwaitingFirstPoke = errors.New("waiting for the first reassurance")
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
// together with a non-nil error, and callers must treat that as stale. The
// explicit first-poke marker returns ErrAwaitingFirstPoke instead. This
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
	if strings.TrimSpace(string(raw)) == AwaitingFirstPoke {
		return time.Time{}, fmt.Errorf("%w: %s", ErrAwaitingFirstPoke, s.path)
	}
	stamp, perr := time.Parse(Layout, strings.TrimSpace(string(raw)))
	if perr != nil {
		return time.Time{}, fmt.Errorf("%w: %s: %w", ErrMalformed, s.path, perr)
	}
	return stamp, nil
}

// InitializeFirstPoke marks a new installation as waiting for its first poke.
// O_EXCL preserves any recorded reassurance or damaged state on reinstall.
func (s *Store) InitializeFirstPoke() (bool, error) {
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("initialize reassurance state: %w", err)
	}
	if _, err := f.WriteString(AwaitingFirstPoke + "\n"); err != nil {
		f.Close()
		return false, fmt.Errorf("write initial reassurance state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, fmt.Errorf("sync initial reassurance state: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close initial reassurance state: %w", err)
	}
	return true, nil
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
