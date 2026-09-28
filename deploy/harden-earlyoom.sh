#!/usr/bin/env bash
#
# harden-earlyoom.sh — keep earlyoom away from the things that must not die.
#
# Why this exists, and why it is not part of the switch: earlyoom runs here with
# EARLYOOM_ARGS="-r 3600" and no --avoid list, so its process selection is
# unconstrained. Under memory pressure it is therefore free to kill:
#
#   * sshd      — the only way in. A reboot puts the machine back in exactly the
#                 same position, and nothing outside can fix it.
#   * watchgoose — the switch itself. A reboot cannot bring it back; only a
#                 person can.
#
# See docs/adr/0007-constrain-earlyoom.md. The switch can respond to either
# outcome but cannot prevent them, which is exactly why they are handled here.
#
#   deploy/harden-earlyoom.sh      edit /etc/default/earlyoom, print a diff
#
# Does NOT restart earlyoom. The new argument list is inert until the service is
# restarted, and restarting it is the operator's decision, so the command is
# printed rather than run.
#
# Idempotent: re-running when the list is already correct changes nothing and
# takes no backup.

set -euo pipefail

EARLYOOM_DEFAULTS="${EARLYOOM_DEFAULTS:-/etc/default/earlyoom}"
EARLYOOM_BIN="${EARLYOOM_BIN:-/usr/bin/earlyoom}"

# The list. Anchored, so that "sshd" does not also spare a process called
# "sshd-something-else" by accident, and matched against the process name
# (comm), which is why every entry here is short: the kernel truncates comm to
# 15 characters and earlyoom matches on what it sees.
#
# systemd is here as well as init, and that is not padding. ADR 0007 names init,
# but on a systemd machine PID 1 is comm "systemd", so an --avoid list that
# matched only ^init$ would spare nothing at all and would look like it had
# worked.
AVOID_RE='^init$|^systemd$|^sshd$|^earlyoom$|^watchgoose$'
AVOID_FLAG="--avoid=${AVOID_RE}"
MARKER='# --avoid list added by watchgoose, deploy/harden-earlyoom.sh (docs/adr/0007)'

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

