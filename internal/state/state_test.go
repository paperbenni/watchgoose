package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecordThenReassuredAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	s := New(path)

	want := time.Date(2026, 9, 28, 12, 34, 56, 123456789, time.UTC)
	if err := s.Record(want); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The format is one RFC3339Nano line, so that a human with `cat` and a
	// machine with Read agree on what is in there.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got, want := string(raw), want.Format(time.RFC3339Nano)+"\n"; got != want {
		t.Errorf("state file contents = %q, want %q", got, want)
	}

	got, err := s.ReassuredAt()
	if err != nil {
		t.Fatalf("ReassuredAt: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("ReassuredAt = %s, want %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("state file mode = %#o, want %#o", perm, fileMode)
	}
}

func TestRecordNeverExposesAPartialWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not stop the write")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	s := New(path)

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Record(first); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// A write that cannot even create its temporary file must leave the
	// previous reassurance exactly as it was: the whole point of the atomic
	// rename is that a reader sees the old timestamp or the new one.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := s.Record(first.Add(time.Hour))
	if err == nil {
		t.Fatal("Record into a read-only directory succeeded, want an error")
	}
	got, readErr := s.ReassuredAt()
	if readErr != nil {
		t.Fatalf("ReassuredAt after a failed Record: %v", readErr)
	}
	if !got.Equal(first) {
		t.Errorf("ReassuredAt = %s, want the untouched %s", got, first)
	}
}

func TestRecordIsAtomicUnderConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	s := New(path)

	// Distinct stamps, so a reader can tell a whole record from a mixture of
	// two of them.
	const rounds = 100
	stamps := make([]time.Time, rounds)
	for i := range stamps {
		stamps[i] = time.Date(2026, 1, 1, 0, 0, 0, i*1000, time.UTC)
	}
	if err := s.Record(stamps[0]); err != nil {
		t.Fatalf("Record: %v", err)
	}
	known := make(map[int64]bool, rounds)
	for _, s := range stamps {
		known[s.UnixNano()] = true
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := s.ReassuredAt()
				if err != nil {
					t.Errorf("a reader saw an unusable state file mid-rename: %v", err)
					return
				}
				if !known[got.UnixNano()] {
					t.Errorf("a reader saw a state file that was never written whole: %s", got.Format(time.RFC3339Nano))
					return
				}
			}
		}()
	}

	for _, stamp := range stamps[1:] {
		if err := s.Record(stamp); err != nil {
			t.Errorf("Record: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	// And no temporary file was left behind by any of it.
	assertOnlyStateFile(t, dir, "last-reassurance")
}

func TestReassuredAtOnAMissingFileIsNeverReassured(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "not-there"))

	got, err := s.ReassuredAt()
	if !errors.Is(err, ErrNotReassured) {
		t.Errorf("error = %v, want ErrNotReassured", err)
	}
	// A lost state file must never disarm the switch, so the zero time is the
	// answer as well as the error.
	if !got.IsZero() {
		t.Errorf("time = %s, want the zero time", got)
	}
	if !Stale(got, time.Now(), 20*time.Minute) {
		t.Error("a missing state file must count as stale")
	}
}

func TestReassuredAtOnAnUnusableFileIsNeverReassured(t *testing.T) {
	cases := map[string]struct {
		contents string
		wantErr  error
	}{
		"empty":           {"", ErrMalformed},
		"not a timestamp": {"soon\n", ErrMalformed},
		"truncated":       {"2026-09-28T12:34:56Z\ntra", ErrMalformed},
		"unix seconds":    {"1788012345\n", ErrMalformed},
		"two lines":       {"2026-09-28T12:34:56Z\n2026-09-28T12:35:56Z\n", ErrMalformed},
		"no date at all":  {"yesterday\n", ErrMalformed},
		"yaml by mistake": {"deadline: 20m\n", ErrMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "last-reassurance")
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s := New(path)

			got, err := s.ReassuredAt()
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
			if !got.IsZero() {
				t.Errorf("time = %s, want the zero time", got)
			}
			if !Stale(got, time.Now(), 20*time.Minute) {
				t.Error("an unusable state file must count as stale")
			}
		})
	}
}

func TestReassuredAtReportsAnUnreadableFileDistinctly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	// A directory where a file is expected: unreadable rather than malformed.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := New(path)

	got, err := s.ReassuredAt()
	if !errors.Is(err, ErrUnreadable) {
		t.Errorf("error = %v, want ErrUnreadable", err)
	}
	if !got.IsZero() {
		t.Errorf("time = %s, want the zero time", got)
	}
}

func TestStale(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	deadline := 20 * time.Minute
	cases := map[string]struct {
		last time.Time
		want bool
	}{
		"never reassured":     {time.Time{}, true},
		"just reassured":      {now.Add(-time.Second), false},
		"well within":         {now.Add(-deadline / 2), false},
		"exactly at deadline": {now.Add(-deadline), false},
		"one second past":     {now.Add(-deadline - time.Second), true},
		"long past":           {now.Add(-72 * time.Hour), true},
		// The deadline is measured from local receipt time, so a wall clock
		// that has jumped backwards cannot arm the switch.
		"clock jumped backwards": {now.Add(time.Hour), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Stale(tc.last, now, deadline); got != tc.want {
				t.Errorf("Stale(last, now, %s) = %v, want %v", deadline, got, tc.want)
			}
		})
	}
}

func TestRecordLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-reassurance")
	s := New(path)
	if err := s.Record(time.Now()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	assertOnlyStateFile(t, dir, "last-reassurance")
}

func TestPathIsReportedForLogging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-reassurance")
	if got := New(path).Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

// assertOnlyStateFile checks that a directory holds the state file and
// nothing else, which is how a littering temp file would show up.
func assertOnlyStateFile(t *testing.T, dir, want string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s holds %v, want just %q", dir, names, want)
	}
	if entries[0].Name() != want {
		t.Errorf("%s holds %q, want %q", dir, entries[0].Name(), want)
	}
	if _, err := os.Stat(filepath.Join(dir, want)); errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is missing", want)
	}
}
