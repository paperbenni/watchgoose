package repair

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"watchgoose/internal/config"
)

// fakeUser is one account in the fake passwd/shadow database. No test in this
// package touches a real account: everything goes through fakeSystem.run.
type fakeUser struct {
	name  string
	uid   int
	gid   int
	home  string
	shell string
	// hash is the raw shadow password field. The tests below set it to each
	// shape that means something different, so that isLocked's reading of
	// shadow is exercised rather than assumed.
	hash string
}

func (u *fakeUser) passwdLine() string {
	return fmt.Sprintf("%s:x:%d:%d::%s:%s", u.name, u.uid, u.gid, u.home, u.shell)
}

func (u *fakeUser) shadowLine() string {
	return fmt.Sprintf("%s:%s:19700:0:99999:7:::", u.name, u.hash)
}

// The shadow password fields that appear in practice, and what each one means.
//
//	bareHash   a real password hash: the account can log in with a password.
//	bareBang   "!" — no password was ever set. Correct for a key-only account,
//	           and NOT a lock: usermod -U refuses to act on it, because doing so
//	           would leave an empty, passwordless field.
//	lockedHash a real hash behind a "!": a password exists and is deliberately
//	           disabled. This is the one case where unlocking is the right move.
//	star       "*": no password will ever match. Not a lock either.
//	empty      no password field at all.
const (
	bareHash   = "$6$abcdefghijklmnopqrst$0123456789abcdefghijklmnopqrstuvwxyz0123456789ab"
	bareBang   = "!"
	lockedHash = "!" + bareHash
	star       = "*"
	empty      = ""
)

// fakeSystem answers the commands repair runs, and remembers what it was
// asked. It is deliberately literal: a command repair does not expect is a
// test failure rather than a silent no-op.
type fakeSystem struct {
	users map[string]*fakeUser
	// calls records every argv, in order, as "name arg arg".
	calls []string
	// argv records the same calls as the argument slices they were built from,
	// so that a test can assert that a value arrived as one element.
	argv [][]string
	// fail lets a test make one command fail.
	fail map[string]error
	// volumeMounted is the answer to the mountinfo question.
	volumeMounted bool
	// mountErr, when set, is returned instead of volumeMounted.
	mountErr error
	// visudoOutput is what visudo -c says.
	visudoErr error
	// sudoersDir is where the sudo grant is aimed.
	sudoersDir string
	// nextUID hands out ids for accounts that get created.
	nextUID int
}

func newFakeSystem(t *testing.T) *fakeSystem {
	t.Helper()
	return &fakeSystem{
		users:   map[string]*fakeUser{},
		fail:    map[string]error{},
		nextUID: 2000,
		// The volume is mounted unless a test says otherwise; the absent case
		// is the one that needs stating out loud.
		volumeMounted: true,
	}
}

// addUser adds an account whose shadow field is bareBang, the shape useradd
// writes for an account created without a password. That is the state every
// account here actually has, and the state that must NOT be reported as locked.
func (f *fakeSystem) addUser(name, home, shell string, locked bool) *fakeUser {
	hash := bareBang
	if locked {
		hash = lockedHash
	}
	u := &fakeUser{name: name, uid: f.nextUID, gid: f.nextUID, home: home, shell: shell, hash: hash}
	f.nextUID++
	f.users[name] = u
	return u
}

