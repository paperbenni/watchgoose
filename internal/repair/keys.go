package repair

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MergeKeys returns existing with want appended, in that order.
//
// This is additive by construction: nothing in existing is removed, reordered
// or rewritten. An ordinary account's authorized_keys can hold keys belonging
// to other people, and a repair that tidied the file would lock them out of
// this machine. Keys already present are not repeated, so running repair
// twice does not grow the file.
//
// The one thing that is dropped is an empty line, which sshd ignores anyway.
// Whitespace around a key is trimmed, since sshd trims it too and an
// untrimmed duplicate would otherwise be appended as a second copy of a key
// that is already there.
func MergeKeys(existing, want []string) []string {
	merged := make([]string, 0, len(existing)+len(want))
	seen := make(map[string]bool, len(existing)+len(want))
	for _, group := range [][]string{existing, want} {
		for _, key := range group {
			key = strings.TrimSpace(key)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, key)
		}
	}
	return merged
}

// renderKeys writes one key per line, which is the only shape sshd's
// authorized_keys parser accepts. The trailing newline is required.
func renderKeys(keys []string) string {
	var b strings.Builder
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		b.WriteString(key)
		b.WriteByte('\n')
	}
	return b.String()
}

// readKeys returns the non-empty lines of a keys file. A file that is not
// there is reported as fs.ErrNotExist so that callers can tell "nothing there
// yet" from "something there that cannot be read", which must never be
// overwritten.
func readKeys(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			keys = append(keys, line)
		}
	}
	return keys, nil
}

// writeFileAtomic replaces a file's contents atomically, at the given mode. The
// temporary file is created in the same directory so that the rename cannot
// cross a filesystem boundary, which is what makes it atomic.
func writeFileAtomic(path, contents string, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	// CreateTemp already creates at 0600, but the mode is stated anyway: this
	// is an authorized_keys file, and a key file that anyone can write is a
	// key file that sshd will refuse.
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
