package poker

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	opts, err := parseFlags([]string{"-url", "http://host:9099/reassure"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", opts.Interval)
	}
	if opts.Timeout != 15*time.Second {
		t.Errorf("timeout = %s, want 15s", opts.Timeout)
	}
	if opts.Once || opts.Insecure {
		t.Errorf("once=%v insecure=%v, want both false", opts.Once, opts.Insecure)
	}
}

func TestMissingURLIsUsageError(t *testing.T) {
	_, err := parseFlags(nil, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestConfigFileSuppliesURL(t *testing.T) {
	path := writeFile(t, "poke.yaml", "url: http://10.0.0.1:9099/reassure\n")
	opts, err := parseFlags([]string{"-config", path}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.URL != "http://10.0.0.1:9099/reassure" {
		t.Errorf("url = %q", opts.URL)
	}
	if opts.configPath != path {
		t.Errorf("configPath = %q, want %q", opts.configPath, path)
	}
}

func TestExplicitFlagsBeatConfigFile(t *testing.T) {
	path := writeFile(t, "poke.yaml", `
url: http://from-file:9099/reassure
interval: 1m
timeout: 5s
insecure: true
`)
	opts, err := parseFlags([]string{
		"-config", path,
		"-url", "http://from-flag:9099/reassure",
		"-interval", "30s",
		"-insecure=false",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.URL != "http://from-flag:9099/reassure" {
		t.Errorf("url = %q, want the flag to win", opts.URL)
	}
	if opts.Interval != 30*time.Second {
		t.Errorf("interval = %s, want 30s from the flag", opts.Interval)
	}
	// Not given on the command line, so the file's value stands.
	if opts.Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s from the file", opts.Timeout)
	}
	if opts.Insecure {
		t.Error("insecure = true, want the explicit -insecure=false to win")
	}
}

// A flag equal to the default still counts as given, which is what someone
// typing it means.
func TestFlagEqualToDefaultStillOverridesFile(t *testing.T) {
	path := writeFile(t, "poke.yaml", "url: http://host:9099/reassure\ninterval: 1m\n")
	opts, err := parseFlags([]string{"-config", path, "-interval", "5m"}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", opts.Interval)
	}
}

func TestPartialConfigFileKeepsOtherDefaults(t *testing.T) {
	path := writeFile(t, "poke.yaml", "url: http://host:9099/reassure\ninsecure: true\n")
	opts, err := parseFlags([]string{"-config", path}, io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Interval != defaultInterval {
		t.Errorf("interval = %s, want the default to stand", opts.Interval)
	}
	if opts.Timeout != defaultTimeout {
		t.Errorf("timeout = %s, want the default to stand", opts.Timeout)
	}
	if !opts.Insecure {
		t.Error("insecure = false, want the file's true to be used")
	}
}

func TestConfigFileMissing(t *testing.T) {
	_, err := parseFlags([]string{"-config", "/nonexistent/poke.yaml"}, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestConfigFileMalformed(t *testing.T) {
	path := writeFile(t, "poke.yaml", "url: [not, a, string]\n")
	_, err := parseFlags([]string{"-config", path}, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestValidateURL(t *testing.T) {
	valid := []string{
		"http://100.76.187.120:9099/reassure",
		"https://work-machine.example/reassure",
		"http://localhost:9099/reassure",
	}
	for _, raw := range valid {
		if _, err := validateURL(raw); err != nil {
			t.Errorf("validateURL(%q) = %v, want nil", raw, err)
		}
	}

	invalid := []struct {
		raw  string
		want string
	}{
		{"", "http or https"},
		{"reassure", "http or https"},
		{"ftp://host/reassure", "http or https"},
		{"http:///reassure", "no host"},
		{"://nope", "invalid url"},
		{"http://host:notaport/reassure", "invalid port"},
		{"http://user:password@host/reassure", "must not contain credentials"},
	}
	for _, tc := range invalid {
		_, err := validateURL(tc.raw)
		if !errors.Is(err, errUsage) {
			t.Errorf("validateURL(%q) = %v, want errUsage", tc.raw, err)
			continue
		}
		if !contains(err.Error(), tc.want) {
			t.Errorf("validateURL(%q) error = %q, want it to mention %q", tc.raw, err, tc.want)
		}
	}
}

func TestValidateRejectsBadDurations(t *testing.T) {
	base := options{URL: "http://host:9099/reassure", Interval: time.Minute, Timeout: time.Second}
	if err := base.validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for _, mutate := range []func(*options){
		func(o *options) { o.Interval = 0 },
		func(o *options) { o.Interval = -time.Minute },
		func(o *options) { o.Timeout = 0 },
		func(o *options) { o.Timeout = -time.Second },
	} {
		o := base
		mutate(&o)
		if err := o.validate(); !errors.Is(err, errUsage) {
			t.Errorf("validate(%+v) = %v, want errUsage", o, err)
		}
	}
}

func TestUnexpectedArgument(t *testing.T) {
	if _, err := parseFlags([]string{"-url", "http://h/r", "extra"}, io.Discard); !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
