// Package repair restores the ability to log in, before any reboot is
// attempted. Repair is what a human would have done by hand; the reboot is a
// separate, later act.
//
// The recovery user is always repaired, because its credentials live on the
// root disk and the root disk is the only storage expected to be present when
// the volume is not. The ordinary accounts are repaired only when the volume is
// actually mounted: when it is not, /home is an empty directory on the root
// disk and anything written there is shadowed the moment the volume mounts on
// a later boot, so a repair that claims success while changing nothing that
// will ever be read is worse than an honest skip
// (see docs/adr/0004-repair-skips-accounts-when-volume-absent.md).
package repair

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"watchgoose/internal/config"
	"watchgoose/internal/mountinfo"
)

const (
	// defaultSudoersPath is where the recovery user's sudo grant lives. The
	// filename contains no dot, so sudo's @includedir in /etc/sudoers will pick
	// it up; a name with a dot would be silently ignored. It is a default
	// rather than a constant only so that tests can aim it at a temporary
	// directory; nothing configures it.
	defaultSudoersPath = "/etc/sudoers.d/watchgoose-recovery"
	// sudoersMode is required by sudo: a sudoers file that anyone but root
	// can write is refused outright.
	sudoersMode fs.FileMode = 0o440
	// homeMode, sshDirMode and keysFileMode are the modes sshd insists on: a
	// .ssh that anyone but its owner can write to, or an authorized_keys that
	// anyone but its owner can write to, is one sshd refuses to read.
	homeMode     fs.FileMode = 0o700
	sshDirMode   fs.FileMode = 0o700
	keysFileMode fs.FileMode = 0o600
	// bashShell is what the recovery user is given if it cannot log in
	// interactively. A recovery account with no shell is not a recovery
	// account.
	bashShell = "/bin/bash"
	// A broken NSS backend or account database must not hold the entire switch
	// before it reaches the reboot ladder.
	commandTimeout = 20 * time.Second
)

// Report is the outcome of one repair, in enough detail for the caller to say
// what happened without having to know how any of it was done.
type Report struct {
	// VolumePresent is whether the data volume holding the ordinary home
	// directories was mounted. It is the single most important line in the
	// log: volume absent and volume present have different remedies.
	VolumePresent bool
	// AccountsRepaired are the ordinary accounts left with the configured keys
	// in place.
	AccountsRepaired []string
	// AccountsSkipped are the ordinary accounts deliberately not touched,
	// either because the volume is absent or because the account does not
	// exist.
	AccountsSkipped []string
	// RecoveryUserCreated is true if the recovery user did not exist and was
	// created during this repair.
	RecoveryUserCreated bool
	// Unlocked are the accounts that were found locked and successfully
	// unlocked.
	Unlocked []string
	// Warnings is everything that went wrong or was deliberately not done. A
	// warning never stops the rest of the repair: one failure must not abort
	// the remainder, because the point of repair is to have as much working as
	// possible when the machine is about to reboot.
	Warnings []string
}

// Perform repairs the machine according to cfg and reports what it did.
func Perform(ctx context.Context, cfg config.Config, log *slog.Logger) (Report, error) {
	return newRepairer(cfg, systemActor(ctx, log)).perform()
}

// actor is the seam through which repair touches the outside world: the shell
// out to useradd/usermod/chown/visudo, and the mountinfo lookup. Tests
// substitute their own implementations, so nothing in this package's test
// suite needs a real system account or a real mount.
type actor struct {
	log          *slog.Logger
	run          func(name string, args ...string) (string, error)
	isMountpoint func(path string) (bool, error)
	// sudoersPath is a field rather than a constant so that tests can point
	// it at a temporary directory. Only systemActor sets it.
	sudoersPath string
}

func systemActor(ctx context.Context, log *slog.Logger) *actor {
	return &actor{
		log:          log,
		run:          func(name string, args ...string) (string, error) { return runCommand(ctx, name, args...) },
		isMountpoint: mountinfo.IsMountpoint,
		sudoersPath:  defaultSudoersPath,
	}
}

