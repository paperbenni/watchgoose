// Package listener is the dead-man's switch for this machine.
//
// It does three things in one process (docs/adr/0005-single-daemon.md): it
// accepts reassurance from outside, it periodically decides whether that
// reassurance has gone stale, and it repairs the machine and reboots it when
// it has. It never inspects whether the machine is healthy, and it never runs
// a diagnostic to decide whether to act: absence of reassurance is both the
// trigger and the only input (docs/adr/0008-reachability-not-health.md).
//
// The loop is the whole design, so it is worth stating plainly:
//
//	no reassurance within the deadline
//	  -> the uptime floor has been passed
//	    -> repair: the recovery user always, the ordinary accounts only if the
//	       volume holding them is actually mounted
//	    -> settle
//	    -> spawn a child that will take the forceful rung if the reboot below
//	       does not happen
//	    -> systemctl reboot
//	    -> the kernel reboot via sysrq-b, by that child, if nothing else did
//
// Before the reboot ladder starts, a reassurance stands the sequence down.
package listener

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"watchgoose/internal/config"
	"watchgoose/internal/reboot"
	"watchgoose/internal/repair"
	"watchgoose/internal/state"
)

const (
	// defaultConfigPath is where the configuration conventionally lives.
	defaultConfigPath = "/etc/watchgoose.yaml"
	// healthRoute is for a human with curl. It is not a health check: it
	// reports the switch's view of the world, not the machine's health.
	healthRoute = "/health"
	// procUptimePath is where the uptime floor is read from. Deliberately not
	// persisted, which is what lets the switch re-arm on every boot for free.
	procUptimePath = "/proc/uptime"
	// stateDirMode is for the directories holding the state file and the log:
	// root's, and not world-readable.
	stateDirMode fs.FileMode = 0o750
	// logFileMode is the log's mode. The log is an audit trail of a security
	// decision, so it is not group- or world-readable.
	logFileMode fs.FileMode = 0o600
	// shutdownGrace bounds the clean shutdown of the listener.
	shutdownGrace = 5 * time.Second
)

// version is what the startup log and /health report.
var version = "0.1.0"

