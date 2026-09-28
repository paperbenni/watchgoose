package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"watchgoose/internal/config"
	"watchgoose/internal/reboot"
	"watchgoose/internal/state"
)

func TestNoRebootBackstopReturnsToPolling(t *testing.T) {
	d, _ := testDaemon(t)
	if err := d.state.Record(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	d.cfg.Reboot.GracefulTimeout = 20 * time.Millisecond
	spawn := func(reboot.EscalationSpec, *slog.Logger) (int, error) { return 0, errors.New("fork unavailable") }
	start := time.Now()
	d.engageReboot(context.Background(), spawn, func(context.Context) error { return errors.New("systemd unavailable") })
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("both rungs failed but the poll loop stayed blocked for %s", elapsed)
	}
	start = time.Now()
	d.engageReboot(context.Background(), spawn, func(context.Context) error { return nil })
	if elapsed := time.Since(start); elapsed < d.cfg.Reboot.GracefulTimeout || elapsed > time.Second {
		t.Fatalf("no child after graceful request: returned after %s, want one bounded timeout", elapsed)
	}
}

func TestEscalationChildCancellationOnlyOnExplicitStop(t *testing.T) {
	cases := []struct {
		result, state string
		want          bool
	}{
		{"success", "running", true},
		{"success", "degraded", true},
		{"success", "stopping", false},
		{"success", "", false},
		{"exit-code", "running", false},
	}
	for _, tc := range cases {
		if got := shouldCancelEscalationChildren(tc.result, tc.state); got != tc.want {
			t.Errorf("result=%q state=%q: cancel=%v, want %v", tc.result, tc.state, got, tc.want)
		}
	}
}

func TestStopAfterRebootRequestLogsNoConclusionAboutReboot(t *testing.T) {
	d, _ := testDaemon(t)
	if err := d.state.Record(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var audit bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&audit, nil))
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		d.engageReboot(ctx,
			func(reboot.EscalationSpec, *slog.Logger) (int, error) { return 42, nil },
			func(context.Context) error { close(started); return nil })
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not release the daemon")
	}
	if strings.Contains(audit.String(), "the reboot is not happening") {
		t.Fatalf("audit log made a false claim after SIGTERM: %s", audit.String())
	}
}

