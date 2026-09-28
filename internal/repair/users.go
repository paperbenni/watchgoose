package repair

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var errAccountMissing = errors.New("account not found")

// passwdEntry is the part of a passwd record that repair cares about.
type passwdEntry struct {
	Name  string
	UID   int
	GID   int
	Home  string
	Shell string
}

// isNologin reports whether this shell makes the account unloggable
// interactively. A recovery user with such a shell is not a recovery user, and
// an ordinary account with one is not loggable by a person at all.
func (e passwdEntry) isNologin() bool {
	switch filepath.Base(e.Shell) {
	case "nologin", "false":
		return true
	default:
		return false
	}
}

// lookup resolves an account name through NSS, so that a directory-backed or
// otherwise exotic account is handled the same way as a local one.
func (a *actor) lookup(name string) (passwdEntry, bool, []string) {
	out, err := a.run("getent", "passwd", name)
	record := firstLine(out)
	if record == "" {
		var exitErr *exec.ExitError
		if errors.Is(err, errAccountMissing) || errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return passwdEntry{}, false, nil
		}
		msg := fmt.Sprintf("could not determine whether account %s exists: getent produced no record: %v", name, err)
		a.log.Warn("repair: " + msg)
		return passwdEntry{}, false, []string{msg}
	}
	if err != nil {
		a.log.Debug("getent reported an error but printed a record", "account", name, "output", oneline(out), "error", err)
	}
	fields := strings.Split(record, ":")
	if len(fields) < 7 {
		return passwdEntry{}, false, []string{fmt.Sprintf("the passwd record for %s is malformed and was left alone: %q", name, oneline(record))}
	}
	uid, uerr := strconv.Atoi(fields[2])
	gid, gerr := strconv.Atoi(fields[3])
	if uerr != nil || gerr != nil {
		return passwdEntry{}, false, []string{fmt.Sprintf("the passwd record for %s has a non-numeric id and was left alone: %q", name, oneline(record))}
	}
	return passwdEntry{
		Name:  fields[0],
		UID:   uid,
		GID:   gid,
		Home:  fields[5],
		Shell: fields[6],
	}, true, nil
}

// isLocked reports whether shadow is hiding a real password behind a lock — a
// password field of "!" followed by a hash, which is what usermod -U clears.
// The second return value is false when the answer could not be read at all, in
// which case the caller should not claim to know either way.
//
// A bare "!" is deliberately not locked. That is what useradd writes for an
// account created without a password, and it is the correct state for a
// key-only account: there is no password to hide and none to unlock. shadow's
// usermod agrees, and refuses to act on it:
//
//	usermod: unlocking the user's password would result in a passwordless
//	account. You should set a password with usermod -p to unlock this
//	user's password.
//
// Treating a bare "!" as locked would therefore make every repair attempt an
// unlock that is refused, and record a warning each time. Since these accounts
// are locked by design, that would be the normal case reporting itself as a
// fault, which teaches everyone to ignore the warnings that matter.
func (a *actor) isLocked(name string) (locked, known bool) {
	out, err := a.run("getent", "shadow", name)
	record := firstLine(out)
	if err != nil && record == "" {
		return false, false
	}
	fields := strings.Split(record, ":")
	if len(fields) < 2 {
		return false, false
	}
	// A "*" password is unusable but is not a lock, and an empty one is a
	// passwordless account rather than a locked one. Only a lock in front of an
	// actual hash is a lock worth clearing.
	return len(fields[1]) > 1 && strings.HasPrefix(fields[1], "!"), true
}

// mkdir creates a directory and forces its mode, whether or not it was already
// there. A directory that already exists with the wrong mode is a bug worth
// fixing: sshd refuses to read authorized_keys out of a group-writable .ssh.
func (a *actor) mkdir(path string, mode fs.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("mkdir -p %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// chown hands a path to its owner by numeric id. Numeric, not by name, because
// the name lookup is the very thing that just failed in some cases.
func (a *actor) chown(path string, e passwdEntry) error {
	if _, err := a.run("chown", fmt.Sprintf("%d:%d", e.UID, e.GID), path); err != nil {
		return fmt.Errorf("chown %s %d:%d: %w", path, e.UID, e.GID, err)
	}
	return nil
}

// ensureHome makes sure the account has a home directory to work in. An
// ordinary account whose home has been deleted is a warning rather than
// something to churn about, so a home that already exists is left exactly as
// it is found.
func (a *actor) ensureHome(e passwdEntry) error {
	if e.Home == "" {
		return errors.New("the passwd record has no home directory")
	}
	if _, err := os.Stat(e.Home); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", e.Home, err)
	}
	a.log.Warn("home directory is missing; creating it so the keys have somewhere to go",
		"account", e.Name, "home", e.Home)
	if err := a.mkdir(e.Home, homeMode); err != nil {
		return err
	}
	return a.chown(e.Home, e)
}

// ensureSSHDir prepares <home>/.ssh with the mode and ownership sshd insists
// on: 0700, owned by the account, never writable by anyone else.
func (a *actor) ensureSSHDir(home string, e passwdEntry) error {
	if home == "" {
		return errors.New("no home directory to put .ssh in")
	}
	if err := a.ensureHome(e); err != nil {
		return err
	}
	dir := sshPath(home)
	if err := a.mkdir(dir, sshDirMode); err != nil {
		return err
	}
	return a.chown(dir, e)
}

// firstLine is the first line of command output, trimmed, or "" if there is
// none. getent prints one record; the surrounding noise is other people's.
func firstLine(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(line)
}