func Run(args []string) int {
	fs := flag.NewFlagSet("watchgoose listen", flag.ContinueOnError)
	var (
		configPath     = fs.String("config", defaultConfigPath, "path to the configuration file")
		check          = fs.Bool("check", false, "validate the configuration, print the resolved configuration to stdout and exit; touches neither the network, the state file nor the log")
		initialize     = fs.Bool("initialize-first-poke", false, "internal: mark a new installation as waiting for its first reassurance")
		cancelChildren = fs.Bool("cancel-escalation-children", false, "internal: cancel this binary's waiting escalation children")
		cancelOnStop   = fs.Bool("cancel-escalation-children-unless-shutdown", false, "internal: cancel escalation children on an explicit service stop")
		// The flags below are the escalation child's private handshake with its
		// parent, not part of the configuration surface. See internal/reboot.
		escalate       = fs.Bool(reboot.EscalateFlag, false, "internal: become the escalation child")
		escalateGrace  = fs.Duration(reboot.EscalateGracefulArg, 0, "internal: graceful timeout to wait out")
		escalateLogArg = fs.String(reboot.EscalateLogArg, "", "internal: audit log to write to")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"watchgoose %s: a dead-man's switch for one work machine.\n\nUsage: watchgoose listen [flags]\n", version)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "watchgoose listen: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *cancelChildren || *cancelOnStop {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "watchgoose: cancelling escalation children requires root")
			return 1
		}
		if *cancelOnStop {
			result := os.Getenv("SERVICE_RESULT")
			if result != "success" {
				fmt.Fprintf(os.Stderr, "watchgoose: service result %q; preserving escalation children after an unplanned stop\n", result)
				return 0
			}
			probeCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
			out, _ := exec.CommandContext(probeCtx, "systemctl", "is-system-running").Output()
			state := strings.TrimSpace(string(out))
			done()
			if !shouldCancelEscalationChildren(result, state) {
				fmt.Fprintf(os.Stderr, "watchgoose: system state %q; preserving escalation children during shutdown or an unknown manager state\n", state)
				return 0
			}
		}
		cancelled, err := reboot.CancelEscalationChildren()
		if err != nil {
			fmt.Fprintf(os.Stderr, "watchgoose: cancel escalation children: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "watchgoose: cancelled escalation children: %v\n", cancelled)
		return 0
	}

	// The escalation child is the same binary, re-executed, so that it can
	// outlive this process and the systemd teardown that is about to happen.
	if *escalate {
		spec := reboot.EscalationSpec{
			GracefulTimeout: *escalateGrace,
			LogFile:         *escalateLogArg,
		}
		logger, logFile, err := openLog(spec.LogFile, false)
		if err != nil {
			// The child's own log is the one thing it has. Without it, fall
			// back to stderr rather than running mute.
			slog.Error("could not open the audit log; the escalation child will log to stderr", "log_file", spec.LogFile, "error", err)
			return reboot.RunEscalationChild(spec, slog.Default())
		}
		defer logFile.Close()
		return reboot.RunEscalationChild(spec, logger)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		if *check {
			// Nothing is open yet and nothing is running: report on stderr.
			fmt.Fprintf(os.Stderr, "watchgoose: %s is not usable: %v\n", *configPath, err)
			return 1
		}
		slog.Error("could not load the configuration; refusing to start", "config", *configPath, "error", err)
		return 1
	}
	if *check {
		return checkConfig(cfg, *configPath)
	}
	if *initialize {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "watchgoose: initializing the first poke requires root")
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(cfg.Server.StateFile), stateDirMode); err != nil {
			fmt.Fprintf(os.Stderr, "watchgoose: create state directory: %v\n", err)
			return 1
		}
		created, err := state.New(cfg.Server.StateFile).InitializeFirstPoke()
		if err != nil {
			fmt.Fprintf(os.Stderr, "watchgoose: initialize first poke: %v\n", err)
			return 1
		}
		if created {
			fmt.Printf("waiting for first reassurance: %s\n", cfg.Server.StateFile)
		} else {
			fmt.Printf("preserved existing reassurance state: %s\n", cfg.Server.StateFile)
		}
		return 0
	}

	// Repair writes to other people's accounts, the forceful rung writes to
	// /proc, and a reboot needs the machine's own privilege. There is no
	// degraded mode to offer a non-root user, so there is no point pretending.
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr,
			"watchgoose: must run as root, and is running as uid %d. It repairs accounts, arms "+
				"sysrq and reboots the machine, all of which need root.\n", os.Geteuid())
		return 1
	}

	for _, dir := range []string{filepath.Dir(cfg.Log.File), filepath.Dir(cfg.Server.StateFile)} {
		if err := os.MkdirAll(dir, stateDirMode); err != nil {
			slog.Error("could not create a state directory", "dir", dir, "error", err)
			return 1
		}
	}

	// The audit log is kept off the journal on purpose: the journal lives under
	// /run, which is one of the things that has been unreliable on this
	// machine. It is still mirrored to stderr so the journal sees it too.
	logger, logFile, err := openLog(cfg.Log.File, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchgoose: could not open the audit log %s: %v\n", cfg.Log.File, err)
		return 1
	}
	defer logFile.Close()

	d := &daemon{
		cfg:     cfg,
		log:     logger,
		state:   state.New(cfg.Server.StateFile),
		started: time.Now(),
	}

	logger.Info("watchgoose starting",
		"version", version,
		"pid", os.Getpid(),
		"uid", os.Geteuid(),
		"config", *configPath,
		"log_file", cfg.Log.File)
	logger.Info("resolved configuration", "summary", summary(cfg),
		"listen", cfg.Server.Listen,
		"reassurance_path", cfg.Reassurance.Path,
		"reassurance_deadline", cfg.Reassurance.Deadline,
		"poll_interval", cfg.Server.PollInterval,
		"min_uptime", cfg.Guard.MinUptime,
		"volume", volumeSetting(cfg),
		"state_file", cfg.Server.StateFile)
	d.warnIfNeverReassured()

	// SIGTERM is how systemd stops this machine, and how a person stops it by
	// hand. Either way the listener is closed and the loop is left; nothing
	// else changes state.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Bind before announcing readiness, so that a port clash is a startup
	// failure rather than a switch that is not listening and does not know it.
	ln, err := d.listen()
	if err != nil {
		logger.Error("watchgoose stopping", "version", version, "error", err)
		return 1
	}

	if err := d.serve(ctx, ln); err != nil {
		logger.Error("watchgoose stopping", "version", version, "error", err)
		return 1
	}
	logger.Info("watchgoose stopped", "version", version, "uptime", time.Since(d.started).Round(time.Second))
	return 0
}

