package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultInterval = 5 * time.Minute
	defaultTimeout  = 15 * time.Second
)

// errUsage marks a configuration problem. It is the only class of failure
// that ends the process immediately, because every other failure is transient
// and the loop must survive it.
var errUsage = errors.New("usage")

func usageErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
}

func usage(w io.Writer) {
	fs := newFlagSet()
	fs.SetOutput(w)
	fmt.Fprintf(w, `goosepoke sends reassurance to a watchgoose daemon.

The payload is the arrival itself: no credential, no token, no timestamp and
no signature are sent. Every failure to send is logged loudly, because a poke
client that has stopped poking looks, from the machine, exactly like a machine
nobody can reach.

Usage:
  goosepoke -url http://100.76.187.120:9099/reassure
  goosepoke -once -url https://work-machine.example/reassure

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprintf(w, `
The config file uses the same names as the flags: url, interval, timeout,
insecure. Explicit flags win over the file.
`)
}

// fileConfig is the optional on-disk configuration. It is deliberately a
// different schema from internal/config: that one is the daemon's, and the
// only keys that make sense to the client are the four below.
type fileConfig struct {
	URL      string         `yaml:"url"`
	Interval *time.Duration `yaml:"interval"`
	Timeout  *time.Duration `yaml:"timeout"`
	Insecure *bool          `yaml:"insecure"`
}

// options is the fully resolved configuration for one run.
type options struct {
	URL      string
	Interval time.Duration
	Timeout  time.Duration
	Insecure bool
	Once     bool

	// configPath is kept for logging only; it is not part of the file schema.
	configPath string
}

func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("goosepoke", flag.ContinueOnError)
	fs.String("url", "", "full URL to POST to, e.g. http://100.76.187.120:9099/reassure (required unless -config is given)")
	fs.Duration("interval", defaultInterval, "how often to poke")
	fs.Duration("timeout", defaultTimeout, "per-request timeout for a single poke")
	fs.Bool("once", false, "send a single poke and exit; non-zero status on failure")
	fs.Bool("insecure", false, "skip TLS verification; only for a self-signed endpoint")
	fs.String("config", "", "optional YAML file whose keys match the flags (url, interval, timeout, insecure)")
	return fs
}

// parseFlags resolves argv into options, layering a config file underneath the
// flags that were actually given. Explicit flags always win: the file supplies
// defaults, it does not override the command line.
func parseFlags(argv []string, stderr io.Writer) (options, error) {
	fs := newFlagSet()
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	if err := fs.Parse(argv); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, usageErrorf("unexpected argument %q", fs.Arg(0))
	}

	opts := options{Interval: defaultInterval, Timeout: defaultTimeout}

	if path := fs.Lookup("config").Value.String(); path != "" {
		cfg, err := loadFile(path)
		if err != nil {
			return options{}, err
		}
		opts.configPath = path
		applyFile(&opts, cfg)
	}

	// Only flags actually present on the command line may override the file.
	// Note that -interval=5m is identical to the default but still counts as
	// given, which is what someone typing it means.
	given := make(map[string]bool, 5)
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	flags := map[string]func(){
		"url":      func() { opts.URL = fs.Lookup("url").Value.String() },
		"interval": func() { opts.Interval = durationOf(fs.Lookup("interval")) },
		"timeout":  func() { opts.Timeout = durationOf(fs.Lookup("timeout")) },
		"insecure": func() { opts.Insecure = boolOf(fs.Lookup("insecure")) },
		"once":     func() { opts.Once = boolOf(fs.Lookup("once")) },
	}
	for name, set := range flags {
		if given[name] {
			set()
		}
	}

	if err := opts.validate(); err != nil {
		return options{}, err
	}
	return opts, nil
}

func durationOf(f *flag.Flag) time.Duration {
	d, _ := time.ParseDuration(f.Value.String())
	return d
}

func boolOf(f *flag.Flag) bool {
	b, _ := strconv.ParseBool(f.Value.String())
	return b
}

// applyFile layers file values over what has already been resolved. Absent
// keys are left alone, so a partial file only overrides what it names.
func applyFile(o *options, cfg fileConfig) {
	if cfg.URL != "" {
		o.URL = cfg.URL
	}
	if cfg.Interval != nil {
		o.Interval = *cfg.Interval
	}
	if cfg.Timeout != nil {
		o.Timeout = *cfg.Timeout
	}
	if cfg.Insecure != nil {
		o.Insecure = *cfg.Insecure
	}
}

func loadFile(path string) (fileConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, usageErrorf("read config: %v", err)
	}
	var cfg fileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fileConfig{}, usageErrorf("parse config %s: %v", path, err)
	}
	return cfg, nil
}

// validate rejects a configuration that could not work, so a typo surfaces at
// startup rather than as a machine quietly rebooting itself later on.
func (o options) validate() error {
	if o.URL == "" {
		return usageErrorf("no url: pass -url, or put url in the -config file")
	}
	if _, err := validateURL(o.URL); err != nil {
		return err
	}
	if o.Interval <= 0 {
		return usageErrorf("interval must be positive, got %s", o.Interval)
	}
	if o.Timeout <= 0 {
		return usageErrorf("timeout must be positive, got %s", o.Timeout)
	}
	return nil
}

// validateURL checks that the target is something a poke can actually reach.
func validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, usageErrorf("invalid url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, usageErrorf("url %q must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return nil, usageErrorf("url %q has no host", raw)
	}
	if u.User != nil {
		return nil, usageErrorf("url must not contain credentials")
	}
	return u, nil
}

func configLabel(path string) string {
	if path == "" {
		return "(none)"
	}
	return path
}
