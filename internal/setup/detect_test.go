package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTailscaleIPv4(t *testing.T) {
	if got := parseTailscaleIPv4("100.76.187.120\n"); got != "100.76.187.120" {
		t.Fatalf("got %q", got)
	}
	for _, output := range []string{"", "not-an-ip", "fd7a::1", "100.76.187.120\n100.76.187.121"} {
		if got := parseTailscaleIPv4(output); got != "" {
			t.Errorf("parseTailscaleIPv4(%q) = %q, want empty", output, got)
		}
	}
}

func TestPublicLoginKeysSkipsOptionsAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	data := "# comment\nssh-ed25519 YWJj user@laptop\nfrom=\"host\" ssh-ed25519 YWJj restricted\n\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh-ed25519 YWJj user@laptop"}
	if got := publicLoginKeys(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %#v, want %#v", got, want)
	}
}