func shouldCancelEscalationChildren(serviceResult, systemState string) bool {
	return serviceResult == "success" && systemState != "stopping" && systemState != ""
}

// checkConfig prints the resolved configuration. It deliberately touches
// nothing: no network, no state file, no log. Someone running this before an
// install should not be creating directories on the machine.
func checkConfig(cfg config.Config, path string) int {
	fmt.Printf("watchgoose %s: resolved configuration from %s\n\n", version, path)
	fmt.Print(summaryDetail(cfg))
	fmt.Println("\nconfiguration is valid")
	return 0
}

// daemon is the one process that listens, decides and escalates.
type daemon struct {
	cfg     config.Config
	log     *slog.Logger
	state   *state.Store
	started time.Time
	// childPID is the escalation child, so that /health can name it. It is
	// atomic because the poll loop writes it and HTTP handlers read it.
	childPID atomic.Int64
	// lastStateProblem is written only by the poll loop, which is one
	// goroutine, so that a state file that stays broken is complained about
	// once rather than once per poll interval, forever.
	lastStateProblem string
}

// assessment is one look at the machine, and is the only judgement the switch
// ever makes: a timestamp and an uptime, and nothing else.
type assessment struct {
	// last and since are the last reassurance and how long ago it was.
	last  time.Time
	since time.Duration
	// stale is whether the reassurance deadline has passed.
	stale bool
	// uptime is the machine's age, and uptimeKnown is whether it could be
	// read. A floor that cannot be measured is a floor that has not been
	// cleared, so an unreadable /proc/uptime holds the switch.
	uptime      time.Duration
	uptimeKnown bool
	uptimeErr   error
	// floorClear is stale, and the uptime floor has been passed. This is the
	// only thing that has to be true for the switch to act.
	floorClear bool
	// stateProblem describes a missing or unusable state file, for the log.
	stateProblem      string
	awaitingFirstPoke bool
}

// switchState is the one-word summary of the assessment, for /health.
func (a assessment) switchState() string {
	switch {
	case a.awaitingFirstPoke:
		return "awaiting first reassurance"
	case !a.stale:
		return "reassured"
	case !a.floorClear:
		return "stale, uptime floor holding"
	default:
		return "armed"
	}
}

// assess reads the reassurance deadline and the uptime, and decides. It never
// looks at anything else, and never runs a diagnostic: the switch measures
// reachability and nothing more (docs/adr/0008-reachability-not-health.md).
func (d *daemon) assess(now time.Time) assessment {
	var a assessment

	last, err := d.state.ReassuredAt()
	switch {
	case errors.Is(err, state.ErrAwaitingFirstPoke):
		a.awaitingFirstPoke = true
		return a
	case errors.Is(err, state.ErrNotReassured):
		// Installation writes an explicit waiting marker. A missing file now
		// means that marker or a recorded reassurance has been lost.
		a.stateProblem = "no reassurance has ever been recorded"
	case err != nil:
		// Fail-safe. An unreadable or malformed state file must not disarm the
		// switch, so it is treated as having never been reassured.
		a.stateProblem = "the reassurance state file could not be read: " + err.Error()
	default:
		a.last = last
		a.since = now.Sub(last)
	}

	a.stale = state.Stale(last, now, d.cfg.Reassurance.Deadline)
	if !a.stale {
		return a
	}

	uptime, uerr := readUptime()
	if uerr != nil {
		a.uptimeErr = uerr
		return a
	}
	a.uptime, a.uptimeKnown = uptime, true
	// The uptime floor's purpose is to make a boot loop harmless, not to make
	// rebooting rare. It is read from /proc and never persisted, so the switch
	// re-arms on every boot with no state of its own
	// (docs/adr/0001-accept-unattended-reboot-loops.md).
	a.floorClear = uptime >= d.cfg.Guard.MinUptime
	return a
}

