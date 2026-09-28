# justfile for watchgoose — a dead-man's switch for a single shared Linux VM.
#
# Read docs/adr/ before changing anything here. Most of the surprising-looking
# parts of this file are surprising on purpose.
#
#   just            list the recipes
#   just build      cross-compile static binaries into dist/
#   just test-vm    install onto THIS machine
#   just install-client  install the sender on THIS machine, prompting for servers
#
# Recipes that change system state all live behind a script in deploy/; this
# file only decides what to run and with what paths, so that a path is written
# down exactly once.
#
# Recipe parameters are POSITIONAL. `just test-client HOST=x` passes the literal
# string "HOST=x" through; it does not name a parameter.

set shell := ["bash", "-euo", "pipefail", "-c"]

# ---------------------------------------------------------------------------
# Paths. Defined once, exported, and read back by the deploy/ scripts through
# the environment. If a path is wrong, fix it here and nowhere else.
# ---------------------------------------------------------------------------

# The repository root, so recipes work from any subdirectory.
export REPO := justfile_directory()

# Where build output lands. Not tracked by git.
export DIST := REPO / "dist"

# Where the daemon is installed on the target machine. The root disk, never the
# volume: /srv, /etc, /opt and /var/lib are all on the root disk, and /home is
# a bind mount from a separate data volume that intermittently fails to mount.
export WATCHGOOSE_PREFIX := "/srv/watchgoose"

# The single configuration file, root-owned. Public keys only, so 0644.
export WATCHGOOSE_CONFIG := "/etc/watchgoose.yaml"

# The systemd unit. The daemon deliberately has no timer; see ADR 0005.
export WATCHGOOSE_UNIT := "/etc/systemd/system/watchgoose.service"

# Source assets, read by the install scripts.
export WATCHGOOSE_EXAMPLE_CONFIG := REPO / "deploy" / "watchgoose.example.yaml"
export WATCHGOOSE_UNIT_SRC := REPO / "deploy" / "watchgoose.service"
export GOOSEPOKE_UNIT := REPO / "deploy" / "goosepoke.service"
export GOOSEPOKE_INSTANCE_UNIT := REPO / "deploy" / "goosepoke@.service"