// runCommand runs an external command and returns its combined output.
//
// name and args are passed to exec.Command as an argv slice. No shell is ever
// involved and no configuration value is ever interpolated into a command
// line, so a hostile or merely careless key, username or path cannot become
// anything but an argument.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	// name and args go to exec.Command as an argv slice. No shell is ever
	// involved and no configuration value is ever interpolated into a command
	// line, so a careless key, username or path cannot become anything but an
	// argument.
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type repairer struct {
	cfg config.Config
	a   *actor
	rep Report
}

func newRepairer(cfg config.Config, a *actor) *repairer {
	return &repairer{cfg: cfg, a: a}
}

func (r *repairer) perform() (Report, error) {
	// A configuration that would misbehave at runtime is a programming-level
	// problem rather than something to repair around, so it is the one thing
	// that is allowed to stop everything.
	if err := r.cfg.Validate(); err != nil {
		return r.rep, err
	}
	r.repairRecoveryUser()
	r.repairSudoers()
	r.repairAccounts()
	return r.rep, nil
}

// repairRecoveryUser makes sure there is an account on the root disk that a
// human can log in through. It is the escape hatch, so it is repaired first
// and unconditionally.
func (r *repairer) repairRecoveryUser() {
	user := r.cfg.Repair.RecoveryUser
	home := r.cfg.Repair.RecoveryHome

	entry, found, warns := r.a.lookup(user)
	r.rep.Warnings = append(r.rep.Warnings, warns...)

	if !found {
		// The home directory is created first, and owned by the user once the
		// user exists. useradd runs with -M so that it does not create a home
		// of its own choosing, and with -d so that the recorded home is the one
		// on the root disk: this is the account that has to work when the
		// volume does not.
		if err := r.a.mkdir(home, homeMode); err != nil {
			r.warn("could not create the recovery home %s: %v", home, err)
		}
		if out, err := r.a.run("useradd",
			"-M",       // do not create a home: the one that matters is the root disk
			"-d", home, // ...and this is where it is
			"-s", bashShell,
			user,
		); err != nil {
			r.warn("could not create the recovery user %s: %v: %s", user, err, oneline(out))
			return
		}
		r.rep.RecoveryUserCreated = true
		r.a.log.Info("recovery user created", "user", user, "home", home, "shell", bashShell)
		var again []string
		entry, found, again = r.a.lookup(user)
		r.rep.Warnings = append(r.rep.Warnings, again...)
		if !found {
			r.warn("the recovery user %s was created but cannot be read back; not repairing its keys", user)
			return
		}
		// The home was made by root a moment ago, so it is root's. Hand it to
		// the account that is meant to be able to write into it.
		if err := r.a.chown(home, entry); err != nil {
			r.warn("could not give the recovery home %s to %s: %v", home, user, err)
		}
	} else if entry.Home != home {
		// An existing account pointing at the wrong home is the exact failure
		// this switch exists to fix.
		if out, err := r.a.run("usermod", "-d", home, user); err != nil {
			r.warn("the recovery user %s has home %s and usermod could not move it to %s: %v: %s",
				user, entry.Home, home, err, oneline(out))
			// Continuing would write the recovery keys into the old home,
			// potentially on the missing data volume.
			return
		} else {
			r.a.log.Info("recovery user home corrected", "user", user, "was", entry.Home, "now", home)
			entry.Home = home
			// usermod -d does not change ownership, and the home it now points
			// at may be root's.
			if err := r.a.chown(home, entry); err != nil {
				r.warn("could not give the recovery home %s to %s: %v", home, user, err)
			}
		}
	}

	if entry.isNologin() {
		// A recovery account that cannot be logged into interactively is not
		// a recovery account.
		if out, err := r.a.run("usermod", "-s", bashShell, user); err != nil {
			r.warn("the recovery user %s has shell %s and usermod could not set bash: %v: %s",
				user, entry.Shell, err, oneline(out))
		} else {
			r.a.log.Info("recovery user shell corrected", "user", user, "was", entry.Shell, "now", bashShell)
		}
	}

	if r.unlock(entry) {
		r.rep.Unlocked = append(r.rep.Unlocked, user)
	}

	// The recovery user's keys are the whole story: the file is replaced, so
	// that a key rotated out of the configuration stops working. There is
	// nobody else whose keys these could be.
	if err := r.a.ensureSSHDir(entry.Home, entry); err != nil {
		r.warn("could not prepare %s for the recovery user %s: %v", sshPath(entry.Home), user, err)
		return
	}
	keysPath := keysPathFor(entry.Home)
	if err := writeFileAtomic(keysPath, renderKeys(r.cfg.Repair.AuthorizedKeys), keysFileMode); err != nil {
		r.warn("could not write the recovery user's keys to %s: %v", keysPath, err)
		return
	}
	if err := r.a.chown(keysPath, entry); err != nil {
		r.warn("could not give %s to the recovery user %s: %v", keysPath, user, err)
	}
	r.a.log.Info("recovery user keys written", "user", user, "keys", len(r.cfg.Repair.AuthorizedKeys), "path", keysPath)
}