func (f *fakeSystem) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	f.argv = append(f.argv, append([]string{name}, args...))
	// A failing command is named by its whole command line, e.g.
	// "usermod -U benjamin" or "chown 2001:2001".
	if err, ok := f.fail[strings.Join(append([]string{name}, args...), " ")]; ok {
		if name == "getent" {
			return "", err
		}
		return "simulated failure: " + err.Error(), err
	}

	switch name {
	case "getent":
		if len(args) != 2 {
			return "", fmt.Errorf("getent wants a database and a key")
		}
		u, ok := f.users[args[1]]
		if !ok {
			// getent exits non-zero and prints nothing for an unknown user.
			return "", errAccountMissing
		}
		switch args[0] {
		case "passwd":
			return u.passwdLine(), nil
		case "shadow":
			return u.shadowLine(), nil
		}
		return "", fmt.Errorf("unknown database %q", args[0])

	case "useradd":
		// The real useradd was told -M, so it creates no home: the home it
		// records is the one repair made. The fake only records it.
		home, shell, name := "", "/bin/sh", ""
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "-d":
				home = args[i+1]
			case "-s":
				shell = args[i+1]
			default:
				if !strings.HasPrefix(args[i], "-") {
					name = args[i]
				}
			}
		}
		f.users[name] = &fakeUser{name: name, uid: f.nextUID, gid: f.nextUID, home: home, shell: shell}
		f.nextUID++
		return "", nil

	case "usermod":
		u, ok := f.users[args[len(args)-1]]
		if !ok {
			return "", fmt.Errorf("%s: no such user", args[len(args)-1])
		}
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "-d":
				u.home = args[i+1]
			case "-s":
				u.shell = args[i+1]
			case "-U":
				// usermod -U strips one leading "!", which is why it only
				// makes sense on a field with a hash behind it.
				u.hash = strings.TrimPrefix(u.hash, "!")
			}
		}
		return "", nil

	case "visudo":
		if f.visudoErr != nil {
			return "simulated failure: " + f.visudoErr.Error(), f.visudoErr
		}
		return "", nil
	case "chown":
		return "", nil
	}
	return "", fmt.Errorf("fakeSystem does not implement %q", name)
}

func (f *fakeSystem) ran(command string) bool {
	for _, c := range f.calls {
		if c == command || strings.HasPrefix(c, command+" ") {
			return true
		}
	}
	return false
}

func (f *fakeSystem) callsTo(command string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, command+" ") || c == command {
			out = append(out, c)
		}
	}
	return out
}

// actor wires the fake system into repair, with the sudo grant aimed at a
// temporary directory so that no test can write to /etc.
func (f *fakeSystem) actor(t *testing.T) *actor {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(f.sudoersDir, "watchgoose-recovery")
	return &actor{
		log:          log,
		run:          f.run,
		isMountpoint: func(string) (bool, error) { return f.volumeMounted, f.mountErr },
		sudoersPath:  path,
	}
}

// testConfig is a configuration whose every path is inside temporary
// directories, so that no test can touch a real account or a real /home.
func testConfig(t *testing.T, f *fakeSystem) config.Config {
	t.Helper()
	volume := t.TempDir()
	f.sudoersDir = t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.StateFile = filepath.Join(t.TempDir(), "last-reassurance")
	cfg.Reassurance.Deadline = 20 * time.Minute
	cfg.Guard.MinUptime = 30 * time.Minute
	cfg.Volume.Mountpoint = volume
	cfg.Repair.Settle = time.Minute
	cfg.Repair.Accounts = nil
	cfg.Repair.AuthorizedKeys = []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIwatchgoose watchgoose@example",
	}
	cfg.Repair.RecoveryUser = "recovery"
	cfg.Repair.RecoveryHome = filepath.Join(t.TempDir(), "recovery")
	cfg.Reboot.GracefulTimeout = 5 * time.Minute
	cfg.Log.File = filepath.Join(t.TempDir(), "watchgoose.log")
	return cfg
}

func perform(t *testing.T, cfg config.Config, f *fakeSystem) Report {
	t.Helper()
	rep, err := newRepairer(cfg, f.actor(t)).perform()
	if err != nil {
		t.Fatalf("perform: %v", err)
	}
	return rep
}

func TestFailedRecoveryLookupDoesNotInventAUser(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	f.fail["getent passwd recovery"] = fmt.Errorf("NSS unavailable")
	rep := perform(t, cfg, f)
	if f.ran("useradd") {
		t.Errorf("useradd ran after an inconclusive lookup: %v", f.calls)
	}
	if !hasWarningAbout(rep.Warnings, "could not determine whether account recovery exists") {
		t.Errorf("NSS failure was not reported: %v", rep.Warnings)
	}
}

