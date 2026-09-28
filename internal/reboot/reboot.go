// Package reboot is the escalation ladder: the rungs, in order, that a machine
// reaches for when it has stopped hearing from the outside world.
//
// The ladder has exactly two rungs. The first is a graceful reboot through
// systemd. The second, reached only if the first has not taken the machine
// down within the configured graceful timeout, is a direct kernel reboot via
// sysrq-b.
//
// The forceful rung is deliberately not configurable, and this package
// contains no code path that can power the machine off. See
// docs/adr/0002-escalate-to-sysrq-b.md, which is binding on this design.
package reboot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// sysrqTriggerPath is the write-only door that performs a sysrq function.
	sysrqTriggerPath = "/proc/sysrq-trigger"

	// The child is this same binary re-executed with private flags. Naming
	// each one makes the child legible in ps, which matters for a process a
	// human may need to find and kill. The names carry no leading dash, which
	// the flag package refuses; the parent adds one when it builds argv.
	EscalateFlag        = "escalate-child"
	EscalateGracefulArg = "escalate-child-graceful"
	EscalateLogArg      = "escalate-child-log"

	// childHardBound caps the escalation child's whole life. The child exists
	// to survive a reboot, so without a bound it would be a permanent orphan
	// on a machine whose graceful rung did not take it down.
	childHardBound = 5 * time.Minute
	// spawnWait bounds how long GracefulReboot waits to see that systemctl was
	// actually started. It does not wait for the reboot to complete.
	spawnWait = 10 * time.Second
)

// EscalationSpec is everything the escalation child needs to know. It travels
// to the child as argv, never as a shell string.
type EscalationSpec struct {
	// GracefulTimeout is how long to let the graceful rung have before taking
	// the forceful one.
	GracefulTimeout time.Duration
	// LogFile is the audit trail the child writes its own few lines to.
	LogFile string
}

// hardBound is the latest instant the child may act, whatever else happens.
func (s EscalationSpec) hardBound() time.Time {
	return time.Now().Add(s.MaximumWait())
}

// MaximumWait bounds how long the parent should trust this child as a reboot
// backstop. If the machine is still running afterward, it must retry.
func (s EscalationSpec) MaximumWait() time.Duration {
	return s.GracefulTimeout + childHardBound
}

// TriggerForcefulReboot reboots the kernel by writing the sysrq reboot command.
//
// The byte written is a hardcoded literal, and this is the only function in
// the package that touches /proc/sysrq-trigger. There is no parameter, no
// variable and no reachable path in this package that can write `o`
// (poweroff), `i` (kill all processes), `c` (deliberate crash) or any other
// byte. This machine has no hypervisor control panel to bring it back, so a
// power-off would be the end of it; the switch is built so that its own code
// cannot reach one (docs/adr/0002-escalate-to-sysrq-b.md).
func TriggerForcefulReboot() error {
	if err := writeProcFile(sysrqTriggerPath, []byte{'b'}); err != nil {
		return fmt.Errorf("trigger forceful reboot: write sysrq 'b': %w", err)
	}
	return nil
}

// GracefulReboot starts the first rung: `systemctl reboot`.
//
// It deliberately does not wait for the reboot to happen. PID 1 may be exactly
// why the switch is firing, so blocking on systemctl is blocking on the thing
// that may be broken. This returns once the process has been started, or has
// finished failing; a nil return means "systemctl was started", not "the
// machine is going down". The escalation child covers the difference.
func GracefulReboot(ctx context.Context) error {
	// An argv slice, never a shell string.
	cmd := exec.CommandContext(ctx, "systemctl", "reboot")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start systemctl reboot: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("systemctl reboot: %w", err)
		}
		return nil
	case <-time.After(spawnWait):
		// Still running after spawnWait. That is the expected outcome on a
		// healthy machine mid-reboot, and the interesting one on a wedged
		// PID 1. Either way the child owns the next rung. The waiting
		// goroutine is abandoned deliberately: it costs one thread and the
		// machine is about to reboot, which this process is expected not to
		// survive.
		return nil
	}
}

// SpawnEscalationChild forks a child that outlives the impending systemd
// teardown, and returns its PID.
//
// The child sleeps for the graceful timeout and then takes the forceful rung.
// It has to survive two things that are about to happen: systemd stopping
// this process on the way down, and this process dying with it. Setsid puts
// the child in its own session, away from the parent's process group and
// controlling terminal. The service uses KillMode=process because a new
// session does not leave its systemd cgroup. Once started, the child ignores
// SIGTERM and SIGHUP.
//
// There is a window of a few microseconds between this fork and the child
// installing its own signal handlers in which a SIGTERM could reach it and
// take it out. Go offers no way to block signals across exec, and closing
// that window by hand would mean a shell, which is worse. The window is
// before the graceful rung has even been started, so losing it costs the
// forceful rung and nothing else: it cannot cause an unwanted reboot.
func SpawnEscalationChild(spec EscalationSpec, log *slog.Logger) (int, error) {
	if spec.GracefulTimeout <= 0 {
		return 0, fmt.Errorf("spawn escalation child: graceful timeout must be positive, got %s", spec.GracefulTimeout)
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("spawn escalation child: locate own binary: %w", err)
	}
	// Every value travels as its own argv element; nothing here is ever
	// handed to a shell.
	cmd := exec.Command(exe,
		"-"+EscalateFlag,
		"-"+EscalateGracefulArg, spec.GracefulTimeout.String(),
		"-"+EscalateLogArg, spec.LogFile,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Its own session, so that the child is not reachable through this
		// process's process group or controlling terminal and nothing here
		// sends a signal to it as a side effect of stopping this process.
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn escalation child: %w", err)
	}
	pid := cmd.Process.Pid
	// No Wait: the child's entire purpose is to still be running when this
	// process is gone. Releasing the handle keeps it from lingering as a
	// zombie in the unlikely event that this process outlives it.
	if err := cmd.Process.Release(); err != nil && log != nil {
		// The child already exists. A failed local handle release must not
		// cause the parent to believe it has no forceful rung.
		log.Warn("escalation child started but its parent handle could not be released", "child_pid", pid, "error", err)
	}
	if log != nil {
		log.Info("escalation child spawned: it takes the forceful rung if the graceful one does not take the machine down",
			"rung", forcefulRungName,
			"child_pid", pid,
			"graceful_timeout", spec.GracefulTimeout,
			"child_hard_bound", spec.GracefulTimeout+childHardBound)
	}
	return pid, nil
}

