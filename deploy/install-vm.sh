#!/usr/bin/env bash
#
# install-vm.sh — install the watchgoose dead-man's switch onto this machine.
#
# Idempotent. Safe to re-run: a second run replaces the binary and the unit,
# leaves an existing configuration alone, and changes nothing else.
#
#   deploy/install-vm.sh                  install or update, preserving config
#
# Deliberately NOT done here:
#   * the recovery user is created by the DAEMON only when repair runs. The
#     installer does not provision a standing passwordless-sudo account.
#   * the simplevm metadata timers are left exactly as they are. homes-sync.timer
#     has a known lockout bug and the switch deliberately self-heals around it
#     rather than switching it off (docs/adr/0003).
#   * /etc/fstab is not read, written or repaired. The volume that /home lives on
#     fails to mount sometimes, and a reboot is the remedy for that
#     (docs/adr/0004). "Fixing" fstab from here fixes the wrong thing.
#
# Paths default to the values exported by the justfile, and can be overridden
# with the same environment variables.

set -euo pipefail

SERVICE_NAME="watchgoose"
PREFIX="${WATCHGOOSE_PREFIX:-/srv/watchgoose}"
CONFIG_PATH="${WATCHGOOSE_CONFIG:-/etc/watchgoose.yaml}"
UNIT_PATH="${WATCHGOOSE_UNIT:-/etc/systemd/system/watchgoose.service}"

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(CDPATH='' cd -- "${SCRIPT_DIR}/.." && pwd -P)"
EXAMPLE_CONFIG="${WATCHGOOSE_EXAMPLE_CONFIG:-${REPO_ROOT}/deploy/watchgoose.example.yaml}"
UNIT_SRC="${WATCHGOOSE_UNIT_SRC:-${SCRIPT_DIR}/watchgoose.service}"

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_WARN=$'\033[1;33m'
    C_ERR=$'\033[1;31m'
    C_OK=$'\033[1;32m'
else
    C_RESET=""
    C_BOLD=""
    C_WARN=""
    C_ERR=""
    C_OK=""
fi

say()  { printf '\n%s==> %s%s\n' "${C_BOLD}" "$*" "${C_RESET}"; }
info() { printf '    %s\n' "$*"; }
ok()   { printf '    %s%s%s\n' "${C_OK}" "$*" "${C_RESET}"; }
warn() { printf '\n%swarning: %s%s\n' "${C_WARN}" "$*" "${C_RESET}" >&2; }
die()  { printf '\n%serror: %s%s\n' "${C_ERR}" "$*" "${C_RESET}" >&2; exit 1; }

CHANGES=()
changed() { CHANGES+=("$1"); }