// repairSudoers grants the recovery user passwordless sudo, if asked to.
func (r *repairer) repairSudoers() {
	if !r.cfg.Repair.RecoveryNopasswdSudo {
		r.a.log.Info("no passwordless sudo grant requested for the recovery user", "user", r.cfg.Repair.RecoveryUser)
		return
	}
	user := r.cfg.Repair.RecoveryUser
	path := r.a.sudoersPath
	// One line. A recovery account that cannot escalate is an inconvenience;
	// a sudoers file that does not parse is worse than that, because sudo
	// refuses to run at all when any included file is broken.
	line := fmt.Sprintf("%s ALL=(ALL:ALL) NOPASSWD: ALL\n", user)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".watchgoose-sudoers-*")
	if err != nil {
		r.warn("could not create a temporary sudoers file beside %s: %v", path, err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(line); err != nil {
		tmp.Close()
		r.warn("could not write temporary sudoers file for %s: %v", path, err)
		return
	}
	if err := tmp.Chmod(sudoersMode); err != nil {
		tmp.Close()
		r.warn("could not set mode on temporary sudoers file for %s: %v", path, err)
		return
	}
	if err := tmp.Close(); err != nil {
		r.warn("could not close temporary sudoers file for %s: %v", path, err)
		return
	}
	if out, err := r.a.run("visudo", "-c", "-f", tmp.Name()); err != nil {
		r.warn("sudoers grant for %s did not validate; existing grant left in place: %v: %s",
			path, err, oneline(out))
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		r.warn("could not install validated sudoers grant at %s: %v", path, err)
		return
	}
	r.a.log.Info("passwordless sudo granted to the recovery user", "user", user, "path", path)
}

// repairAccounts unlocks the ordinary accounts and puts the configured keys
// in place, but only when the volume that holds their home directories is
// mounted.
func (r *repairer) repairAccounts() {
	accounts := r.cfg.Repair.Accounts
	if len(accounts) == 0 {
		r.rep.VolumePresent = true
		return
	}

	mountpoint := r.cfg.Volume.Mountpoint
	if mountpoint == "" {
		// No volume configured: this machine has only the one disk.
		r.rep.VolumePresent = true
	} else {
		present, err := r.a.isMountpoint(mountpoint)
		switch {
		case err != nil:
			// Treat an unreadable mountinfo as an absent volume. Writing to the
			// mountpoint would land on the root disk and be shadowed, which is
			// the outcome ADR 0004 exists to avoid.
			r.rep.VolumePresent = false
			r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, accounts...)
			r.warn("could not tell whether the volume %s is mounted, so the ordinary accounts are skipped: %v",
				mountpoint, err)
			return
		case !present:
			r.rep.VolumePresent = false
			r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, accounts...)
			r.warn("volume %s is not mounted: skipping the ordinary accounts. A write to the mountpoint now "+
				"would land on the root disk and be shadowed the moment the volume mounts on a later boot, so "+
				"the reboot is the remedy here, not a key rewrite", mountpoint)
			return
		default:
			r.rep.VolumePresent = true
		}
	}
	r.a.log.Info("volume is mounted; repairing the ordinary accounts", "volume", mountpoint, "accounts", len(accounts))

	for _, account := range accounts {
		r.repairAccount(account)
	}
}