// poll is one turn of the switch.
func (d *daemon) poll(ctx context.Context) {
	a := d.assess(time.Now())
	if a.stateProblem != d.lastStateProblem {
		// Distinct on purpose: a machine that has never been poked and a
		// machine that has lost its state file look identical to the switch
		// and are not the same problem. Said once per change rather than once
		// per poll, so that a machine nobody has poked does not fill its own
		// log with the same line every minute.
		if a.stateProblem != "" {
			d.log.Warn("reassurance state: "+a.stateProblem+" — treating the machine as never reassured",
				"state_file", d.state.Path())
		}
		d.lastStateProblem = a.stateProblem
	}
	if !a.stale {
		return
	}

	if a.last.IsZero() {
		d.log.Info("no usable reassurance timestamp; the machine looks unreachable",
			"deadline", d.cfg.Reassurance.Deadline)
	} else {
		d.log.Info("reassurance deadline passed; the machine looks unreachable",
			"since_reassurance", a.since.Round(time.Second),
			"deadline", d.cfg.Reassurance.Deadline)
	}

	if !a.floorClear {
		d.log.Info("uptime floor is holding the switch",
			"uptime", a.uptime.Round(time.Second),
			"min_uptime", d.cfg.Guard.MinUptime)
		if a.uptimeErr != nil {
			d.log.Warn("the uptime could not be read, so the floor is holding by default",
				"min_uptime", d.cfg.Guard.MinUptime, "error", a.uptimeErr)
		}
		return
	}

	d.log.Warn("the switch fires: the machine has been up long enough to be told about the silence",
		"uptime", a.uptime.Round(time.Second),
		"min_uptime", d.cfg.Guard.MinUptime)
	d.escalate(ctx)
}

// escalate is the switch firing, in order. It re-reads the reassurance
// deadline before repair, settling and spawning the reboot child. Once that
// child is spawned, the reboot ladder is committed.
func (d *daemon) escalate(ctx context.Context) {
	// (a) Repair: what a human would have done by hand, before any reboot.
	if !d.checkpoint("repair") {
		return
	}
	report, err := repair.Perform(ctx, d.cfg, d.log)
	if err != nil {
		d.log.Error("repair could not run at all; continuing to the reboot", "error", err)
	} else {
		d.logReport(report)
	}

	// (b) Settle: let the repair take effect before restarting anything.
	if !d.checkpoint("settle") {
		return
	}
	d.log.Info("waiting for the repair to settle", "settle", d.cfg.Repair.Settle)
	if !sleepOrStop(ctx, d.cfg.Repair.Settle) {
		d.log.Info("asked to stop while settling; standing down without rebooting", "settle", d.cfg.Repair.Settle)
		return
	}
	d.engageReboot(ctx, reboot.SpawnEscalationChild, reboot.GracefulReboot)
}

