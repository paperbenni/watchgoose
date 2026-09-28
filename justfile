# justfile for watchgoose — a dead-man's switch for a single shared Linux VM.
#
# Read docs/adr/ before changing anything here. Most of the surprising-looking
# parts of this file are surprising on purpose.
#
#   just            list the recipes
#   just build      cross-compile static binaries into dist/
#   just setup      choose this machine's role and install it
#
# Recipes that change system state delegate to an installer; this file handles
# builds and passes the installer its source paths.
#
set shell := ["bash", "-euo", "pipefail", "-c"]

# ---------------------------------------------------------------------------
# Paths used by maintenance recipes and the legacy VM uninstaller.
# ---------------------------------------------------------------------------

# The repository root, so recipes work from any subdirectory.
export REPO := justfile_directory()

# Where build output lands. Not tracked by git.
export DIST := REPO / "dist"

# Where the daemon is installed on the target machine. The root disk, never the
# volume: /srv, /etc, /opt and /var/lib are all on the root disk, and /home is
# a bind mount from a separate data volume that intermittently fails to mount.
export WATCHGOOSE_PREFIX := "/srv/watchgoose"

# The listener configuration file, root-owned. Public keys only, so 0644.
export WATCHGOOSE_CONFIG := "/etc/watchgoose.yaml"

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Cross-compile the static binary into dist/ for both target architectures.
build:
    @mkdir -p "{{DIST}}"
    # CGO_ENABLED=0 is a hard requirement, not a preference. The target machine
    # must run these with nothing installed and no matching glibc; a
    # dynamically linked binary there is a binary that silently does not run.
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "{{DIST}}/watchgoose" ./cmd/watchgoose
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o "{{DIST}}/watchgoose-linux-arm64" ./cmd/watchgoose
    @printf '\nbuilt (linux/amd64 and linux/arm64 watchgoose):\n\n'
    ls -lh "{{DIST}}/watchgoose" "{{DIST}}/watchgoose-linux-arm64"
    @printf '\nconfirm static linkage — every line should say "statically linked":\n'
    file "{{DIST}}/watchgoose" "{{DIST}}/watchgoose-linux-arm64" 2>/dev/null || true

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
    "{{WATCHGOOSE_PREFIX}}/watchgoose" listen -config "{{WATCHGOOSE_CONFIG}}" -check
    @printf '\nconfig accepted by the daemon: %s\n' "{{WATCHGOOSE_CONFIG}}"

# ---------------------------------------------------------------------------
# Deploy to this machine
# ---------------------------------------------------------------------------

# Build the native binary, then choose listener or poker in the Huh wizard.
setup:
    @mkdir -p "{{DIST}}"
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o "{{DIST}}/watchgoose-local" ./cmd/watchgoose
    "{{DIST}}/watchgoose-local" setup

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
# Help
# ---------------------------------------------------------------------------

# List the recipes.
default: help

# List the recipes and what they do.
help:
    @just --list