func TestCreatesTheRecoveryUserOnTheRootDisk(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	home := cfg.Repair.RecoveryHome

	rep := perform(t, cfg, f)

	if !rep.RecoveryUserCreated {
		t.Error("RecoveryUserCreated = false, want true")
	}
	u, ok := f.users["recovery"]
	if !ok {
		t.Fatal("the recovery user was not created")
	}
	if u.home != home {
		t.Errorf("the recovery user's home is %q, want %q", u.home, home)
	}
	// -M is what stops useradd creating a home of its own, on the volume, in
	// the wrong place. This is the whole reason the flag is there.
	add := f.callsTo("useradd")
	if len(add) != 1 {
		t.Fatalf("useradd ran %d times, want once: %v", len(add), add)
	}
	for _, want := range []string{"useradd -M", "-d " + home, "-s /bin/bash", "recovery"} {
		if !strings.Contains(add[0], want) {
			t.Errorf("useradd was run as %q, want it to contain %q", add[0], want)
		}
	}
	// The home exists, is private, and belongs to the user.
	info, err := os.Stat(home)
	if err != nil {
		t.Fatalf("the recovery home was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the recovery home is %#o, want 0700", perm)
	}
	if !f.ran("chown " + fmt.Sprintf("%d:%d", u.uid, u.gid) + " " + home) {
		t.Errorf("the recovery home was not given to the recovery user; calls: %v", f.calls)
	}

	// And the keys are exactly the configured ones, in the configured order.
	got, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("the recovery user's keys were not written: %v", err)
	}
	want := cfg.Repair.AuthorizedKeys[0] + "\n"
	if string(got) != want {
		t.Errorf("authorized_keys = %q, want %q", got, want)
	}
}

