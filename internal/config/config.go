// Package config defines watchgoose's configuration schema and how it is loaded.
//
// The configuration is a single YAML document, conventionally installed to
// /etc/watchgoose.yaml. Every duration is a Go duration string ("20m", "90s").
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole of watchgoose's configuration.
type Config struct {
	Server      Server      `yaml:"server"`
	Reassurance Reassurance `yaml:"reassurance"`
	Guard       Guard       `yaml:"guard"`
	Volume      Volume      `yaml:"volume"`
	Repair      Repair      `yaml:"repair"`
	Reboot      Reboot      `yaml:"reboot"`
	Log         Log         `yaml:"log"`
}

// Server describes the listener the daemon binds, and where it keeps the
// timestamp of the last reassurance it received.
type Server struct {
	// Listen is the bind address. Bind to a tailnet address rather than
	// 0.0.0.0 unless you have a reason not to.
	Listen string `yaml:"listen"`
	// PollInterval is how often the daemon re-evaluates whether
	// reassurance has gone stale. Keep it well under Deadline.
	PollInterval time.Duration `yaml:"poll_interval"`
	// StateFile holds the time of the last reassurance. It is written to
	// disk rather than kept in memory so that a crash and respawn is not
	// mistaken for silence.
	StateFile string `yaml:"state_file"`
}

// Reassurance is the contract with whatever pokes the machine from outside.
type Reassurance struct {
	// Path is the HTTP route the client POSTs to. The body is ignored;
	// the arrival is the entire signal.
	Path string `yaml:"path"`
	// Deadline is how long without reassurance before the machine
	// concludes it is unreachable. Must be comfortably larger than the
	// client's poke interval so a single missed poke is not silence.
	Deadline time.Duration `yaml:"deadline"`
}

// Guard is the boot-loop protection.
type Guard struct {
	// MinUptime is how long the machine must have been up before the
	// switch is allowed to act. Read from /proc/uptime, not persisted.
	MinUptime time.Duration `yaml:"min_uptime"`
}

// Volume describes the data disk that holds the ordinary user home
// directories. Repair of those accounts is pointless when it is absent,
// because writes to the mountpoint land on the root disk and are shadowed
// the moment the volume mounts on a later boot.
type Volume struct {
	// Mountpoint to check. Leave empty to treat the volume as always
	// present, which is only correct for machines without one.
	Mountpoint string `yaml:"mountpoint"`
}

// Repair is what the machine does to itself before rebooting.
type Repair struct {
	// Settle is how long to wait after repairing before the first reboot
	// attempt.
	Settle time.Duration `yaml:"settle"`
	// Accounts are the ordinary accounts to keep logged in. They are
	// repaired only when the volume is present, and are unlocked either
	// way when present.
	Accounts []string `yaml:"accounts"`
	// AuthorizedKeys are written to the recovery user and, when the volume
	// is present, to each account above. These are public keys.
	AuthorizedKeys []string `yaml:"authorized_keys"`
	// RecoveryUser is the standing account whose credentials live on the
	// root disk, so that a human has somewhere to get in when the
	// ordinary accounts are unreachable.
	RecoveryUser string `yaml:"recovery_user"`
	// RecoveryHome must be outside the volume mountpoint.
	RecoveryHome string `yaml:"recovery_home"`
	// RecoveryNopasswdSudo grants the recovery user passwordless sudo.
	RecoveryNopasswdSudo bool `yaml:"recovery_nopasswd_sudo"`
}

// Reboot is the escalation ladder.
type Reboot struct {
	// GracefulTimeout is how long to wait for a graceful reboot before
	// escalating. The forceful rung itself is deliberately not
	// configurable; see docs/adr/0002-escalate-to-sysrq-b.md.
	GracefulTimeout time.Duration `yaml:"graceful_timeout"`
}

// Log is where the daemon's own audit trail goes. It is separate from the
// journal because the journal is not available in every failure this tool
// exists to handle.
type Log struct {
	File string `yaml:"log_file"`
}

// Default returns a configuration with every field populated to a usable
// value, so that a partial YAML file only overrides what it names.
func Default() Config {
	return Config{
		Server: Server{
			Listen:       "127.0.0.1:9099",
			PollInterval: time.Minute,
			StateFile:    "/srv/watchgoose/var/last-reassurance",
		},
		Reassurance: Reassurance{
			Path:     "/reassure",
			Deadline: 20 * time.Minute,
		},
		Guard:  Guard{MinUptime: 30 * time.Minute},
		Volume: Volume{Mountpoint: ""},
		Repair: Repair{
			Settle:         time.Minute,
			Accounts:       nil,
			AuthorizedKeys: nil,
			RecoveryUser:   "recovery",
			RecoveryHome:   "/srv/recovery",
		},
		Reboot: Reboot{GracefulTimeout: 5 * time.Minute},
		Log:    Log{File: "/srv/watchgoose/var/watchgoose.log"},
	}
}

// Load reads a YAML configuration over the defaults and validates the
// result. The file is owned by root and is the only configuration input.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate rejects configurations that would misbehave at runtime, rather
// than letting a typo become a switch that never fires or one that always
// does.
func (c Config) Validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen must be set")
	}
	if c.Server.PollInterval <= 0 {
		return fmt.Errorf("server.poll_interval must be positive")
	}
	if c.Server.StateFile == "" {
		return fmt.Errorf("server.state_file must be set")
	}
	if c.Reassurance.Path == "" || c.Reassurance.Path[0] != '/' {
		return fmt.Errorf("reassurance.path must be an absolute path")
	}
	if c.Reassurance.Deadline <= c.Server.PollInterval {
		return fmt.Errorf("reassurance.deadline (%s) must exceed server.poll_interval (%s), "+
			"otherwise the switch will fire between pokes", c.Reassurance.Deadline, c.Server.PollInterval)
	}
	if c.Guard.MinUptime <= 0 {
		return fmt.Errorf("guard.min_uptime must be positive")
	}
	if c.Reboot.GracefulTimeout <= 0 {
		return fmt.Errorf("reboot.graceful_timeout must be positive")
	}
	if c.Repair.RecoveryUser == "" || c.Repair.RecoveryHome == "" {
		return fmt.Errorf("repair.recovery_user and repair.recovery_home must both be set")
	}
	if c.Volume.Mountpoint != "" && isUnder(c.Repair.RecoveryHome, c.Volume.Mountpoint) {
		return fmt.Errorf("repair.recovery_home (%s) is inside volume.mountpoint (%s); "+
			"the recovery user must live on the root disk or it will be lost with the volume",
			c.Repair.RecoveryHome, c.Volume.Mountpoint)
	}
	for _, k := range c.Repair.AuthorizedKeys {
		if len(k) < 3 || k[:3] != "ssh" && k[:3] != "ecd" && k[:3] != "sk-" {
			return fmt.Errorf("repair.authorized_keys contains something that is not a public key: %.20q", k)
		}
	}
	return nil
}

// isUnder reports whether path is mountpoint or lies beneath it.
func isUnder(path, mountpoint string) bool {
	if mountpoint == "" {
		return false
	}
	if path == mountpoint {
		return true
	}
	if len(mountpoint) > 0 && mountpoint[len(mountpoint)-1] == '/' {
		mountpoint = mountpoint[:len(mountpoint)-1]
	}
	return len(path) > len(mountpoint) && path[:len(mountpoint)+1] == mountpoint+"/"
}
