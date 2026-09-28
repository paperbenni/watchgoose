package pokeconfig

import (
	"fmt"
	"net/url"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultInterval = 5 * time.Minute
	DefaultTimeout  = 15 * time.Second
)

// File is the on-disk poker configuration shared by the runtime and setup.
type File struct {
	URL      string         `yaml:"url"`
	Interval *time.Duration `yaml:"interval"`
	Timeout  *time.Duration `yaml:"timeout"`
	Insecure *bool          `yaml:"insecure"`
}

func Parse(raw []byte) (File, error) {
	var cfg File
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func Marshal(cfg File) ([]byte, error) { return yaml.Marshal(cfg) }

// ValidateURL applies the same URL rules to prompts and the running client.
func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url %q must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url %q has no host", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("url must not contain credentials")
	}
	return u, nil
}

func ValidateServerURL(raw string) error {
	u, err := ValidateURL(raw)
	if err != nil {
		return err
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("url %q needs the full reassurance path", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("url %q must not contain a fragment", raw)
	}
	return nil
}