func TestRecoveryUsersKeysAreReplacedExactly(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	home := cfg.Repair.RecoveryHome
	keys := filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(keys), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A key that is no longer in the configuration must stop working: this
	// file exists for this repair and nobody else.
	stale := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIretired retired@example\n"
	if err := os.WriteFile(keys, []byte(stale+"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIother other@example\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f.addUser("recovery", home, "/bin/bash", false)

	perform(t, cfg, f)

	got, err := os.ReadFile(keys)
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	want := cfg.Repair.AuthorizedKeys[0] + "\n"
	if string(got) != want {
		t.Errorf("authorized_keys = %q, want exactly %q", got, want)
	}
}

func TestCorrectsARecoveryUserWithTheWrongHomeOrShellOrLock(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	// Exactly the state the simplevm deactivate_homes.sh bug leaves behind,
	// except pointed at the recovery user's home as well.
	f.addUser("recovery", "/home/recovery", "/usr/sbin/nologin", true)

	rep := perform(t, cfg, f)

	u := f.users["recovery"]
	if u.home != cfg.Repair.RecoveryHome {
		t.Errorf("the recovery user's home is %q, want %q", u.home, cfg.Repair.RecoveryHome)
	}
	if u.shell != "/bin/bash" {
		t.Errorf("the recovery user's shell is %q, want /bin/bash", u.shell)
	}
	if u.hash != bareHash {
		t.Errorf("the recovery user's shadow field is %q, want the hash to have been uncovered", u.hash)
	}
	if !contains(rep.Unlocked, "recovery") {
		t.Errorf("Unlocked = %v, want it to contain the recovery user", rep.Unlocked)
	}
	if rep.RecoveryUserCreated {
		t.Error("RecoveryUserCreated = true, but the user already existed")
	}
	// The keys follow the account to its corrected home.
	if _, err := os.Stat(filepath.Join(cfg.Repair.RecoveryHome, ".ssh", "authorized_keys")); err != nil {
		t.Errorf("the recovery user's keys are not in the corrected home: %v", err)
	}
}

func TestFailedRecoveryHomeCorrectionDoesNotWriteKeysToOldHome(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	oldHome := filepath.Join(t.TempDir(), "old-home")
	f.addUser("recovery", oldHome, "/bin/bash", false)
	f.fail["usermod -d "+cfg.Repair.RecoveryHome+" recovery"] = fmt.Errorf("account database unavailable")
	perform(t, cfg, f)
	if _, err := os.Stat(filepath.Join(oldHome, ".ssh", "authorized_keys")); !os.IsNotExist(err) {
		t.Fatalf("keys written to the old home despite failed correction: %v", err)
	}
}

func TestOrdinaryAccountKeysAreMergedAdditively(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin", "ubuntu"}

	benjamin := filepath.Join(cfg.Volume.Mountpoint, "benjamin")
	ubuntu := filepath.Join(cfg.Volume.Mountpoint, "ubuntu")
	// benjamin already has two keys that belong to other people, in an order
	// that is none of repair's business.
	existing := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIfriend friend@example\n" +
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQCcolleague colleague@laptop\n"
	seedKeys(t, filepath.Join(benjamin, ".ssh", "authorized_keys"), existing)
	f.addUser("benjamin", benjamin, "/bin/bash", true)
	f.addUser("ubuntu", ubuntu, "/bin/bash", false)

	rep := perform(t, cfg, f)

	if !rep.VolumePresent {
		t.Error("VolumePresent = false, but the volume is mounted")
	}
	if len(rep.AccountsRepaired) != 2 {
		t.Errorf("AccountsRepaired = %v, want both accounts", rep.AccountsRepaired)
	}
	if len(rep.AccountsSkipped) != 0 {
		t.Errorf("AccountsSkipped = %v, want none", rep.AccountsSkipped)
	}
	if !contains(rep.Unlocked, "benjamin") {
		t.Errorf("Unlocked = %v, want it to contain benjamin", rep.Unlocked)
	}

	got, err := os.ReadFile(filepath.Join(benjamin, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	// Every key the user had, in the order they had them, and then ours.
	want := existing + cfg.Repair.AuthorizedKeys[0] + "\n"
	if string(got) != want {
		t.Errorf("authorized_keys =\n%q\nwant\n%q", got, want)
	}

	info, err := os.Stat(filepath.Join(benjamin, ".ssh"))
	if err != nil {
		t.Fatalf("stat .ssh: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the .ssh directory is %#o, want 0700", perm)
	}
	info, err = os.Stat(filepath.Join(benjamin, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("authorized_keys is %#o, want 0600", perm)
	}
	if u := f.users["benjamin"]; !f.ran(fmt.Sprintf("chown %d:%d %s", u.uid, u.gid, filepath.Join(benjamin, ".ssh", "authorized_keys"))) {
		t.Errorf("the keys were not given to benjamin; calls: %v", f.calls)
	}
}

func TestRepairIsAdditiveWhenTheKeyIsAlreadyPresent(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin"}
	home := filepath.Join(cfg.Volume.Mountpoint, "benjamin")
	keys := filepath.Join(home, ".ssh", "authorized_keys")
	seedKeys(t, keys, cfg.Repair.AuthorizedKeys[0]+"\n")
	f.addUser("benjamin", home, "/bin/bash", false)

	rep := perform(t, cfg, f)

	got, err := os.ReadFile(keys)
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	if string(got) != cfg.Repair.AuthorizedKeys[0]+"\n" {
		t.Errorf("authorized_keys = %q, want it unchanged", got)
	}
	if !contains(rep.AccountsRepaired, "benjamin") {
		t.Errorf("AccountsRepaired = %v, want benjamin: nothing to do is still repaired", rep.AccountsRepaired)
	}
}

// A key-only account's shadow field is a bare "!", which is what useradd writes
// and what every account on the target machine has. shadow's usermod refuses to
// unlock it, so calling that "locked" would make the normal case report itself
// as a fault on every repair.
func TestBareBangIsNotTreatedAsLocked(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hash       string
		wantUnlock bool
	}{
		{"bare bang, the normal key-only state", bareBang, false},
		{"real hash, not locked", bareHash, false},
		{"real hash behind a lock, genuinely locked", lockedHash, true},
		{"star, no password will ever match", star, false},
		{"empty field, passwordless", empty, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSystem(t)
			cfg := testConfig(t, f)
			cfg.Repair.Accounts = []string{"benjamin"}
			u := f.addUser("benjamin", filepath.Join(t.TempDir(), "benjamin"), "/bin/bash", false)
			u.hash = tc.hash

			rep := perform(t, cfg, f)

			if got := contains(rep.Unlocked, "benjamin"); got != tc.wantUnlock {
				t.Errorf("shadow field %q: Unlocked contains benjamin = %v, want %v", tc.hash, got, tc.wantUnlock)
			}
			if attempted := calledWith(f, "usermod", "-U", "benjamin"); attempted != tc.wantUnlock {
				t.Errorf("shadow field %q: usermod -U attempted = %v, want %v", tc.hash, attempted, tc.wantUnlock)
			}
			// The account must be repaired either way. Being locked is not a
			// reason to skip it.
			if !contains(rep.AccountsRepaired, "benjamin") {
				t.Errorf("shadow field %q: benjamin was not repaired", tc.hash)
			}
		})
	}
}

// The accounts on the real machine are all key-only, so a repair of them must
// produce no warnings at all. Warnings that fire in the normal case are warnings
// nobody reads.
func TestRepairOfKeyOnlyAccountsProducesNoWarnings(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin", "ubuntu"}
	f.addUser("benjamin", filepath.Join(cfg.Volume.Mountpoint, "benjamin"), "/bin/bash", false)
	f.addUser("ubuntu", filepath.Join(cfg.Volume.Mountpoint, "ubuntu"), "/bin/bash", false)

	rep := perform(t, cfg, f)

	if len(rep.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none: a key-only account is not a fault", rep.Warnings)
	}
	if len(rep.Unlocked) != 0 {
		t.Errorf("Unlocked = %v, want none: nothing was locked", rep.Unlocked)
	}
	if !contains(rep.AccountsRepaired, "benjamin") || !contains(rep.AccountsRepaired, "ubuntu") {
		t.Errorf("AccountsRepaired = %v, want both accounts", rep.AccountsRepaired)
	}
}

func TestReportsAbsentVolumeEvenWithNoOrdinaryAccounts(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	f.volumeMounted = false
	rep := perform(t, cfg, f)
	if rep.VolumePresent {
		t.Fatal("absent volume was reported as present because no accounts were configured")
	}
}

func TestSkipsOrdinaryAccountsWhenTheVolumeIsAbsent(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin", "ubuntu"}
	f.volumeMounted = false

	// Their home directories exist on the root disk, which is exactly why
	// writing to them would be a lie.
	benjamin := filepath.Join(cfg.Volume.Mountpoint, "benjamin")
	if err := os.MkdirAll(benjamin, 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f.addUser("benjamin", benjamin, "/bin/bash", true)
	f.addUser("ubuntu", filepath.Join(cfg.Volume.Mountpoint, "ubuntu"), "/bin/bash", true)

	rep := perform(t, cfg, f)

	if rep.VolumePresent {
		t.Error("VolumePresent = true, but the volume is not mounted")
	}
	if len(rep.AccountsSkipped) != 2 {
		t.Errorf("AccountsSkipped = %v, want both accounts", rep.AccountsSkipped)
	}
	if len(rep.AccountsRepaired) != 0 {
		t.Errorf("AccountsRepaired = %v, want none", rep.AccountsRepaired)
	}
	if len(rep.Unlocked) != 0 {
		t.Errorf("Unlocked = %v, want none: the accounts live on a volume that is not there", rep.Unlocked)
	}
	// Nothing at all was written under the mountpoint.
	entries, err := os.ReadDir(cfg.Volume.Mountpoint)
	if err != nil {
		t.Fatalf("read the mountpoint: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "benjamin" {
		t.Errorf("the mountpoint holds %v, want only the empty benjamin directory that was seeded", entries)
	}
	if _, err := os.Stat(filepath.Join(benjamin, ".ssh")); !os.IsNotExist(err) {
		t.Errorf("a .ssh directory was created on the mountpoint: %v", err)
	}
	// The reason is recorded, because the two cases have different remedies.
	if !hasWarningAbout(rep.Warnings, cfg.Volume.Mountpoint) {
		t.Errorf("Warnings = %v, want one that names the absent volume", rep.Warnings)
	}
	// The recovery user is on the root disk and is repaired regardless.
	if !rep.RecoveryUserCreated {
		t.Error("the recovery user was not created; it does not depend on the volume")
	}
	if f.ran("usermod -U benjamin") {
		t.Error("usermod was run against an account on an absent volume")
	}
}

func TestAnUnreadableMountTableMeansTheVolumeIsAssumedAbsent(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin"}
	f.mountErr = fmt.Errorf("open /proc/self/mountinfo: permission denied")
	f.addUser("benjamin", filepath.Join(cfg.Volume.Mountpoint, "benjamin"), "/bin/bash", false)

	rep := perform(t, cfg, f)

	if rep.VolumePresent {
		t.Error("VolumePresent = true, but the volume could not be checked")
	}
	if !contains(rep.AccountsSkipped, "benjamin") {
		t.Errorf("AccountsSkipped = %v, want benjamin", rep.AccountsSkipped)
	}
	if len(rep.Warnings) == 0 {
		t.Error("Warnings is empty; an unknown mount state must be said out loud")
	}
}

func TestAnAccountThatDoesNotExistIsSkippedRatherThanInvented(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"nobody-at-all"}

	rep := perform(t, cfg, f)

	if !contains(rep.AccountsSkipped, "nobody-at-all") {
		t.Errorf("AccountsSkipped = %v, want the account that does not exist", rep.AccountsSkipped)
	}
	if contains(rep.AccountsRepaired, "nobody-at-all") {
		t.Error("an account that does not exist was reported as repaired")
	}
	// useradd runs exactly once in this whole repair, and that is for the
	// recovery user. Creating an ordinary account is provisioning, not repair.
	add := f.callsTo("useradd")
	if len(add) != 1 {
		t.Fatalf("useradd ran %d times, want once: %q", len(add), add)
	}
	if strings.Contains(add[0], "nobody-at-all") {
		t.Errorf("repair created the ordinary account: %q", add[0])
	}
	if len(rep.Warnings) == 0 {
		t.Error("Warnings is empty; skipping an account must be said out loud")
	}
}

func TestOneFailureDoesNotStopTheRest(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.Accounts = []string{"benjamin", "ubuntu"}
	benjamin := filepath.Join(cfg.Volume.Mountpoint, "benjamin")
	ubuntu := filepath.Join(cfg.Volume.Mountpoint, "ubuntu")
	b := f.addUser("benjamin", benjamin, "/bin/bash", true)
	f.addUser("ubuntu", ubuntu, "/bin/bash", true)
	// benjamin cannot be unlocked, and giving it its keys back fails too.
	// Neither may stop ubuntu from being repaired, or the recovery user from
	// being created.
	f.fail["usermod -U benjamin"] = fmt.Errorf("cannot unlock benjamin")
	f.fail[fmt.Sprintf("chown %d:%d", b.uid, b.gid)+" "+filepath.Join(benjamin, ".ssh", "authorized_keys")] =
		fmt.Errorf("no such file or directory")

	rep := perform(t, cfg, f)

	if !contains(rep.Unlocked, "ubuntu") || contains(rep.Unlocked, "benjamin") {
		t.Errorf("Unlocked = %v, want ubuntu and not benjamin", rep.Unlocked)
	}
	if !contains(rep.AccountsRepaired, "ubuntu") {
		t.Errorf("AccountsRepaired = %v, want ubuntu to have been repaired anyway", rep.AccountsRepaired)
	}
	if _, err := os.Stat(filepath.Join(benjamin, ".ssh", "authorized_keys")); err != nil {
		t.Errorf("benjamin's keys were not written even though the chown failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ubuntu, ".ssh", "authorized_keys")); err != nil {
		t.Errorf("ubuntu's keys were not written after a neighbouring failure: %v", err)
	}
	if !rep.RecoveryUserCreated {
		t.Error("the recovery user was not created despite the ordinary-account failures")
	}
	if len(rep.Warnings) < 2 {
		t.Errorf("Warnings = %v, want one for each failure", rep.Warnings)
	}
}

func TestAValidSudoersGrantIsLeftInPlace(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.RecoveryNopasswdSudo = true

	perform(t, cfg, f)

	path := filepath.Join(f.sudoersDir, "watchgoose-recovery")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no sudoers file: %v", err)
	}
	if want := "recovery ALL=(ALL:ALL) NOPASSWD: ALL\n"; string(got) != want {
		t.Errorf("the sudoers file is %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o440 {
		t.Errorf("the sudoers file is %#o, want 0440", perm)
	}
	if len(f.callsTo("visudo")) == 0 {
		t.Errorf("the sudoers file was not validated; calls: %v", f.calls)
	}
}

func TestAnInvalidSudoersFileIsRemovedRatherThanLeftBroken(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.RecoveryNopasswdSudo = true
	f.visudoErr = fmt.Errorf("syntax error, unexpected end of file")

	rep := perform(t, cfg, f)

	path := filepath.Join(f.sudoersDir, "watchgoose-recovery")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a sudoers file that does not parse was left in place: %v", err)
	}
	if !hasWarningAbout(rep.Warnings, path) {
		t.Errorf("Warnings = %v, want one that names the broken sudoers file", rep.Warnings)
	}
}

func TestInvalidSudoersUpdatePreservesExistingGrant(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.RecoveryNopasswdSudo = true
	path := filepath.Join(f.sudoersDir, "watchgoose-recovery")
	previous := []byte("recovery ALL=(ALL:ALL) NOPASSWD: ALL\n")
	if err := os.WriteFile(path, previous, 0o440); err != nil {
		t.Fatal(err)
	}
	f.visudoErr = fmt.Errorf("visudo unavailable")
	perform(t, cfg, f)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(previous) {
		t.Fatalf("existing sudo grant was lost: %q, %v", got, err)
	}
}

func TestNoSudoersFileWhenTheGrantIsNotAskedFor(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.RecoveryNopasswdSudo = false

	perform(t, cfg, f)

	if _, err := os.Stat(filepath.Join(f.sudoersDir, "watchgoose-recovery")); !os.IsNotExist(err) {
		t.Error("a sudo grant was written although the configuration did not ask for one")
	}
}

func TestConfigurationValuesAreNeverShellInterpolated(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	// A name full of shell metacharacters. If any of this were ever built
	// into a command string it would be a command injection; as an argv
	// element it is just an account name.
	nasty := `evil"; touch /tmp/pwned; $(id) ` + "`id`" + ` &`
	cfg.Repair.Accounts = []string{nasty}
	cfg.Volume.Mountpoint = ""
	f.addUser(nasty, filepath.Join(t.TempDir(), "evil"), "/bin/bash", true)
	cfg.Repair.RecoveryUser = "recovery"

	perform(t, cfg, f)

	// The whole name must arrive as one argument to one command. Anything else
	// means a command string was built somewhere along the way.
	asOneArgument := false
	for _, argv := range f.argv {
		for _, arg := range argv {
			if arg == nasty {
				asOneArgument = true
			}
		}
	}
	if !asOneArgument {
		t.Errorf("the account name never arrived as a single argument; calls: %q", f.calls)
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Fatal("a command string was built somewhere")
	}
}

func TestPerformRejectsAConfigurationItCannotActOn(t *testing.T) {
	f := newFakeSystem(t)
	cfg := testConfig(t, f)
	cfg.Repair.RecoveryHome = "" // as config.Validate requires

	if _, err := newRepairer(cfg, f.actor(t)).perform(); err == nil {
		t.Fatal("perform accepted a configuration that would misbehave at runtime")
	}
	if len(f.calls) != 0 {
		t.Errorf("perform ran %v despite an unusable configuration", f.calls)
	}
}

func TestRunCommandStopsWhenRepairIsCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runCommand(ctx, "sleep", "5")
	if err == nil {
		t.Fatal("long-running repair command ignored cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("repair command kept the switch blocked after cancellation")
	}
}

func seedKeys(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// calledWith reports whether the fake was asked to run exactly this argv. It
// compares the recorded argv slices rather than the joined strings, so an
// argument containing a space cannot masquerade as a different command.
func calledWith(f *fakeSystem, name string, args ...string) bool {
	want := append([]string{name}, args...)
	for _, got := range f.argv {
		if len(got) != len(want) {
			continue
		}
		same := true
		for i := range got {
			if got[i] != want[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

func hasWarningAbout(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}