# The client lives in cmd/goosepoke and builds to a binary called `goosepoke`.
# "Poke" on its own is the verb, and a command by that name collides with other
# software; the program is named after the goose, the action is not.
export GOOSEPOKE_PKG := "./cmd/goosepoke"

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Cross-compile the static binaries into dist/.
build:
    @mkdir -p "{{DIST}}"
    # CGO_ENABLED=0 is a hard requirement, not a preference. The target machine
    # must run these with nothing installed and no matching glibc; a
    # dynamically linked binary there is a binary that silently does not run.
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "{{DIST}}/watchgoose" ./cmd/watchgoose
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "{{DIST}}/goosepoke" {{GOOSEPOKE_PKG}}
    # arm64 is for the Raspberry Pi that runs the client, not the daemon.
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o "{{DIST}}/goosepoke-linux-arm64" {{GOOSEPOKE_PKG}}
    @printf '\nbuilt (linux/amd64 watchgoose + goosepoke, linux/arm64 goosepoke):\n\n'
    ls -lh "{{DIST}}"
    @printf '\nconfirm static linkage — every line should say "statically linked":\n'
    file "{{DIST}}"/* 2>/dev/null || true

# Build, then name the daemon binary the VM install will use.
watchgoose: build
    # A dependency rather than a step, so that test-vm and friends cannot run
    # against a stale or missing binary.
    @printf '\ndaemon binary for this machine: %s\n' "{{DIST}}/watchgoose"

# ---------------------------------------------------------------------------
# Checks
# ---------------------------------------------------------------------------

# gofmt (must print nothing), go vet, go test.
check:
    #!/usr/bin/env bash
    # A shebang recipe rather than one command per line, because just passes
    # `$$` through untouched and this needs real shell variables. just also
    # requires every line of a shebang recipe to share one indent level, hence
    # the flat `if` body below.
    set -euo pipefail
    unformatted="$(gofmt -l .)"
    if [ -n "${unformatted}" ]; then
    printf 'gofmt: these files need formatting:\n%s\n' "${unformatted}" >&2
    printf 'fix with: gofmt -w .\n' >&2
    exit 1
    fi
    printf 'gofmt: clean\n'
    go vet ./...
    go test ./...

# Validate the INSTALLED config with the daemon's own -check flag.
check-config:
    # The real binary, from the real installed path: a config the daemon would
    # reject at startup cannot pass here.
    "{{WATCHGOOSE_PREFIX}}/watchgoose" -config "{{WATCHGOOSE_CONFIG}}" -check
    @printf '\nconfig accepted by the daemon: %s\n' "{{WATCHGOOSE_CONFIG}}"

# ---------------------------------------------------------------------------
# Deploy to this machine
# ---------------------------------------------------------------------------

# Install and start watchgoose on THIS machine.
test-vm: watchgoose
    deploy/install-vm.sh
    @printf '\n'
    @printf 'Send a reassurance poke from another machine to activate this fresh install.\n'
    @printf 'Until the first successful poke, repair and reboot are held. Afterward,\n'
    @printf 'the normal deadline and uptime floor apply across service and VM restarts. The exact\n'
    @printf 'command was printed above and is in the install summary; the log is at\n'
    @printf '  %s\n' "{{WATCHGOOSE_PREFIX}}/var/watchgoose.log"

# Constrain earlyoom away from sshd and the daemon (ADR 0007).
earlyoom:
    # The script does NOT restart earlyoom: it prints the command and leaves
    # the decision to you, because the new list is inert until then.
    deploy/harden-earlyoom.sh

# Remove the daemon from this machine.
uninstall-vm:
    # Keeps /etc/watchgoose.yaml and the state directory unless you pass
    # PURGE=1, because a re-install should not need re-keying.
    deploy/uninstall-vm.sh

# ---------------------------------------------------------------------------
# Deploy the client to a separate, more reliable host
# ---------------------------------------------------------------------------

# On the sender, build and install the client; prompt for full server URLs.
install-client:
    @mkdir -p "{{DIST}}"
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o "{{DIST}}/goosepoke-local" {{GOOSEPOKE_PKG}}
    deploy/install-client.sh

# Confirm HOST was given. Without it, this does nothing at all, on purpose.
_require_host HOST:
    @if [ -z "{{HOST}}" ]; then printf 'test-client: usage: just test-client HOST url [interval] [timeout] [arch]\n  e.g. just test-client user@client-host http://VM_TAILNET_IP:9099/reassure 5m 15s arm64\n' >&2; exit 2; fi
    @command -v ssh >/dev/null 2>&1 || { printf 'test-client: ssh not found\n' >&2; exit 1; }
    @command -v scp >/dev/null 2>&1 || { printf 'test-client: scp not found\n' >&2; exit 1; }

# Confirm the daemon URL was given. There is no sensible default: the client
# has to be pointed at one specific machine, and guessing is how you end up
# reassuring the wrong box. It must be the FULL url, path included — the client
# POSTs to exactly what it is given and appends nothing.
_require_client_url url:
    @if [ -z "{{url}}" ]; then printf 'test-client: set the FULL URL of the daemon endpoint, path included.\n  e.g. just test-client user@client-host http://VM_TAILNET_IP:9099/reassure 5m 15s arm64\n' >&2; exit 2; fi
    @case "{{url}}" in http://*|https://*) ;; *) printf 'test-client: url must start with http:// or https://, got: %s\n' "{{url}}" >&2; exit 2 ;; esac

# Build, install and start the client on HOST over SSH. The client runs as an
# unprivileged system user and pokes the daemon; it never logs in as anything.
test-client HOST url interval="5m" timeout="15s" arch="amd64": (_require_host HOST) (_require_client_url url)
    # Parameters are positional, so this is:
    #   just test-client HOST url [interval] [timeout] [arch]
    CGO_ENABLED=0 GOOS=linux GOARCH="{{arch}}" go build -trimpath -ldflags "-s -w" -o "{{DIST}}/goosepoke" {{GOOSEPOKE_PKG}}
    ssh "{{HOST}}" 'sudo useradd --system --no-create-home --shell /usr/sbin/nologin goosepoke 2>/dev/null || true'
    scp -q "{{DIST}}/goosepoke" "{{HOST}}":/tmp/goosepoke
    ssh "{{HOST}}" 'sudo install -m 0755 -o root -g root /tmp/goosepoke /usr/local/bin/goosepoke && rm -f /tmp/goosepoke'
    # Render the unit template: the placeholders in deploy/goosepoke.service are
    # the only thing that differs between machines.
    sed -e "s|http://WATCHGOOSE_HOST:9099/reassure|{{url}}|" -e "s|-interval 5m|-interval {{interval}}|" -e "s|-timeout 15s|-timeout {{timeout}}|" "{{GOOSEPOKE_UNIT}}" | ssh "{{HOST}}" 'sudo tee /etc/systemd/system/goosepoke.service >/dev/null && sudo chmod 0644 /etc/systemd/system/goosepoke.service'
    ssh "{{HOST}}" 'sudo systemctl daemon-reload && sudo systemctl enable --now goosepoke'
    @printf '\ninstalled goosepoke on %s\n' "{{HOST}}"
    @printf 'watch it:        ssh %s "journalctl -u goosepoke -f"\n' "{{HOST}}"
    @printf 'poke once, now:  ssh %s "sudo -u goosepoke /usr/local/bin/goosepoke -url {{url}} -once"\n' "{{HOST}}"
    ssh "{{HOST}}" 'systemctl --no-pager --full status goosepoke || true'

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

# List the recipes.
default: help

# List the recipes and what they do.
help:
    @just --list
