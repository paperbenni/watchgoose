package setup

import (
	"context"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"watchgoose/internal/config"
	"watchgoose/internal/mountinfo"
)

// detectedListenerDefaults are only editable starting values for the wizard.
// Nothing detected here is written without the user's review.
func detectedListenerDefaults() config.Config {
	cfg := config.Default()
	cfg.Server.Listen = ""
	if ip := localTailscaleIPv4(); ip != "" {
		cfg.Server.Listen = net.JoinHostPort(ip, "9099")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		cfg.Repair.AuthorizedKeys = publicLoginKeys(filepath.Join(home, ".ssh", "authorized_keys"))
	}
	cfg.Volume.Mountpoint = homeMountpoint(home)
	return cfg
}

func localTailscaleIPv4() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return ""
	}
	return parseTailscaleIPv4(string(out))
}

func parseTailscaleIPv4(output string) string {
	fields := strings.Fields(output)
	if len(fields) != 1 {
		return ""
	}
	ip := net.ParseIP(fields[0]).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

// An authorized_keys entry is a useful starting point because its private key
// is held by someone who can already log in to this machine. Options and
// comments are skipped; setup needs plain public key lines for recovery.
func publicLoginKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") &&
			!strings.HasPrefix(fields[0], "ecdsa-") && !strings.HasPrefix(fields[0], "sk-") {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
			if _, err := base64.RawStdEncoding.DecodeString(fields[1]); err != nil {
				continue
			}
		}
		keys = append(keys, line)
	}
	return keys
}

func homeMountpoint(home string) string {
	if mounted, err := mountinfo.IsMountpoint("/home"); err == nil && mounted {
		return "/home"
	}
	if home != "" && filepath.IsAbs(home) && home != "/" {
		if mounted, err := mountinfo.IsMountpoint(home); err == nil && mounted {
			return home
		}
	}
	return ""
}