# current_args FILE — print the value of EARLYOOM_ARGS, unquoted.
current_args() {
    awk '
        /^[[:space:]]*EARLYOOM_ARGS=/ {
            value = substr($0, index($0, "=") + 1)
            gsub(/^[ \t"]+/, "", value)
            gsub(/[ \t"]+$/, "", value)
            print value
            exit
        }
    ' "$1"
}

[ "$(id -u)" -eq 0 ] || die "must run as root: this edits ${EARLYOOM_DEFAULTS}"
[ -f "${EARLYOOM_DEFAULTS}" ] || die "${EARLYOOM_DEFAULTS} not found. Is earlyoom installed from the distribution package? (Debian/Ubuntu: apt install earlyoom). This script edits that file rather than inventing one, because the file's format belongs to the package."

say "Constraining earlyoom"
info "file: ${EARLYOOM_DEFAULTS}"

BEFORE_ARGS="$(current_args "${EARLYOOM_DEFAULTS}")"
info "current EARLYOOM_ARGS: ${BEFORE_ARGS:-<unset>}"

# --- already correct? -------------------------------------------------------
if printf '%s' "${BEFORE_ARGS}" | grep -qF -- "${AVOID_FLAG}"; then
    ok "${EARLYOOM_DEFAULTS} already carries the avoid list. Nothing to do."
    say "What earlyoom is spared"
    for name in init systemd sshd earlyoom watchgoose; do
        if printf '%s\n' "${name}" | grep -Eq "${AVOID_RE}"; then
            info "  ${name}"
        else
            die "internal error: '${name}' does not match the avoid list; this should not be possible"
        fi
    done
    say "earlyoom is still running with whatever it was started with"
    info "Check, and restart if the arguments differ from the file:"
    info "  systemctl show earlyoom --property=ExecStart"
    info "  systemctl restart earlyoom"
    printf '\n'
    exit 0
fi

# --- back up before touching anything ---------------------------------------
BACKUP="${EARLYOOM_DEFAULTS}.$(date +%Y%m%d-%H%M%S).bak"
cp -p -- "${EARLYOOM_DEFAULTS}" "${BACKUP}"
ok "backed up to ${BACKUP}"

# --- build the new argument list --------------------------------------------
#
# Anything already in EARLYOOM_ARGS is kept, including an --avoid list somebody
# else wrote: this adds to the file's intent, it does not replace it.
NEW_ARGS="${BEFORE_ARGS}"
if [ -n "${NEW_ARGS}" ]; then
    NEW_ARGS="${NEW_ARGS} ${AVOID_FLAG}"
else
    NEW_ARGS="${AVOID_FLAG}"
fi

TMP_NEW="$(mktemp "${TMPDIR:-/tmp}/earlyoom-defaults.XXXXXX")"
# shellcheck disable=SC2064  # expand $TMP_NEW now, on purpose: it is removed below
trap "rm -f '${TMP_NEW}'" EXIT

awk -v newvalue="${NEW_ARGS}" -v marker="${MARKER}" '
    # Drop a marker left by a previous run of this script, so re-running does
    # not accumulate them. Only our own lines match.
    /harden-earlyoom\.sh/ { next }
    /^[[:space:]]*EARLYOOM_ARGS=/ {
        print marker
        print "EARLYOOM_ARGS=\"" newvalue "\""
        found = 1
        next
    }
    { print }
    END {
        if (!found) {
            print marker
            print "EARLYOOM_ARGS=\"" newvalue "\""
        }
    }
' "${EARLYOOM_DEFAULTS}" >"${TMP_NEW}"

# --- validate before installing ---------------------------------------------
say "Validating the new file"
if ! bash -n "${TMP_NEW}"; then
    die "the rewritten ${EARLYOOM_DEFAULTS} is not valid shell; the original is untouched"
fi
ok "parses as shell (systemd sources this file as EnvironmentFile)"

if [ -x "${EARLYOOM_BIN}" ]; then
    if "${EARLYOOM_BIN}" --help 2>&1 | grep -q -- '--avoid'; then
        ok "${EARLYOOM_BIN} supports --avoid"
    else
        die "${EARLYOOM_BIN} does not advertise --avoid; refusing to write a flag this build may not understand"
    fi
else
    warn "${EARLYOOM_BIN} not found; cannot confirm that this earlyoom supports --avoid"
fi

# The avoid list is an extended regular expression. Check that it matches what it
# must and, just as importantly, does not match arbitrary processes.
for must_match in init systemd sshd earlyoom watchgoose; do
    if printf '%s\n' "${must_match}" | grep -Eq "${AVOID_RE}"; then
        ok "avoids ${must_match}"
    else
        die "the avoid list does not match '${must_match}'; refusing to install it"
    fi
done
for must_not_match in bash login systemd-logind cron nginx; do
    if printf '%s\n' "${must_not_match}" | grep -Eq "${AVOID_RE}"; then
        die "the avoid list matches '${must_not_match}', which it should not; the anchors are wrong"
    fi
done
ok "does not match unrelated processes (anchors are correct)"

# --- install ----------------------------------------------------------------
cat "${TMP_NEW}" >"${EARLYOOM_DEFAULTS}"
chmod --reference="${BACKUP}" -- "${EARLYOOM_DEFAULTS}" 2>/dev/null || true
chown --reference="${BACKUP}" -- "${EARLYOOM_DEFAULTS}" 2>/dev/null || true
ok "wrote ${EARLYOOM_DEFAULTS}"

AFTER_ARGS="$(current_args "${EARLYOOM_DEFAULTS}")"
if [ "${AFTER_ARGS}" != "${NEW_ARGS}" ]; then
    die "readback mismatch: wrote '${NEW_ARGS}' but the file now says '${AFTER_ARGS}'; restore with: cp ${BACKUP} ${EARLYOOM_DEFAULTS}"
fi
ok "read back: EARLYOOM_ARGS=\"${AFTER_ARGS}\""

# --- show the diff ----------------------------------------------------------
say "What changed"
if command -v diff >/dev/null 2>&1; then
    diff -u --label "before (${BACKUP})" --label "after (${EARLYOOM_DEFAULTS})" "${BACKUP}" "${EARLYOOM_DEFAULTS}" || true
else
    warn "diff not available; the change is the single line shown above"
    info "  - ${BEFORE_ARGS:-<unset>}"
    info "  + ${AFTER_ARGS}"
fi

# --- tell the operator, do not do it for them -------------------------------
say "earlyoom has NOT been restarted"
cat <<RESTART_NOTE
    The running earlyoom still has the argument list it was started with. The
    file is read once, at start. Until the service is restarted, this change
    does nothing at all — including nothing for the machine it was meant to
    protect.

    To apply it:

      systemctl restart earlyoom

    A restart kills the old earlyoom and starts a new one, which immediately
    does nothing: earlyoom only acts when memory actually runs low, and a
    restart is not a memory event. It is still left to you, because this is a
    memory supervisor on a machine that has been losing memory ungracefully,
    and the moment to restart it is better judged by someone looking at the
    machine than by a script that has not read it.

    To undo: restore the backup and restart.

      cp ${BACKUP} ${EARLYOOM_DEFAULTS}
      systemctl restart earlyoom
RESTART_NOTE

say "After this, the machine is protected against two more ways of becoming"
info "unreachable: earlyoom killing sshd, and earlyoom killing the switch."
info "Neither of those is a fault the switch can detect from inside."
printf '\n'