// engageReboot is isolated from repair so the failure paths can be exercised
// without touching accounts or asking this machine to reboot.
func (d *daemon) engageReboot(ctx context.Context,
	spawn func(reboot.EscalationSpec, *slog.Logger) (int, error),
	graceful func(context.Context) error,
) {
	// (c) The escalation child, spawned before the graceful reboot so that a
	// wedged PID 1 cannot stop the ladder.
	if !d.checkpoint("spawn the escalation child") {
		return
	}
	spec := reboot.EscalationSpec{
		GracefulTimeout: d.cfg.Reboot.GracefulTimeout,
		LogFile:         d.cfg.Log.File,
	}
	pid, err := spawn(spec, d.log)
	if err != nil {
		// Not fatal: the graceful rung may still take the machine down, and a
		// switch that gives up because a fork failed is worse than one reboot
		// too few.
		d.log.Error("the escalation child could not be spawned; only the graceful rung remains", "error", err)
	} else {
		d.childPID.Store(int64(pid))
	}
	// An audit trail that names a pid that was never started is a lie.
	child := "none: the child could not be spawned"
	if pid > 0 {
		child = strconv.Itoa(pid)
	}

	// (d) Rung one: the graceful reboot. It is launched and not waited for,
	// because PID 1 may be exactly why the switch is firing.
	d.log.Warn("rung 1 of 2: asking systemd to reboot the machine", "rung", "systemctl reboot")
	gracefulErr := graceful(ctx)
	if gracefulErr != nil {
		d.log.Error("the graceful reboot could not be started", "rung", "systemctl reboot", "error", gracefulErr)
	}
	if pid <= 0 {
		if gracefulErr != nil {
			d.log.Error("neither reboot rung started; returning to the poll loop to retry")
			return
		}
		d.log.Warn("no escalation child is available; waiting one graceful timeout before retrying the switch",
			"retry_in", d.cfg.Reboot.GracefulTimeout)
		_ = sleepOrStop(ctx, d.cfg.Reboot.GracefulTimeout)
		return
	}

	// (e) Wait for the child, but never forever. If it also fails, return to
	// the poll loop so the switch can try again.
	d.log.Warn("the reboot ladder is engaged; this process now waits to be killed by the reboot, "+
		"and the escalation child takes the forceful rung if nothing does",
		"escalation_child", child,
		"forceful_rung_in", d.cfg.Reboot.GracefulTimeout)
	if sleepOrStop(ctx, spec.MaximumWait()+time.Second) {
		d.childPID.Store(0)
		d.log.Error("still running after the escalation child's hard bound; returning to the poll loop to retry",
			"escalation_child", child)
		return
	}
	d.log.Info("daemon received a stop signal while the reboot ladder was engaged",
		"escalation_child", child)
}

// checkpoint re-reads the reassurance deadline immediately before an
// irreversible step. If reassurance has arrived, the sequence is abandoned.
func (d *daemon) checkpoint(stage string) bool {
	last, err := d.state.ReassuredAt()
	if errors.Is(err, state.ErrAwaitingFirstPoke) {
		d.log.Info("waiting for the first reassurance; standing down", "stage", stage)
		return false
	}
	if err != nil && !errors.Is(err, state.ErrNotReassured) {
		// Unreadable or malformed is not reassurance. Carry on, loudly.
		d.log.Warn("could not read the reassurance state before an irreversible step; continuing",
			"stage", stage, "error", err)
		return true
	}
	if !state.Stale(last, time.Now(), d.cfg.Reassurance.Deadline) {
		d.log.Info("reassured, standing down",
			"stage", stage,
			"since_reassurance", time.Since(last).Round(time.Second),
			"deadline", d.cfg.Reassurance.Deadline)
		return false
	}
	return true
}

