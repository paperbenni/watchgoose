package setup

import (
	"os"
	"path/filepath"
	"testing"

	"watchgoose/internal/config"
)

func TestListenerYAMLLoadsAsListenerConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Listen = "100.64.0.10:9099"
	cfg.Volume.Mountpoint = "/home"
	cfg.Repair.AuthorizedKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAa user@laptop"}
	cfg.Repair.Accounts = []string{"alice", "bob"}
	cfg.Repair.RecoveryNopasswdSudo = true
	data, err := listenerYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "watchgoose.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != cfg.Server.Listen || loaded.Volume.Mountpoint != cfg.Volume.Mountpoint ||
		len(loaded.Repair.AuthorizedKeys) != 1 || len(loaded.Repair.Accounts) != 2 ||
		!loaded.Repair.RecoveryNopasswdSudo {
		t.Fatalf("generated config lost setup choices: %+v", loaded)
	}
}
