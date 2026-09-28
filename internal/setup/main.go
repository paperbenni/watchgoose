// Package setup installs watchgoose as a listener or poker on this machine.
package setup

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"watchgoose/deploy"
	"watchgoose/internal/pokeconfig"
)

const (
	configDir = "/etc/watchgoose/poke"
	unitPath  = "/etc/systemd/system/watchgoose-poke@.service"
	binPath   = "/usr/local/bin/watchgoose"
)

type urlsFlag []string

func (u *urlsFlag) String() string { return strings.Join(*u, ", ") }
func (u *urlsFlag) Set(s string) error {
	*u = append(*u, s)
	return nil
}

func Run(args []string) error {
	fs := flag.NewFlagSet("watchgoose setup", flag.ContinueOnError)
	var supplied urlsFlag
	var role string
	fs.Var(&supplied, "url", "full reassurance URL; repeat for multiple listeners")
	fs.StringVar(&role, "role", "", "listener or poker; skips role prompt")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if runtime.GOOS != "linux" {
		return errors.New("setup requires Linux")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("systemd is not running")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			return errors.New("sudo is required to install the service")
		}
	}
	if role == "" {
		if err := huh.NewSelect[string]().Title("What will this machine do?").Options(
			huh.NewOption("Listen and repair this machine", "listener"),
			huh.NewOption("Poke other machines", "poker"),
		).Value(&role).Run(); err != nil {
			return err
		}
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if role == "listener" {
		if len(supplied) != 0 {
			return errors.New("-url is only for poker setup")
		}
		if exists(unitPath) {
			return errors.New("this machine already has a poker service; run the listener on a separate machine")
		}
		return setupListener(binary)
	}
	if role != "poker" {
		return fmt.Errorf("role must be listener or poker, got %q", role)
	}
	if exists(listenerUnitPath) {
		return errors.New("this machine already has a listener service; run the poker on a separate machine")
	}
	urls := []string(supplied)
	if len(urls) == 0 {
		var err error
		urls, err = promptURLs()
		if err != nil {
			return err
		}
	}
	configs, err := prepareConfigs(urls)
	if err != nil {
		return err
	}
	return installPoker(binary, urls, configs)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func promptURLs() ([]string, error) {
	fmt.Printf("Enter each server's full reassurance URL, including its path.\n")
	fmt.Printf("Example: http://100.64.0.10:9099/reassure\n")
	fmt.Printf("Pokes run every %s. Leave the next field blank to finish.\n\n", pokeconfig.DefaultInterval)
	var urls []string
	for {
		var answer string
		field := huh.NewInput().
			Title(fmt.Sprintf("Server %d URL", len(urls)+1)).
			Value(&answer).
			Validate(func(s string) error {
				if s == "" && len(urls) > 0 {
					return nil
				}
				if err := pokeconfig.ValidateServerURL(s); err != nil {
					return err
				}
				for _, prior := range urls {
					if prior == s {
						return fmt.Errorf("URL already entered")
					}
				}
				return nil
			})
		if err := field.Run(); err != nil {
			return nil, err
		}
		if answer == "" {
			return urls, nil
		}
		urls = append(urls, answer)
	}
}

// prepareConfigs validates every URL and serializes through the client's own
// config type before any privileged change is made.
func prepareConfigs(urls []string) ([][]byte, error) {
	if len(urls) == 0 {
		return nil, errors.New("at least one server URL is required")
	}
	seen := make(map[string]bool, len(urls))
	configs := make([][]byte, 0, len(urls))
	for _, url := range urls {
		if err := pokeconfig.ValidateServerURL(url); err != nil {
			return nil, err
		}
		if seen[url] {
			return nil, fmt.Errorf("duplicate URL: %s", url)
		}
		seen[url] = true
		data, err := pokeconfig.Marshal(pokeconfig.File{URL: url})
		if err != nil {
			return nil, err
		}
		parsed, err := pokeconfig.Parse(data)
		if err != nil || parsed.URL != url {
			return nil, fmt.Errorf("generated config for %s failed validation", url)
		}
		configs = append(configs, data)
	}
	return configs, nil
}

func root(args ...string) error {
	name := args[0]
	if os.Geteuid() != 0 {
		name = "sudo"
	} else {
		args = args[1:]
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func installFile(source, dest, mode string) error {
	if err := root("install", "-m", mode, "-o", "root", "-g", "root", source, dest+".new"); err != nil {
		return err
	}
	return root("mv", "-f", dest+".new", dest)
}

func installPoker(binary string, urls []string, configs [][]byte) error {
	stage, err := os.MkdirTemp("", "watchgoose-poke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, data := range configs {
		if err := os.WriteFile(filepath.Join(stage, strconv.Itoa(i+1)+".yaml"), data, 0600); err != nil {
			return err
		}
	}
	if err := root("install", "-d", "-m", "0755", "-o", "root", "-g", "root", configDir); err != nil {
		return err
	}
	if _, err := user.Lookup("goosepoke"); err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return err
		}
		nologin, err := exec.LookPath("nologin")
		if err != nil {
			return err
		}
		if err := root("useradd", "--system", "--no-create-home", "--shell", nologin, "goosepoke"); err != nil {
			return err
		}
	}
	if err := installFile(binary, binPath, "0755"); err != nil {
		return err
	}
	unit := filepath.Join(stage, "poke-unit.service")
	if err := os.WriteFile(unit, deploy.PokeUnit, 0600); err != nil {
		return err
	}
	if err := installFile(unit, unitPath, "0644"); err != nil {
		return err
	}
	for i := range configs {
		name := strconv.Itoa(i+1) + ".yaml"
		dest := filepath.Join(configDir, name)
		if err := installFile(filepath.Join(stage, name), dest, "0644"); err != nil {
			return err
		}
	}
	if err := root("systemctl", "daemon-reload"); err != nil {
		return err
	}
	for i := range urls {
		name := fmt.Sprintf("watchgoose-poke@%d.service", i+1)
		if err := root("systemctl", "enable", name); err != nil {
			return err
		}
		if err := root("systemctl", "restart", name); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		base, ok := strings.CutSuffix(entry.Name(), ".yaml")
		if !ok || base == "" || base[0] == '0' {
			continue
		}
		number, err := strconv.Atoi(base)
		if err != nil || number <= len(urls) || strconv.Itoa(number) != base {
			continue
		}
		if err := root("systemctl", "disable", "--now", "watchgoose-poke@"+base+".service"); err != nil {
			return err
		}
		if err := root("rm", "-f", filepath.Join(configDir, entry.Name())); err != nil {
			return err
		}
	}
	if _, err := os.Stat("/etc/systemd/system/goosepoke.service"); err == nil {
		if err := root("systemctl", "disable", "--now", "goosepoke.service"); err != nil {
			return err
		}
	}
	if exists("/etc/systemd/system/goosepoke@.service") {
		legacy, err := os.ReadDir("/etc/goosepoke")
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range legacy {
			base, ok := strings.CutSuffix(entry.Name(), ".yaml")
			if !ok || base == "" || base[0] == '0' {
				continue
			}
			if number, err := strconv.Atoi(base); err != nil || number < 1 || strconv.Itoa(number) != base {
				continue
			}
			if err := root("systemctl", "disable", "--now", "goosepoke@"+base+".service"); err != nil {
				return err
			}
		}
	}
	fmt.Printf("\nPoker installed; each instance pokes every %s.\nBinary: %s\nConfig directory: %s\nUnit: %s\n", pokeconfig.DefaultInterval, binPath, configDir, unitPath)
	for i, url := range urls {
		name := fmt.Sprintf("watchgoose-poke@%d.service", i+1)
		if err := root("systemctl", "is-enabled", "--quiet", name); err != nil {
			return fmt.Errorf("%s is not enabled: %w", name, err)
		}
		if err := root("systemctl", "is-active", "--quiet", name); err != nil {
			return fmt.Errorf("%s is not active: %w", name, err)
		}
		fmt.Printf("  %s  %s\n", name, url)
	}
	fmt.Println("\nLogs: journalctl -u 'watchgoose-poke@*.service' -f")
	return nil
}