// logReport says what repair did, one clear line per outcome. Volume present
// and volume absent have different remedies, so they are told apart here
// rather than summed into a single success message
// (docs/adr/0004-repair-skips-accounts-when-volume-absent.md).
func (d *daemon) logReport(r repair.Report) {
	d.log.Info("repair: done",
		"volume_present", r.VolumePresent,
		"volume", volumeSetting(d.cfg),
		"accounts_repaired", r.AccountsRepaired,
		"accounts_skipped", r.AccountsSkipped,
		"recovery_user_created", r.RecoveryUserCreated,
		"unlocked", r.Unlocked,
		"warnings", len(r.Warnings))
	if r.VolumePresent {
		d.log.Info("repair: the volume is mounted, so the ordinary accounts were repaired",
			"volume", volumeSetting(d.cfg),
			"accounts", r.AccountsRepaired)
	} else {
		d.log.Info("repair: the volume is absent, so only the recovery user was repaired. "+
			"Rewriting keys on the mountpoint would change nothing that survives the next boot; "+
			"the reboot is the remedy for a volume that did not mount",
			"volume", volumeSetting(d.cfg),
			"accounts_skipped", r.AccountsSkipped)
	}
	for _, account := range r.AccountsSkipped {
		d.log.Warn("repair: account not repaired", "account", account)
	}
	if r.RecoveryUserCreated {
		d.log.Info("repair: the recovery user did not exist and has been created",
			"recovery_user", d.cfg.Repair.RecoveryUser,
			"recovery_home", d.cfg.Repair.RecoveryHome)
	}
}

// warnIfNeverReassured says the loudest thing it can at the one moment a
// human is most likely to read the journal.
func (d *daemon) warnIfNeverReassured() {
	_, err := d.state.ReassuredAt()
	if errors.Is(err, state.ErrAwaitingFirstPoke) {
		d.log.Info("waiting for the first reassurance before enabling repair and reboot",
			"state_file", d.state.Path(), "reassurance_path", d.cfg.Reassurance.Path)
		return
	}
	if !errors.Is(err, state.ErrNotReassured) {
		return
	}
	d.log.Warn("THERE IS NO REASSURANCE STATE FILE YET: "+d.state.Path()+" does not exist. "+
		"The installation marker or an earlier reassurance was lost; treating the machine as unreassured.",
		"state_file", d.state.Path(),
		"reassurance_path", d.cfg.Reassurance.Path,
		"deadline", d.cfg.Reassurance.Deadline,
		"min_uptime", d.cfg.Guard.MinUptime)
}

// listen binds the reassurance endpoint. It is separate from serve so that the
// bind happens before anything is announced as ready, and so that a test can
// hand in its own listener.
func (d *daemon) listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", d.cfg.Server.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", d.cfg.Server.Listen, err)
	}
	return ln, nil
}

// serve runs the listener and the poll loop, which is the entire process
// (docs/adr/0005-single-daemon.md). It returns when the context is cancelled or
// the listener gives up.
func (d *daemon) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler: d.routes(),
		// A reassurance POST is a single empty request; anything slower than
		// this is not reassurance.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          log.New(logWriter{d.log}, "", 0),
	}

	// The listener is bound by the caller, so a port clash has already failed
	// startup by the time anything is announced as ready.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			d.log.Warn("the listener did not shut down cleanly", "error", err)
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(safeListener{Listener: ln, log: d.log})
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	d.log.Info("listening for reassurance",
		"addr", ln.Addr().String(),
		"reassurance_path", d.cfg.Reassurance.Path,
		"health_path", healthRoute)

	ticker := time.NewTicker(d.cfg.Server.PollInterval)
	defer ticker.Stop()

	// Decide once at startup rather than after a first full interval: a daemon
	// respawned into an hour of silence should not sit there for a minute.
	d.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			d.log.Info("shutting down", "reason", ctx.Err())
			return nil
		case err := <-serveErr:
			return err
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

// routes is the whole external surface of this machine: one route that
// reassures, one that answers a human with curl, and nothing else.
func (d *daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(d.cfg.Reassurance.Path, onlyMethod(http.MethodPost, d.handleReassure))
	mux.Handle(healthRoute, onlyMethod(http.MethodGet, d.handleHealth))
	// Everything else falls through to the mux's own 404.
	return mux
}