# config_value SECTION KEY FILE
#
# Print the scalar for KEY inside the top-level SECTION of a YAML file, unquoted,
# or nothing if it is absent. This is a dozen lines of awk, NOT a YAML parser,
# and it decides nothing: it exists so that the poke command printed at the end
# of the install is the command that actually works on this machine, rather than
# one hard-coded from the example config. If it ever returns something wrong, the
# only consequence is a wrong line in a message, and the message also names the
# config file the value came from.
config_value() {
    awk -v section="$1" -v key="$2" '
        /^[[:space:]]*#/ { next }
        /^[^[:space:]]/ {
            current = $0
            sub(/:.*/, "", current)
            next
        }
        current == section {
            line = $0
            if (line !~ "^[[:space:]]+" key "[[:space:]]*" ":") { next }
            sub("^[[:space:]]+" key "[[:space:]]*" ":[[:space:]]*", "", line)
            sub(/[[:space:]]*(#.*)?$/, "", line)
            gsub(/^\042/, "", line); gsub(/\042$/, "", line)
            gsub(/^\047/, "", line); gsub(/\047$/, "", line)
            print line
            exit
        }
    ' "$3"
}

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "must run as root: this writes ${PREFIX}, ${CONFIG_PATH} and ${UNIT_PATH}"
command -v systemctl >/dev/null 2>&1 || die "systemctl not found; this is not a systemd machine"
[ -d /run/systemd/system ] || die "/run/systemd/system is missing, so systemd is not PID 1; refusing to install a service nothing will start"
[ -f "${EXAMPLE_CONFIG}" ] || die "example config not found at ${EXAMPLE_CONFIG} (set WATCHGOOSE_EXAMPLE_CONFIG)"
[ -f "${UNIT_SRC}" ] || die "unit file not found at ${UNIT_SRC} (set WATCHGOOSE_UNIT_SRC)"
[ -d "${REPO_ROOT}/cmd/watchgoose" ] || die "cmd/watchgoose not found under ${REPO_ROOT}; this script expects to be run from the repository"

# ---------------------------------------------------------------------------
# Find the binary to install
# ---------------------------------------------------------------------------

resolve_binary() {
    local candidate arch
    if [ -n "${WATCHGOOSE_BIN:-}" ]; then
        [ -f "${WATCHGOOSE_BIN}" ] || die "WATCHGOOSE_BIN=${WATCHGOOSE_BIN} does not exist"
        printf '%s\n' "${WATCHGOOSE_BIN}"
        return 0
    fi
    # Only dist/, which is what `just build` produces with CGO_ENABLED=0. A bare
    # `go build` leaves a dynamically linked ./watchgoose in the repository root,
    # and that binary does not run on the target machine, so it is not a
    # candidate. If you built one deliberately, name it:
    #   WATCHGOOSE_BIN=./watchgoose deploy/install-vm.sh
    for candidate in \
        "${REPO_ROOT}/dist/watchgoose" \
        "${PREFIX}/watchgoose"
    do
        if [ -f "${candidate}" ]; then
            printf '%s\n' "${candidate}"
            return 0
        fi
    done
    if command -v go >/dev/null 2>&1; then
        arch="${WATCHGOOSE_ARCH:-$(go env GOARCH)}"
        # Everything in this function goes to stderr: the path is read back
        # through a command substitution, which captures stdout and nothing else.
        # A progress message on stdout would arrive as part of the path.
        say "No prebuilt binary found; building one for linux/${arch}" >&2
        info "source: ${REPO_ROOT}" >&2
        # CGO_ENABLED=0 for the same reason as the justfile: the target machine
        # must be able to run this with nothing installed.
        ( cd "${REPO_ROOT}" && CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -ldflags "-s -w" -o "${REPO_ROOT}/dist/watchgoose" ./cmd/watchgoose ) >&2
        printf '%s\n' "${REPO_ROOT}/dist/watchgoose"
        return 0
    fi
    die "no watchgoose binary found (looked in ${REPO_ROOT}/dist and ${PREFIX}) and go is not installed to build one; run 'just build' first"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

say "Installing watchgoose on $(hostname)"

BIN_SRC="$(resolve_binary)"
BIN_DST="${PREFIX}/watchgoose"
VAR_DIR="${PREFIX}/var"

info "binary:   ${BIN_SRC}"
info "install:  ${PREFIX}"
info "config:   ${CONFIG_PATH}"
info "unit:     ${UNIT_PATH}"

# 1. Directories -------------------------------------------------------------
#
# /srv is on the root disk. /home is not: it is a bind mount from a volume that
# intermittently fails to mount. Everything the switch needs while the volume is
# absent lives under /srv, on the root disk, or it does not exist at all.
say "Directories"
install -d -o root -g root -m 0750 "${PREFIX}" "${VAR_DIR}"
ok "${PREFIX}        root:root 0750"
ok "${VAR_DIR}  root:root 0750"

# 2. Binary ------------------------------------------------------------------
say "Binary"
if [ -e "${BIN_DST}" ]; then
    # Removed before being replaced. The running process keeps its own inode and
    # is unaffected; writing over the file in place would fail with ETXTBSY
    # anyway. The new file is in place before the service is restarted below.
    rm -f "${BIN_DST}"
    info "removed the previous binary; the running daemon is unaffected"
fi
install -m 0755 -o root -g root "${BIN_SRC}" "${BIN_DST}"
changed "binary       ${BIN_DST}"
ok "installed ${BIN_DST} ($(stat -c %s "${BIN_DST}") bytes, $(stat -c %A "${BIN_DST}"), $(stat -c %U:%G "${BIN_DST}"))"

# 3. Config ------------------------------------------------------------------
say "Config"
if [ -e "${CONFIG_PATH}" ]; then
    info "PRESERVED the existing ${CONFIG_PATH}; it has not been touched."
    info "Edit it directly to change the machine address, accounts or keys."
else
    install -m 0644 -o root -g root "${EXAMPLE_CONFIG}" "${CONFIG_PATH}"
    changed "config       ${CONFIG_PATH}  (created from the example)"
    ok "installed ${CONFIG_PATH} (root:root 0644 — public keys only, no secrets)"
fi

# Validate before enabling a switch that may act immediately on an old VM.
# A rejected config must never be installed as a restarting service.
say "Validating the installed config before starting the service"
if ! CHECK_OUTPUT="$("${BIN_DST}" -config "${CONFIG_PATH}" -check 2>&1)"; then
    printf '%s\n' "${CHECK_OUTPUT}" | sed 's/^/      /'
    die "edit ${CONFIG_PATH} with this VM's listen address and your public key, then rerun the install; service was not started"
fi
ok "the daemon accepts ${CONFIG_PATH}"

# A fresh install waits for a real poke before it can repair or reboot. Do not
# recreate the marker on an existing installation whose state file was lost:
# that machine has already been armed and must continue to fail safe.
if [ ! -e "${UNIT_PATH}" ] && [ "$(systemctl show --property=LoadState --value "${SERVICE_NAME}" 2>/dev/null)" = "not-found" ]; then
    say "Initializing first-poke state"
    "${BIN_DST}" -config "${CONFIG_PATH}" -initialize-first-poke
fi

# 4. Unit and service --------------------------------------------------------
say "systemd unit"
install -m 0644 -o root -g root "${UNIT_SRC}" "${UNIT_PATH}"
changed "unit          ${UNIT_PATH}"
ok "installed ${UNIT_PATH}"

say "Starting the service"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" >/dev/null
systemctl restart "${SERVICE_NAME}"
info "daemon-reload done, unit enabled, service restarted"

# 5. Verification ------------------------------------------------------------
say "Verification"
CHECK_OK=1

if [ -x "${BIN_DST}" ]; then
    if CHECK_OUTPUT="$("${BIN_DST}" -config "${CONFIG_PATH}" -check 2>&1)"; then
        ok "the daemon accepts ${CONFIG_PATH}"
        [ -z "${CHECK_OUTPUT}" ] || printf '%s\n' "${CHECK_OUTPUT}" | sed 's/^/      /'
    else
        CHECK_OK=0
        warn "the daemon REJECTED ${CONFIG_PATH}:"
        printf '%s\n' "${CHECK_OUTPUT}" | sed 's/^/      /'
        warn "the service is installed and enabled, but it will crash-loop until"
        warn "this is fixed. Reproduce it with:"
        warn "  ${BIN_DST} -config ${CONFIG_PATH} -check"
    fi
else
    CHECK_OK=0
    warn "could not execute ${BIN_DST} to validate the config"
fi

if systemctl is-active --quiet "${SERVICE_NAME}"; then
    ok "service ${SERVICE_NAME} is active"
else
    CHECK_OK=0
    warn "service ${SERVICE_NAME} is NOT active. Diagnostics follow."
    systemctl --no-pager --full status "${SERVICE_NAME}" 2>&1 | sed 's/^/      /' || true
fi

if systemctl is-enabled --quiet "${SERVICE_NAME}"; then
    ok "service ${SERVICE_NAME} is enabled at boot"
else
    CHECK_OK=0
    warn "service ${SERVICE_NAME} is NOT enabled at boot"
fi

# 6. Summary -----------------------------------------------------------------
say "What this run changed"
if [ "${#CHANGES[@]}" -gt 0 ]; then
    printf '    %s\n' "${CHANGES[@]}"
else
    printf '    nothing substantive; the config was preserved and the binary and\n'
    printf '    unit were rewritten identically\n'
fi

LISTEN="$(config_value server listen "${CONFIG_PATH}" || true)"
PATH_ROUTE="$(config_value reassurance path "${CONFIG_PATH}" || true)"
RECOVERY_USER="$(config_value repair recovery_user "${CONFIG_PATH}" || true)"
RECOVERY_HOME="$(config_value repair recovery_home "${CONFIG_PATH}" || true)"
LOG_FILE="$(config_value log log_file "${CONFIG_PATH}" || true)"
STATE_FILE="$(config_value server state_file "${CONFIG_PATH}" || true)"

# Fall back to the built-in defaults, which are also in internal/config.
LISTEN="${LISTEN:-127.0.0.1:9099}"
PATH_ROUTE="${PATH_ROUTE:-/reassure}"
RECOVERY_USER="${RECOVERY_USER:-recovery}"
RECOVERY_HOME="${RECOVERY_HOME:-/srv/recovery}"
LOG_FILE="${LOG_FILE:-${VAR_DIR}/watchgoose.log}"
STATE_FILE="${STATE_FILE:-${VAR_DIR}/last-reassurance}"

case "${PATH_ROUTE}" in
    /*) ;;
    *)  warn "reassurance.path is '${PATH_ROUTE}', which is not absolute; the"
        warn "command below is a guess. The real value is in ${CONFIG_PATH}."
        ;;
esac

say "Where to look"
info "log file:   ${LOG_FILE}   (log.log_file in the config)"
info "journal:    journalctl -u ${SERVICE_NAME} -f"
info "state file: ${STATE_FILE}   (server.state_file — time of last reassurance)"
info "config:     ${CONFIG_PATH}"
info "unit:       ${UNIT_PATH}"
info "binary:     ${BIN_DST}"
info "repository: ${REPO_ROOT}"

say "Send a reassurance poke by hand, now"
case "${LISTEN}" in
    0.0.0.0|0.0.0.0:*)
        PORT="${LISTEN##*:}"
        [ "${PORT}" = "${LISTEN}" ] && PORT="9099"
        warn "server.listen is the wildcard ${LISTEN}, which is not an address"
        warn "anything can connect to. Use this machine's tailnet address:"
        CURL_TARGET="http://<the-machine's-address>:${PORT}"
        ;;
    *)
        CURL_TARGET="http://${LISTEN}"
        ;;
esac

info "The body is ignored; the arrival is the whole signal (ADR 0006). Send it"
info "from the machine that runs goosepoke, or from a laptop on the tailnet:"
printf '\n    curl -fsS -X POST -m 10 "%s%s"\n\n' "${CURL_TARGET}" "${PATH_ROUTE}"
info "An empty 2xx is the expected result. No response means the daemon is not"
info "listening on ${LISTEN}: check 'systemctl status ${SERVICE_NAME}' and the log."

say "The recovery user"
info "The installer does not create ${RECOVERY_USER}. Repair creates it only when"
info "the switch fires, at ${RECOVERY_HOME}, with the configured keys and sudo grant."

say "Then install the client somewhere else"
info "NOT on this machine: a client that stops poking when the machine it"
info "watches goes down is reporting a fault it cannot see, and the result is a"
info "reboot loop with no cause (ADR 0008)."
info ""
info "  just test-client HOST url [interval] [timeout] [arch]"
info ""
info "for example — note the path, the client POSTs to exactly what it is given:"
info "  just test-client user@client-host ${CURL_TARGET}${PATH_ROUTE} 5m 15s arm64"
info ""
info "That builds goosepoke, copies it over SSH, renders deploy/goosepoke.service with the"
info "URL filled in, installs it and starts the service there."

printf '\n%s' "${C_OK}"
if [ "${CHECK_OK}" -eq 1 ]; then
    printf 'watchgoose is installed, running, and the config is accepted.\n'
else
    printf 'watchgoose is installed but NOT healthy — see the warnings above.\n'
fi
printf 'Send the curl poke above before you walk away.%s\n\n' "${C_RESET}"

[ "${CHECK_OK}" -eq 1 ] || exit 1
