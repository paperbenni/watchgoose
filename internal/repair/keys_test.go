package repair

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Ordinary account: additive. Other people's keys must survive, in the order
// their owners left them.
func TestMergeKeysIsAdditive(t *testing.T) {
	cases := map[string]struct {
		existing []string
		want     []string
		expect   []string
	}{
		"into an empty file": {
			existing: nil,
			want:     []string{"ssh-ed25519 AAAA mine", "ssh-rsa BBBB mine-too"},
			expect:   []string{"ssh-ed25519 AAAA mine", "ssh-rsa BBBB mine-too"},
		},
		"onto somebody else's keys": {
			existing: []string{"ssh-ed25519 AAAA colleague", "ssh-rsa BBBB laptop"},
			want:     []string{"ssh-ed25519 CCCC mine"},
			expect:   []string{"ssh-ed25519 AAAA colleague", "ssh-rsa BBBB laptop", "ssh-ed25519 CCCC mine"},
		},
		"a key that is already there is not doubled": {
			existing: []string{"ssh-ed25519 AAAA mine"},
			want:     []string{"ssh-ed25519 AAAA mine"},
			expect:   []string{"ssh-ed25519 AAAA mine"},
		},
		"whitespace around a key does not make it a new one": {
			existing: []string{"  ssh-ed25519 AAAA mine  "},
			want:     []string{"ssh-ed25519 AAAA mine"},
			expect:   []string{"ssh-ed25519 AAAA mine"},
		},
		"blank lines are dropped": {
			existing: []string{"", "ssh-ed25519 AAAA mine", "   "},
			want:     []string{""},
			expect:   []string{"ssh-ed25519 AAAA mine"},
		},
		"the existing order is never changed": {
			existing: []string{"z", "y", "x"},
			want:     []string{"a", "b"},
			expect:   []string{"z", "y", "x", "a", "b"},
		},
		"nothing wanted, nothing done": {
			existing: []string{"ssh-ed25519 AAAA colleague"},
			want:     nil,
			expect:   []string{"ssh-ed25519 AAAA colleague"},
		},
		"nothing there and nothing wanted": {
			existing: nil,
			want:     nil,
			expect:   []string{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := MergeKeys(tc.existing, tc.want)
			if !reflect.DeepEqual(got, tc.expect) {
				t.Errorf("MergeKeys(%q, %q) = %q, want %q", tc.existing, tc.want, got, tc.expect)
			}
		})
	}
}

// MergeKeys must not modify the slice it was handed: the caller may be about
// to compare it against what was on disk.
func TestMergeKeysDoesNotModifyItsArguments(t *testing.T) {
	existing := []string{"ssh-ed25519 AAAA colleague"}
	want := []string{"ssh-ed25519 CCCC mine"}
	_ = MergeKeys(existing, want)

	if len(existing) != 1 || existing[0] != "ssh-ed25519 AAAA colleague" {
		t.Errorf("existing was modified: %q", existing)
	}
	if len(want) != 1 || want[0] != "ssh-ed25519 CCCC mine" {
		t.Errorf("want was modified: %q", want)
	}
}

func TestMergeKeysIsIdempotent(t *testing.T) {
	want := []string{"ssh-ed25519 AAAA mine", "ssh-rsa BBBB colleague"}
	once := MergeKeys([]string{"ssh-ed25519 CCCC other"}, want)
	twice := MergeKeys(once, want)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("repairing twice changed the file: %q then %q", once, twice)
	}
}

func TestRenderKeys(t *testing.T) {
	cases := map[string]struct {
		keys   []string
		expect string
	}{
		"one key":            {[]string{"ssh-ed25519 AAAA mine"}, "ssh-ed25519 AAAA mine\n"},
		"several keys":       {[]string{"a", "b"}, "a\nb\n"},
		"no keys":            {nil, ""},
		"blank keys skipped": {[]string{"a", "", "  ", "b"}, "a\nb\n"},
		"whitespace trimmed": {[]string{"  a  ", "b\n"}, "a\nb\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := renderKeys(tc.keys); got != tc.expect {
				t.Errorf("renderKeys(%q) = %q, want %q", tc.keys, got, tc.expect)
			}
		})
	}
}

func TestWriteKeysReplacesTheFileWholesale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := writeFileAtomic(path, "retired retired@example\n", keysFileMode); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// The recovery user's file is replaced, not merged: a key rotated out of
	// the configuration has to stop working.
	if err := writeFileAtomic(path, renderKeys([]string{"mine mine@example"}), keysFileMode); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "mine mine@example\n"; string(got) != want {
		t.Errorf("authorized_keys = %q, want exactly %q", got, want)
	}
	assertMode(t, path, 0o600)
	assertNoTemporaries(t, filepath.Dir(path))
}

func TestWriteKeysCreatesNothingLeftBehindAndKeepsTheMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watchgoose-recovery")
	if err := writeFileAtomic(path, "recovery ALL=(ALL:ALL) NOPASSWD: ALL\n", sudoersMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertMode(t, path, 0o440)

	// Rewriting must keep the mode even though the file now exists, and must
	// not leave a temporary file where sudo's @includedir would find it.
	if err := writeFileAtomic(path, "recovery ALL=(ALL:ALL) NOPASSWD: ALL\n", sudoersMode); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	assertMode(t, path, 0o440)
	assertNoTemporaries(t, dir)
}

func TestWriteKeysReportsAMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "authorized_keys")
	if err := writeFileAtomic(path, "x\n", keysFileMode); err == nil {
		t.Fatal("writeKeys invented a directory that is not there")
	}
}

func TestReadKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte("a\n\nb\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := readKeys(path)
	if err != nil {
		t.Fatalf("readKeys: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("readKeys = %q, want %q", got, want)
	}

	// A file that is not there is a distinct answer from an unreadable one,
	// because only the first is safe to create.
	if _, err := readKeys(filepath.Join(dir, "absent")); !os.IsNotExist(err) {
		t.Errorf("readKeys on a missing file = %v, want a not-exist error", err)
	}
}

func TestKeysPathIsInsideTheAccountHome(t *testing.T) {
	got := keysPathFor("/home/benjamin")
	if want := "/home/benjamin/.ssh/authorized_keys"; got != want {
		t.Errorf("keysPathFor = %q, want %q", got, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s is %#o, want %#o", path, got, want)
	}
}

func assertNoTemporaries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("%s was left behind in %s", e.Name(), dir)
		}
	}
}