// handleReassure records the arrival of reassurance.
//
// The arrival is the entire signal. Nothing about the request is read: not the
// body, not the source, not a signature (docs/adr/0006-unauthenticated-heartbeat.md).
// There is deliberately no authentication and no source filtering — anyone who
// can reach the tailnet address can keep this switch disarmed, and a disarmed
// switch is silent, which is a weakness this design accepts knowingly.
func (d *daemon) handleReassure(w http.ResponseWriter, r *http.Request) {
	if err := d.state.Record(time.Now()); err != nil {
		// The reassurance arrived but was not recorded, so the deadline still
		// runs and the machine will still conclude it is unreachable. Say so
		// loudly rather than reassuring a client that is not being believed.
		d.log.Error("reassurance arrived but could not be recorded",
			"state_file", d.state.Path(), "remote", r.RemoteAddr, "error", err)
		http.Error(w, "could not record reassurance", http.StatusInternalServerError)
		return
	}
	d.log.Info("reassurance received", "remote", r.RemoteAddr, "state_file", d.state.Path())
	// No content, no body: there is nothing for the client to learn from a
	// reply, and nothing for it to keep.
	w.WriteHeader(http.StatusNoContent)
}

// healthResponse is for a human with curl. It describes the switch, not the
// machine: the switch measures reachability and has no opinion about health
// (docs/adr/0008-reachability-not-health.md).
type healthResponse struct {
	Version              string `json:"version"`
	Started              string `json:"started"`
	Uptime               string `json:"uptime"`
	UptimeFloor          string `json:"uptime_floor"`
	SwitchState          string `json:"switch_state"`
	ReassurancePath      string `json:"reassurance_path"`
	ReassuranceDeadline  string `json:"reassurance_deadline"`
	LastReassurance      string `json:"last_reassurance"`
	TimeSinceReassurance string `json:"time_since_reassurance"`
	Stale                bool   `json:"stale"`
	GuardHolding         bool   `json:"guard_holding"`
	Note                 string `json:"note,omitempty"`
	StateFile            string `json:"state_file"`
	EscalationChildPID   int    `json:"escalation_child_pid,omitempty"`
}

func (d *daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	a := d.assess(now)
	resp := healthResponse{
		Version:              version,
		Started:              d.started.Format(time.RFC3339),
		Uptime:               now.Sub(d.started).Round(time.Second).String(),
		UptimeFloor:          d.cfg.Guard.MinUptime.String(),
		SwitchState:          a.switchState(),
		ReassurancePath:      d.cfg.Reassurance.Path,
		ReassuranceDeadline:  d.cfg.Reassurance.Deadline.String(),
		TimeSinceReassurance: "never",
		Stale:                a.stale,
		GuardHolding:         a.stale && !a.floorClear,
		StateFile:            d.state.Path(),
		EscalationChildPID:   int(d.childPID.Load()),
	}
	if !a.last.IsZero() {
		resp.LastReassurance = a.last.Format(time.RFC3339Nano)
		resp.TimeSinceReassurance = a.since.Round(time.Second).String()
	}
	if a.uptimeKnown {
		resp.Note = "machine uptime " + a.uptime.Round(time.Second).String()
	}
	if a.uptimeErr != nil {
		resp.Note = "machine uptime unreadable, so the floor is holding"
	}
	if a.stateProblem != "" {
		resp.Note = strings.TrimSpace(resp.Note + "; " + a.stateProblem)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}

// onlyMethod turns a wrong method into a 405 rather than letting it reach the
// handler: a reassurance is a POST and nothing else.
func onlyMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// logWriter adapts slog to the io.Writer net/http wants for its own
// complaints, so that nothing the listener says escapes the audit trail.
type logWriter struct{ log *slog.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Warn("http: " + strings.TrimSpace(string(p)))
	return len(p), nil
}

// safeListener contains a panic to the connection that caused it. net/http
// already recovers handler panics, but a panic raised in the connection's own
// read or write path would otherwise take the whole listener down, and a
// listener that cannot die is the entire point of there being one process
// (docs/adr/0005-single-daemon.md).
type safeListener struct {
	net.Listener
	log *slog.Logger
}

func (l safeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return safeConn{Conn: c, log: l.log}, nil
}

type safeConn struct {
	net.Conn
	log *slog.Logger
}

func (c safeConn) Read(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, c.dropped("read", r)
		}
	}()
	return c.Conn.Read(p)
}