// RunEscalationChild is the body of the escalation child: sleep out the
// graceful rung and then take the forceful rung. It returns the process exit code.
func RunEscalationChild(spec EscalationSpec, log *slog.Logger) int {
	// First statement, before anything can block or fail: the child must be
	// deaf to the two signals systemd uses to stop this process.
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)

	if log == nil {
		log = slog.Default()
	}
	fields := []any{
		"rung", forcefulRungName,
		"child_pid", os.Getpid(),
		"graceful_timeout", spec.GracefulTimeout,
		"child_hard_bound", spec.GracefulTimeout + childHardBound,
	}
	// A copy, so that adding a field to one log line can never disturb
	// another.
	with := func(extra ...any) []any { return append(append([]any{}, fields...), extra...) }

	if spec.GracefulTimeout <= 0 {
		log.Error("escalation child: refusing to run with no graceful timeout to wait out", fields...)
		return 1
	}

	log.Info("escalation child started: waiting out the graceful rung", fields...)

	// Hard bound: whatever else happens, this process is gone by the time the
	// graceful timeout plus the cap is up, so it can never become a permanent
	// orphan on a machine that failed to reboot.
	if !sleepUntil(time.Now().Add(spec.GracefulTimeout), spec.hardBound()) {
		log.Error("escalation child: hard bound reached before the graceful rung was spent, giving up", fields...)
		return 1
	}

	// The kernel permits privileged writes to /proc/sysrq-trigger regardless
	// of /proc/sys/kernel/sysrq, which controls keyboard invocation only.
	// SysRq-b is an immediate reboot with no filesystem sync.
	log.Warn("escalation child: rebooting the machine without syncing filesystems", fields...)
	if err := TriggerForcefulReboot(); err != nil {
		log.Error("escalation child: could not trigger the forceful reboot", with("error", err)...)
		return 1
	}
	return 0
}

// forcefulRungName is how the forceful rung is named in every log line.
const forcefulRungName = "sysrq-b"

// CancelEscalationChildren is used by an explicit service stop or uninstall.
// It only signals processes running this executable with the private child
// flag, so an unrelated process in the service cgroup is left alone.
func CancelEscalationChildren() ([]int, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate own executable: %w", err)
	}
	return cancelEscalationChildren("/proc", exe, func(pid int) error {
		return syscall.Kill(pid, syscall.SIGKILL)
	})
}

func cancelEscalationChildren(procRoot, exe string, kill func(int) error) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procRoot, err)
	}
	var cancelled []int
	var problems []error
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		proc := filepath.Join(procRoot, entry.Name())
		path, err := os.Readlink(filepath.Join(proc, "exe"))
		if err != nil || strings.TrimSuffix(path, " (deleted)") != exe {
			continue
		}
		argv, err := os.ReadFile(filepath.Join(proc, "cmdline"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Errorf("read pid %d command line: %w", pid, err))
			}
			continue
		}
		if !hasArg(argv, "-"+EscalateFlag) {
			continue
		}
		if err := kill(pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			problems = append(problems, fmt.Errorf("kill escalation child %d: %w", pid, err))
			continue
		}
		cancelled = append(cancelled, pid)
	}
	return cancelled, errors.Join(problems...)
}

func hasArg(cmdline []byte, want string) bool {
	for _, arg := range bytes.Split(cmdline, []byte{0}) {
		if string(arg) == want {
			return true
		}
	}
	return false
}

// sleepUntil waits until t in slices, and reports false if bound was reached
// instead, meaning the wait was abandoned. No slice ever runs past the bound,
// because the bound is the only thing making the wait safe.
func sleepUntil(t, bound time.Time) bool {
	for {
		d := time.Until(t)
		if d <= 0 {
			return true
		}
		if d > time.Minute {
			d = time.Minute
		}
		if remaining := time.Until(bound); remaining < d {
			d = remaining
		}
		time.Sleep(d)
		if !time.Now().Before(bound) {
			return false
		}
	}
}

// writeProcFile writes to a /proc node. It does not create the node: if the
// kernel does not offer it, that is an error to report, not a file to leave
// lying around in /proc.
func writeProcFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %q to %s: %w", data, path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
