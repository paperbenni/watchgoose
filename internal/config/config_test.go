package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadText(t *testing.T, content string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watchgoose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	_, err := loadText(t, "serrver:\n  listen: 100.76.187.120:9099\nrepair:\n  authorized_keys: [ssh-ed25519-foo]\n")
	if err == nil || !strings.Contains(err.Error(), "serrver") {
		t.Fatalf("unknown server section passed configuration check: %v", err)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	_, err := loadText(t, "repair:\n  authorized_keys: [ssh-ed25519-foo]\n---\nrepair:\n  authorized_keys: []\n")
	if err == nil || !strings.Contains(err.Error(), "one YAML document") {
		t.Fatalf("second YAML document was accepted: %v", err)
	}
}

func TestValidateRejectsUnusableRecoveryAndRoute(t *testing.T) {
	base := Default()
	base.Repair.AuthorizedKeys = []string{"ssh-ed25519 AAAA test"}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"no recovery key", func(c *Config) { c.Repair.AuthorizedKeys = nil }},
		{"route collision", func(c *Config) { c.Reassurance.Path = "/health" }},
		{"relative recovery home", func(c *Config) { c.Repair.RecoveryHome = "recovery" }},
		{"sudoers injection", func(c *Config) { c.Repair.RecoveryUser = "recovery\nroot" }},
		{"volume path traversal", func(c *Config) {
			c.Volume.Mountpoint = "/home"
			c.Repair.RecoveryHome = "/home/../home/recovery"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}