func (c safeConn) Write(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, c.dropped("write", r)
		}
	}()
	return c.Conn.Write(p)
}

// dropped logs a panic and returns the error that makes net/http tear the
// connection down. Carrying on with a socket whose framing is now anyone's
// guess would be worse than losing the connection.
func (c safeConn) dropped(direction string, panicValue any) error {
	if c.log != nil {
		c.log.Error("http: panic in a connection, dropping the connection",
			"direction", direction,
			"remote", c.Conn.RemoteAddr().String(),
			"panic", fmt.Sprint(panicValue),
			"stack", string(debug.Stack()))
	}
	return fmt.Errorf("connection dropped after a panic in the %s path", direction)
}

// readUptime reads the machine's age from /proc/uptime, whose first field is
// seconds since boot. The uptime floor is deliberately not persisted, which is
// what lets the switch re-arm on every boot without keeping any state
// (docs/adr/0001-accept-unattended-reboot-loops.md).
func readUptime() (time.Duration, error) {
	raw, err := os.ReadFile(procUptimePath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", procUptimePath, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("%s is empty", procUptimePath)
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s (%q): %w", procUptimePath, fields[0], err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// sleepOrStop waits, and reports false if the daemon was asked to stop first.
func sleepOrStop(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// openLog opens the audit log for append, creating it if needed and leaving
// the descriptor open for the life of the process. It is mirrored to stderr so
// that the journal sees the same lines, which is how an operator watching
// `journalctl -fu watchgoose` gets the story.
func openLog(path string, mirrorStderr bool) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(path), stateDirMode); err != nil {
		return nil, nil, fmt.Errorf("create the log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	if mirrorStderr {
		w = io.MultiWriter(f, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})), f, nil
}

// volumeSetting is the mountpoint, or a word saying there is not one.
func volumeSetting(cfg config.Config) string {
	if cfg.Volume.Mountpoint == "" {
		return "none configured"
	}
	return cfg.Volume.Mountpoint
}

// summary is the whole configuration on one line, for the startup log.
func summary(cfg config.Config) string {
	return strings.Join(summaryLines(cfg), "; ")
}

// summaryDetail is the same thing, one setting per line, for -check.
func summaryDetail(cfg config.Config) string {
	lines := summaryLines(cfg)
	var b strings.Builder
	width := 0
	for _, l := range lines {
		if key, _, _ := strings.Cut(l, "="); len(key) > width {
			width = len(key)
		}
	}
	for _, l := range lines {
		key, value, _ := strings.Cut(l, "=")
		fmt.Fprintf(&b, "  %-*s  %s\n", width, key, value)
	}
	return b.String()
}

// summaryLines is the configuration as greppable key=value pairs. Keys are the
// YAML field names, so a line in the log can be matched against the
// configuration file that produced it.
func summaryLines(cfg config.Config) []string {
	return []string{
		"server.listen=" + cfg.Server.Listen,
		"server.poll_interval=" + cfg.Server.PollInterval.String(),
		"server.state_file=" + cfg.Server.StateFile,
		"reassurance.path=" + cfg.Reassurance.Path,
		"reassurance.deadline=" + cfg.Reassurance.Deadline.String(),
		"guard.min_uptime=" + cfg.Guard.MinUptime.String(),
		"volume.mountpoint=" + volumeSetting(cfg),
		"repair.settle=" + cfg.Repair.Settle.String(),
		"repair.accounts=" + orNone(strings.Join(cfg.Repair.Accounts, ", ")),
		"repair.authorized_keys=" + plural(len(cfg.Repair.AuthorizedKeys), "key"),
		"repair.recovery_user=" + cfg.Repair.RecoveryUser,
		"repair.recovery_home=" + cfg.Repair.RecoveryHome,
		"repair.recovery_nopasswd_sudo=" + strconv.FormatBool(cfg.Repair.RecoveryNopasswdSudo),
		"reboot.graceful_timeout=" + cfg.Reboot.GracefulTimeout.String(),
		"log.log_file=" + cfg.Log.File,
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