func (r *repairer) repairAccount(account string) {
	entry, found, warns := r.a.lookup(account)
	r.rep.Warnings = append(r.rep.Warnings, warns...)
	if !found {
		// There is nothing to repair and nothing to create: making an ordinary
		// account appear would be inventing a user, not repairing one.
		r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, account)
		r.warn("ordinary account %s does not exist; nothing to repair", account)
		return
	}

	if r.unlock(entry) {
		r.rep.Unlocked = append(r.rep.Unlocked, account)
	}

	if err := r.a.ensureSSHDir(entry.Home, entry); err != nil {
		r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, account)
		r.warn("could not prepare %s for %s: %v", sshPath(entry.Home), account, err)
		return
	}

	path := keysPathFor(entry.Home)
	existing, err := readKeys(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Unreadable is not the same as absent, and overwriting a file whose
		// contents cannot be read would delete other people's keys.
		r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, account)
		r.warn("could not read %s for %s, so it has been left alone: %v", path, account, err)
		return
	}

	// Additive, always. An ordinary account's authorized_keys may hold keys
	// that belong to other people, and removing or reordering them would lock
	// those people out of this machine.
	merged := MergeKeys(existing, r.cfg.Repair.AuthorizedKeys)
	if strings.Join(merged, "\n") == strings.Join(existing, "\n") {
		r.a.log.Info("account keys already in place", "account", account, "keys", len(merged), "path", path)
	} else {
		if err := writeFileAtomic(path, renderKeys(merged), keysFileMode); err != nil {
			r.rep.AccountsSkipped = append(r.rep.AccountsSkipped, account)
			r.warn("could not write %s for %s: %v", path, account, err)
			return
		}
		if err := r.a.chown(path, entry); err != nil {
			r.warn("could not give %s to %s: %v", path, account, err)
		}
		r.a.log.Info("account keys written", "account", account, "keys", len(merged), "added", len(merged)-countExisting(existing), "path", path)
	}
	r.rep.AccountsRepaired = append(r.rep.AccountsRepaired, account)
}

// unlock runs usermod -U for an account that is found locked, and reports
// whether it did anything.
func (r *repairer) unlock(entry passwdEntry) bool {
	locked, known := r.a.isLocked(entry.Name)
	if known && !locked {
		// Not locked. Running usermod -U anyway would be noise in the audit
		// trail, and would claim an unlock that did not happen.
		return false
	}
	out, err := r.a.run("usermod", "-U", entry.Name)
	if err != nil {
		r.warn("could not unlock %s: %v: %s", entry.Name, err, oneline(out))
		return false
	}
	if !known {
		// The lock state could not be read, so this is an unlock attempt that
		// is not known to have been one. Say so rather than overstate it.
		r.a.log.Info("unlock attempted; the lock state could not be read first", "account", entry.Name, "path", entry.Home)
		return false
	}
	r.a.log.Info("account unlocked", "account", entry.Name, "path", entry.Home)
	return true
}

// warn records a warning. Warnings are collected, not fatal: the rest of the
// repair continues regardless.
func (r *repairer) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.rep.Warnings = append(r.rep.Warnings, msg)
	r.a.log.Warn("repair: " + msg)
}

// oneline squeezes command output onto one line, so a warning is greppable.
func oneline(out string) string {
	out = strings.TrimSpace(out)
	out = strings.ReplaceAll(out, "\n", "; ")
	if len(out) > 200 {
		out = out[:200] + "..."
	}
	if out == "" {
		out = "(no output)"
	}
	return out
}

func sshPath(home string) string { return filepath.Join(home, ".ssh") }

func keysPathFor(home string) string { return filepath.Join(sshPath(home), "authorized_keys") }

// countExisting is how many of the merged keys came from the file already, for
// the log line that says what repair added.
func countExisting(existing []string) int {
	if len(existing) == 0 {
		return 0
	}
	n := 0
	for _, k := range existing {
		if strings.TrimSpace(k) != "" {
			n++
		}
	}
	return n
}
