package setup

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"
	"watchgoose/deploy"
	"watchgoose/internal/config"
)

const (
	listenerConfigPath = "/etc/watchgoose.yaml"
	listenerUnitPath   = "/etc/systemd/system/watchgoose.service"
	listenerBinaryPath = "/srv/watchgoose/watchgoose"
)

func promptListener(initial config.Config) (config.Config, []byte, error) {
	cfg := initial
	listen := cfg.Server.Listen
	keys := strings.Join(cfg.Repair.AuthorizedKeys, "\n")
	mountpoint := cfg.Volume.Mountpoint
	accounts := strings.Join(cfg.Repair.Accounts, ", ")
	sudo := cfg.Repair.RecoveryNopasswdSudo
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Tailnet listen address (IP:port)").Description("Detected from Tailscale when available; edit as needed.").Placeholder("100.64.0.10:9099").Value(&listen).
			Validate(validateListen),
		huh.NewText().Title("Recovery SSH public keys (one per line)").Description("Review these login keys; keep only keys whose private halves you can use.").Value(&keys).
			Validate(func(s string) error {
				if len(splitLines(s)) == 0 {
					return fmt.Errorf("enter at least one public key")
				}
				return nil
			}),
		huh.NewInput().Title("Separate home volume mountpoint (blank if none)").Description("Detected from mounted /home or your mounted home directory.").Value(&mountpoint),
		huh.NewInput().Title("Accounts to repair (comma separated, blank if none)").Value(&accounts),
		huh.NewConfirm().Title("Give recovery user passwordless sudo?").Value(&sudo),
	)).Run(); err != nil {
		return cfg, nil, err
	}
	cfg.Server.Listen = strings.TrimSpace(listen)
	if err := validateListen(cfg.Server.Listen); err != nil {
		return cfg, nil, err
	}
	cfg.Repair.AuthorizedKeys = splitLines(keys)
	cfg.Volume.Mountpoint = strings.TrimSpace(mountpoint)
	cfg.Repair.Accounts = splitComma(accounts)
	cfg.Repair.RecoveryNopasswdSudo = sudo
	if err := cfg.Validate(); err != nil {
		return cfg, nil, err
	}
	data, err := listenerYAML(cfg)
	return cfg, data, err
}

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if host == "" {
		return fmt.Errorf("listen address needs a private or tailnet host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("listen address needs a port from 1 to 65535")
	}
	return nil
}

func splitLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if value := strings.TrimSpace(line); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func splitComma(text string) []string {
	var out []string
	for _, entry := range strings.Split(text, ",") {
		if value := strings.TrimSpace(entry); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// State user choices only; the listener supplies its defaults. Load the
// generated YAML with config.Load before installing it to catch schema drift.
func listenerYAML(cfg config.Config) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"server": map[string]any{"listen": cfg.Server.Listen},
		"volume": map[string]any{"mountpoint": cfg.Volume.Mountpoint},
		"repair": map[string]any{
			"authorized_keys":        cfg.Repair.AuthorizedKeys,
			"accounts":               cfg.Repair.Accounts,
			"recovery_nopasswd_sudo": cfg.Repair.RecoveryNopasswdSudo,
		},
	})
}

func setupListener(binary string) error {
	var cfg config.Config
	var initial config.Config
	var data []byte
	var writeConfig bool
	if exists(listenerConfigPath) {
		var replace bool
		if err := huh.NewConfirm().Title("Replace the existing listener config?").Value(&replace).Run(); err != nil {
			return err
		}
		if !replace {
			var err error
			cfg, err = config.Load(listenerConfigPath)
			if err != nil {
				return fmt.Errorf("existing config: %w", err)
			}
		} else {
			writeConfig = true
			if existing, err := config.Load(listenerConfigPath); err == nil {
				initial = existing
			} else {
				initial = detectedListenerDefaults()
			}
		}
	} else {
		writeConfig = true
		initial = detectedListenerDefaults()
	}
	if writeConfig {
		var err error
		cfg, data, err = promptListener(initial)
		if err != nil {
			return err
		}
	}
	stage, err := os.MkdirTemp("", "watchgoose-listener-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if writeConfig {
		check := filepath.Join(stage, "watchgoose.yaml")
		if err := os.WriteFile(check, data, 0600); err != nil {
			return err
		}
		if _, err := config.Load(check); err != nil {
			return fmt.Errorf("generated config: %w", err)
		}
	}
	unit := filepath.Join(stage, "watchgoose.service")
	if err := os.WriteFile(unit, deploy.ListenerUnit, 0600); err != nil {
		return err
	}
	if err := root("install", "-d", "-m", "0750", "-o", "root", "-g", "root", "/srv/watchgoose", "/srv/watchgoose/var"); err != nil {
		return err
	}
	if writeConfig {
		if err := installFile(filepath.Join(stage, "watchgoose.yaml"), listenerConfigPath, "0644"); err != nil {
			return err
		}
	}
	fresh := !exists(listenerUnitPath)
	if err := installFile(binary, listenerBinaryPath, "0755"); err != nil {
		return err
	}
	if fresh {
		if err := root(listenerBinaryPath, "listen", "-config", listenerConfigPath, "-initialize-first-poke"); err != nil {
			return err
		}
	}
	if err := installFile(unit, listenerUnitPath, "0644"); err != nil {
		return err
	}
	if err := root("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := root("systemctl", "enable", "watchgoose.service"); err != nil {
		return err
	}
	if err := root("systemctl", "restart", "watchgoose.service"); err != nil {
		return err
	}
	if err := root("systemctl", "is-active", "--quiet", "watchgoose.service"); err != nil {
		return err
	}
	if err := root("systemctl", "is-enabled", "--quiet", "watchgoose.service"); err != nil {
		return err
	}
	fmt.Printf("\nListener installed and running.\n  Binary: %s\n  Config: %s\n  Unit: %s\n", listenerBinaryPath, listenerConfigPath, listenerUnitPath)
	fmt.Printf("  Reassurance URL: http://%s%s\n", cfg.Server.Listen, cfg.Reassurance.Path)
	fmt.Println("  Logs: journalctl -u watchgoose.service -f")
	if fresh {
		fmt.Println("  The switch waits for its first successful poke before it can repair or reboot.")
	}
	return nil
}