func TestMissingReassuranceHasNoInventedElapsedDuration(t *testing.T) {
	d, cfg := testDaemon(t)
	d.cfg.Guard.MinUptime = 100 * 365 * 24 * time.Hour
	if err := os.WriteFile(cfg.Server.StateFile, []byte("malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var audit bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&audit, nil))
	d.poll(context.Background())
	if strings.Contains(audit.String(), "since_reassurance=0s") || strings.Contains(audit.String(), "reassurance deadline passed") {
		t.Fatalf("audit log invented a measured silence duration: %s", audit.String())
	}
}

// testDaemon is a daemon whose every path is in a temporary directory. It
// never binds, never writes to /etc and never reboots anything: assess reads
// /proc/uptime, which is the one piece of real state the switch consults.
func testDaemon(t *testing.T) (*daemon, config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.PollInterval = time.Minute
	cfg.Server.StateFile = filepath.Join(dir, "last-reassurance")
	cfg.Reassurance.Path = "/reassure"
	cfg.Reassurance.Deadline = 20 * time.Minute
	cfg.Guard.MinUptime = time.Nanosecond // this machine is certainly up
	cfg.Volume.Mountpoint = ""
	cfg.Repair.RecoveryHome = filepath.Join(dir, "recovery")
	cfg.Repair.AuthorizedKeys = []string{"ssh-ed25519 AAAA test"}
	cfg.Log.File = filepath.Join(dir, "watchgoose.log")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the test configuration is not valid: %v", err)
	}
	return &daemon{
		cfg:     cfg,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		state:   state.New(cfg.Server.StateFile),
		started: time.Now().Add(-time.Hour),
	}, cfg
}

func TestAssess(t *testing.T) {
	cases := map[string]struct {
		// last is written to the state file; "missing" leaves it absent.
		last          string
		minUptime     time.Duration
		wantStale     bool
		wantFloorClr  bool
		wantState     string
		wantLastIsSet bool
	}{
		"reassured just now": {
			last: time.Now().Format(time.RFC3339Nano), minUptime: time.Hour,
			wantStale: false, wantFloorClr: false, wantState: "reassured", wantLastIsSet: true,
		},
		"fresh install awaits first poke even past uptime floor": {
			last: "pending", minUptime: time.Nanosecond,
			wantStale: false, wantFloorClr: false, wantState: "awaiting first reassurance",
		},
		"reassured a minute ago": {
			last: time.Now().Add(-time.Minute).Format(time.RFC3339Nano), minUptime: time.Hour,
			wantStale: false, wantFloorClr: false, wantState: "reassured", wantLastIsSet: true,
		},
		"stale but the machine has just booted": {
			last: time.Now().Add(-time.Hour).Format(time.RFC3339Nano), minUptime: 100 * 365 * 24 * time.Hour,
			wantStale: true, wantFloorClr: false, wantState: "stale, uptime floor holding", wantLastIsSet: true,
		},
		"stale and the machine has been up long enough": {
			last: time.Now().Add(-time.Hour).Format(time.RFC3339Nano), minUptime: time.Nanosecond,
			wantStale: true, wantFloorClr: true, wantState: "armed", wantLastIsSet: true,
		},
		"missing state after installation, and long past the uptime floor": {
			last: "missing", minUptime: time.Nanosecond,
			wantStale: true, wantFloorClr: true, wantState: "armed",
		},
		"missing state after installation, and the machine has just booted": {
			// A lost state file is not the fresh-install marker.
			last: "missing", minUptime: 100 * 365 * 24 * time.Hour,
			wantStale: true, wantFloorClr: false, wantState: "stale, uptime floor holding",
		},
		"a corrupt state file is stale, not reassurance": {
			last: "corrupt", minUptime: time.Nanosecond,
			wantStale: true, wantFloorClr: true, wantState: "armed",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d, cfg := testDaemon(t)
			d.cfg.Guard.MinUptime = tc.minUptime
			switch tc.last {
			case "pending":
				if _, err := d.state.InitializeFirstPoke(); err != nil {
					t.Fatalf("initialize: %v", err)
				}
			case "missing":
			case "corrupt":
				if err := os.WriteFile(cfg.Server.StateFile, []byte("not a timestamp\n"), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			default:
				if err := d.state.Record(mustParse(t, tc.last)); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			a := d.assess(time.Now())
			if a.stale != tc.wantStale {
				t.Errorf("stale = %v, want %v", a.stale, tc.wantStale)
			}
			if a.floorClear != tc.wantFloorClr {
				t.Errorf("floorClear = %v, want %v", a.floorClear, tc.wantFloorClr)
			}
			if got := a.switchState(); got != tc.wantState {
				t.Errorf("switchState = %q, want %q", got, tc.wantState)
			}
			if a.last.IsZero() == tc.wantLastIsSet {
				t.Errorf("last is zero = %v, want %v", a.last.IsZero(), !tc.wantLastIsSet)
			}
			if tc.last == "missing" && a.stateProblem == "" {
				t.Error("a missing state file was not reported as a problem worth logging")
			}
			if tc.last == "pending" && !a.awaitingFirstPoke {
				t.Error("fresh installation did not wait for its first poke")
			}
			if tc.last == "corrupt" && a.stateProblem == "" {
				t.Error("a corrupt state file was not reported as a problem worth logging")
			}
		})
	}
}

// The poke that stops the loop has to work at every step, not only between
// polls, so the checkpoint is what stands between a reassurance and an
// irreversible act.
func TestCheckpointStandsDownAfterAPoke(t *testing.T) {
	d, cfg := testDaemon(t)
	if _, err := d.state.InitializeFirstPoke(); err != nil {
		t.Fatal(err)
	}
	if d.checkpoint("repair") {
		t.Fatal("repair began before the first poke")
	}

	if err := os.Remove(cfg.Server.StateFile); err != nil {
		t.Fatal(err)
	}
	if !d.checkpoint("repair") {
		t.Fatal("a missing state file disarmed an installed switch")
	}

	if err := d.state.Record(time.Now()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	for _, stage := range []string{"repair", "settle", "spawn the escalation child", "the graceful reboot"} {
		if d.checkpoint(stage) {
			t.Errorf("the checkpoint before %q did not stand down after reassurance", stage)
		}
	}

	// And once the reassurance is older than the deadline, the sequence goes
	// ahead again. Nothing latches: a switch that latched would need state it
	// deliberately does not have.
	if err := d.state.Record(time.Now().Add(-2 * cfg.Reassurance.Deadline)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !d.checkpoint("the graceful reboot") {
		t.Error("the checkpoint stood down on stale reassurance")
	}
}

// A reassurance is a bare arrival: no body, no signature, no source check. The
// body is not even read, and must not be able to affect the answer.
func TestHandleReassure(t *testing.T) {
	d, cfg := testDaemon(t)
	handler := d.routes()

	req := httptest.NewRequest(http.MethodPost, cfg.Reassurance.Path, strings.NewReader("this body is ignored"))
	req.RemoteAddr = "100.64.0.7:5555"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want nothing", body)
	}
	got, err := d.state.ReassuredAt()
	if err != nil {
		t.Fatalf("reassurance was not recorded: %v", err)
	}
	if time.Since(got) > time.Minute {
		t.Errorf("the recorded reassurance is %s old", time.Since(got))
	}

	// A reassurance is a POST and nothing else.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.Reassurance.Path, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on the reassurance path = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestOtherRoutesAreNotFound(t *testing.T) {
	d, _ := testDaemon(t)
	handler := d.routes()
	for _, path := range []string{"/", "/metrics", "/reassure/extra", "/health/", "/admin"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestHandleHealthDescribesTheSwitchAndNotTheMachine(t *testing.T) {
	d, cfg := testDaemon(t)
	if err := d.state.Record(time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	d.cfg.Guard.MinUptime = 100 * 365 * 24 * time.Hour

	rec := httptest.NewRecorder()
	d.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, healthRoute, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the health response is not JSON: %v (%s)", err, rec.Body.String())
	}
	for _, field := range []string{"uptime", "time_since_reassurance", "stale", "guard_holding", "state_file"} {
		if _, ok := got[field]; !ok {
			t.Errorf("the health response has no %q: %s", field, rec.Body.String())
		}
	}
	if got["state_file"] != cfg.Server.StateFile {
		t.Errorf("state_file = %v, want %q", got["state_file"], cfg.Server.StateFile)
	}
	if got["stale"] != true {
		t.Errorf("stale = %v, want true: reassurance is an hour old", got["stale"])
	}
	if got["guard_holding"] != true {
		t.Errorf("guard_holding = %v, want true: the uptime floor is hours away", got["guard_holding"])
	}
	if got["switch_state"] != "stale, uptime floor holding" {
		t.Errorf("switch_state = %v", got["switch_state"])
	}

	// And a fresh reassurance disarms it.
	if err := d.state.Record(time.Now()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	rec = httptest.NewRecorder()
	d.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, healthRoute, nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the health response is not JSON: %v", err)
	}
	if got["stale"] != false || got["guard_holding"] != false {
		t.Errorf("stale = %v, guard_holding = %v; want both false just after reassurance", got["stale"], got["guard_holding"])
	}
	if got["switch_state"] != "reassured" {
		t.Errorf("switch_state = %v, want reassured", got["switch_state"])
	}
}

// The whole point of the design is that this one process does all three jobs,
// so the three are tested together over a real socket. The uptime floor is set
// out of reach, so the switch can be observed deciding not to act without
// anything in this test being able to repair or reboot anything.
func TestServeListensReassuresAndStaysHarmless(t *testing.T) {
	d, cfg := testDaemon(t)
	d.cfg.Guard.MinUptime = 100 * 365 * 24 * time.Hour
	d.cfg.Server.PollInterval = 10 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- d.serve(ctx, ln) }()

	// A poke, with a body that means nothing at all.
	resp, err := http.Post(base+cfg.Reassurance.Path, "application/octet-stream", strings.NewReader("ignore me"))
	if err != nil {
		t.Fatalf("post reassurance: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST %s = %d, want %d", cfg.Reassurance.Path, resp.StatusCode, http.StatusNoContent)
	}
	if _, err := d.state.ReassuredAt(); err != nil {
		t.Errorf("the reassurance did not reach the state file: %v", err)
	}

	// The health endpoint answers a human with curl.
	get, err := http.Get(base + healthRoute)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want %d", healthRoute, get.StatusCode, http.StatusOK)
	}
	var health map[string]any
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("the health response is not JSON: %v (%s)", err, body)
	}
	if health["stale"] != false {
		t.Errorf("stale = %v, want false immediately after reassurance", health["stale"])
	}

	// Anything else is not a route on this machine.
	missing, err := http.Get(base + "/admin")
	if err != nil {
		t.Fatalf("get /admin: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("GET /admin = %d, want %d", missing.StatusCode, http.StatusNotFound)
	}

	// Let a few poll intervals pass. The switch must stay put: the machine has
	// been reassured, and even if it had not, the uptime floor holds it.
	time.Sleep(50 * time.Millisecond)
	if _, err := d.state.ReassuredAt(); err != nil {
		t.Errorf("polling disturbed the state file: %v", err)
	}
	if d.childPID.Load() != 0 {
		t.Error("the switch escalated; nothing in this test should have")
	}

	// And a clean stop.
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("serve returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the context was cancelled")
	}
}

func TestReadUptime(t *testing.T) {
	got, err := readUptime()
	if err != nil {
		t.Fatalf("readUptime: %v", err)
	}
	if got <= 0 {
		t.Errorf("readUptime = %s, want a positive duration", got)
	}
	if got > 100*365*24*time.Hour {
		t.Errorf("readUptime = %s, which is not a machine uptime", got)
	}
}

// The summary is what a human reads in the journal and in -check, and it is
// keyed by the YAML field names so that a line can be matched against the
// configuration file that produced it.
func TestSummaryIsKeyedByConfigurationFieldName(t *testing.T) {
	_, cfg := testDaemon(t)
	cfg.Repair.Accounts = []string{"benjamin", "ubuntu"}
	cfg.Repair.AuthorizedKeys = []string{"ssh-ed25519 AAAA one", "ssh-ed25519 AAAA two"}
	cfg.Repair.RecoveryNopasswdSudo = true
	lines := strings.Join(summaryLines(cfg), "\n")

	for _, want := range []string{
		"server.listen=" + cfg.Server.Listen,
		"reassurance.deadline=20m0s",
		"guard.min_uptime=1ns",
		"repair.accounts=benjamin, ubuntu",
		"repair.authorized_keys=2 keys",
		"repair.recovery_nopasswd_sudo=true",
		"reboot.graceful_timeout=5m0s",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the summary does not contain %q:\n%s", want, lines)
		}
	}
	if !strings.Contains(summary(cfg), "server.listen=") {
		t.Error("the one-line summary is not the same set of settings")
	}
	// A machine with no volume says so rather than showing an empty value.
	if !strings.Contains(lines, "volume.mountpoint=none configured") {
		t.Errorf("the summary does not say that there is no volume:\n%s", lines)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return got
}
