package setup

import (
	"strings"
	"testing"

	"watchgoose/internal/pokeconfig"
)

func TestPrepareConfigsPreservesURLs(t *testing.T) {
	urls := []string{
		"http://100.64.0.10:9099/reassure",
		"https://server.example/reassure?source=a&server=b",
	}
	configs, err := prepareConfigs(urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != len(urls) {
		t.Fatalf("got %d configs, want %d", len(configs), len(urls))
	}
	for i, data := range configs {
		cfg, err := pokeconfig.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.URL != urls[i] {
			t.Errorf("config %d URL = %q, want %q", i, cfg.URL, urls[i])
		}
	}
}

func TestPrepareConfigsRejectsBadServerList(t *testing.T) {
	for _, tc := range []struct {
		name string
		urls []string
		want string
	}{
		{"empty", nil, "at least one"},
		{"missing path", []string{"http://server.example"}, "path"},
		{"duplicate", []string{"http://server.example/reassure", "http://server.example/reassure"}, "duplicate"},
		{"credentials", []string{"http://user:secret@server.example/reassure"}, "credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prepareConfigs(tc.urls)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
