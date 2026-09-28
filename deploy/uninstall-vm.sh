#!/usr/bin/env bash
#
# uninstall-vm.sh — remove the watchgoose dead-man's switch from this machine.
#
#   deploy/uninstall-vm.sh      stop, disable, remove the binary and the unit.
#                               The config and the state directory STAY.
#   PURGE=1 deploy/uninstall-vm.sh
#                               also remove /etc/watchgoose.yaml and
#                               /srv/watchgoose/var
#
# Why the default keeps things: uninstalling the switch is most often a step in
# reinstalling it, and the config holds the public keys the machine repairs
# itself with. Throwing those away by default means the next install comes up
# disarmed until someone notices, and a switch that quietly lost its keys is a
# switch that repairs nobody.
#
# The recovery user and its home are NEVER touched by the default path, and are
# only removed when explicitly requested, twice over, with a typed confirmation.
# It is a standing account that may be the only way into the machine; deleting
# it as a side effect of "remove the daemon" would be unforgivable.

set -euo pipefail

SERVICE_NAME="watchgoose"
PREFIX="${WATCHGOOSE_PREFIX:-/srv/watchgoose}"
CONFIG_PATH="${WATCHGOOSE_CONFIG:-/etc/watchgoose.yaml}"
UNIT_PATH="${WATCHGOOSE_UNIT:-/etc/systemd/system/watchgoose.service}"
UNIT_NAME="${UNIT_PATH##*/}"
BIN_DST="${PREFIX}/watchgoose"
VAR_DIR="${PREFIX}/var"

PURGE="${PURGE:-0}"
# The second gate. Both must be set, and neither is set by any recipe in the
# justfile.
REMOVE_RECOVERY_USER="${REMOVE_RECOVERY_USER:-no}"

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

is_yes() {
    case "$1" in
        1|y|Y|yes|YES|true|TRUE) return 0 ;;
        *) return 1 ;;
    esac
}

[ "$(id -u)" -eq 0 ] || die "must run as root: this stops a service and writes ${PREFIX} and /etc/systemd/system"
command -v systemctl >/dev/null 2>&1 || die "systemctl not found; this is not a systemd machine"

say "Removing watchgoose from $(hostname)"

# Read the recovery account's details BEFORE anything is deleted, because after
# PURGE=1 the config is gone and with it the only record of where that account
# lives.
RECOVERY_USER="recovery"
RECOVERY_HOME="/srv/recovery"
if [ -f "${CONFIG_PATH}" ]; then
    RECOVERY_USER="$(config_value repair recovery_user "${CONFIG_PATH}" || true)"
    RECOVERY_HOME="$(config_value repair recovery_home "${CONFIG_PATH}" || true)"
    RECOVERY_USER="${RECOVERY_USER:-recovery}"
    RECOVERY_HOME="${RECOVERY_HOME:-/srv/recovery}"
fi

# 1. Stop the service --------------------------------------------------------
say "Stopping the service"
# Kill a waiting forceful-reboot child before and after the stop. The second
# pass closes the window in which a still-running daemon could spawn one.
cancel_children_without_binary() {
    local proc exe arg found
    for proc in /proc/[0-9]*; do
        exe="$(readlink "${proc}/exe" 2>/dev/null)" || continue
        case "${exe}" in
            "${BIN_DST}"|"${BIN_DST} (deleted)") ;;
            *) continue ;;
        esac
        found=0
        while IFS= read -r -d '' arg; do
            if [ "${arg}" = "-escalate-child" ]; then found=1; break; fi
        done < "${proc}/cmdline" 2>/dev/null || true
        if [ "${found}" -eq 1 ]; then
            kill -KILL "${proc##*/}" 2>/dev/null || [ ! -e "${proc}" ] || die "could not cancel escalation child ${proc##*/}"
            ok "cancelled escalation child ${proc##*/}"
        fi
    done
}
if [ -x "${BIN_DST}" ]; then
    "${BIN_DST}" -cancel-escalation-children || die "could not cancel escalation children; refusing to uninstall while a forceful reboot may still be armed"
else
    warn "${BIN_DST} is unavailable; scanning /proc for a waiting escalation child"
    cancel_children_without_binary
fi
# Tolerate a unit that is already gone: an uninstaller that fails because the
# thing it is uninstalling is absent is an uninstaller that cannot be re-run.
if [ "$(systemctl show --property=LoadState --value "${SERVICE_NAME}" 2>/dev/null)" != "not-found" ]; then
    systemctl stop "${SERVICE_NAME}" || die "could not stop ${SERVICE_NAME}; leaving the unit and binary in place"
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || warn "could not disable ${SERVICE_NAME}"
    ok "stopped and disabled ${SERVICE_NAME}"
else
    info "unit ${UNIT_NAME} is not installed; nothing to stop"
fi
if [ -x "${BIN_DST}" ]; then
    "${BIN_DST}" -cancel-escalation-children || die "could not cancel escalation children after stopping the service"
else
    cancel_children_without_binary
fi

# 2. Remove the unit ---------------------------------------------------------
say "Removing the unit"
if [ -e "${UNIT_PATH}" ]; then
    rm -f "${UNIT_PATH}"
    ok "removed ${UNIT_PATH}"
else
    info "${UNIT_PATH} is not there"
fi
systemctl daemon-reload
systemctl reset-failed "${SERVICE_NAME}" 2>/dev/null || true
info "daemon-reload done"

# 3. Remove the binary -------------------------------------------------------
say "Removing the binary"
if [ -e "${BIN_DST}" ]; then
    rm -f "${BIN_DST}"
    ok "removed ${BIN_DST}"
else
    info "${BIN_DST} is not there"
fi

# 4. Config and state -------------------------------------------------------
say "Config and state"
if is_yes "${PURGE}"; then
    if [ -e "${CONFIG_PATH}" ]; then
        rm -f "${CONFIG_PATH}"
        ok "removed ${CONFIG_PATH}"
    else
        info "${CONFIG_PATH} is not there"
    fi
    for leftover in "${CONFIG_PATH}".*.bak; do
        if [ -e "${leftover}" ]; then
            rm -f "${leftover}"
            ok "removed ${leftover}"
        fi
    done
    if [ -d "${VAR_DIR}" ]; then
        rm -rf "${VAR_DIR}"
        ok "removed ${VAR_DIR} (log and state file)"
    else
        info "${VAR_DIR} is not there"
    fi
    if [ -d "${PREFIX}" ]; then
        if rmdir "${PREFIX}" 2>/dev/null; then
            ok "removed the now-empty ${PREFIX}"
        else
            info "${PREFIX} is not empty; left in place"
        fi
    fi
else
    if [ -e "${CONFIG_PATH}" ]; then
        info "KEPT    ${CONFIG_PATH}"
        info "        It holds the public keys the machine repairs itself with."
        info "        To remove it: PURGE=1 ${0}"
    else
        info "${CONFIG_PATH} is not there"
    fi
    if [ -d "${VAR_DIR}" ]; then
        info "KEPT    ${VAR_DIR}"
        info "        The last-reassurance timestamp and the log. The timestamp is"
        info "        evidence of when the machine last heard from outside; it is"
        info "        also what a fresh install reads, so keeping it avoids a"
        info "        machine briefly believing it has gone silent."
        info "        To remove it: PURGE=1 ${0}"
    fi
    if [ -d "${PREFIX}" ]; then
        info "KEPT    ${PREFIX} (now empty; rmdir it by hand if you want it gone)"
    fi
fi

# 5. The recovery user ------------------------------------------------------
#
# Never part of the default. Gated twice, and confirmed by typing the account
# name, because this is the one thing here that cannot be reinstalled by
# running a script again.
RECOVERY_REMOVED="no"
say "The recovery user"
if ! id "${RECOVERY_USER}" >/dev/null 2>&1; then
    info "account '${RECOVERY_USER}' does not exist on this machine; nothing to do"
elif [ "${REMOVE_RECOVERY_USER}" = "delete-permanently" ]; then
    case "${RECOVERY_HOME}" in
        /|/home|/home/*)
            die "refusing to remove '${RECOVERY_USER}': its home '${RECOVERY_HOME}' is on the volume or at the filesystem root. Something is wrong with the config, and deleting the wrong home directory is not the way to find out."
            ;;
    esac
    printf '\n%sYou have asked to permanently delete the recovery account.%s\n' "${C_ERR}" "${C_RESET}"
    info "  account: ${RECOVERY_USER}"
    info "  home:    ${RECOVERY_HOME}   (and everything in it, including its keys)"
    info "  this cannot be undone, and it may be the only way into this machine."
    # Talk to the terminal directly, so the prompt and the typed answer cannot be
    # lost into a pipe or a log. Refusing without a terminal is the whole point:
    # this must never be removable by something that cannot be interrupted.
    if ! ( exec 3<>/dev/tty ) 2>/dev/null; then
        die "no terminal is available to confirm on. Run this from an interactive shell, not from a pipeline, a cron job or a service."
    fi
    exec 3<>/dev/tty
    printf '  To confirm, type the account name exactly: ' >&2
    printf '%s\n' "${RECOVERY_USER}" >&3
    IFS= read -r CONFIRM <&3 || CONFIRM=""
    exec 3>&-
    if [ "${CONFIRM}" != "${RECOVERY_USER}" ]; then
        die "confirmation did not match the account name; ${RECOVERY_USER} has been left alone."
    fi
    # No -r: delete the account and its group but not the home, so that a wrong
    # path cannot take a directory tree with it. The home is removed explicitly,
    # only after the account is gone, and only at the path the config named.
    if userdel "${RECOVERY_USER}"; then
        ok "removed the account ${RECOVERY_USER}"
    else
        die "userdel failed; ${RECOVERY_USER} may still exist. Nothing else was touched."
    fi
    if [ -d "${RECOVERY_HOME}" ]; then
        case "${RECOVERY_HOME}" in
            /srv/*) ;;
            *) die "unexpected recovery home '${RECOVERY_HOME}'; the account is gone but the directory has been left in place. Remove it by hand if you are sure." ;;
        esac
        rm -rf "${RECOVERY_HOME}"
        ok "removed ${RECOVERY_HOME}"
    fi
    for sudoers_file in /etc/sudoers.d/*"${RECOVERY_USER}"*; do
        if [ -e "${sudoers_file}" ]; then
            rm -f "${sudoers_file}"
            ok "removed ${sudoers_file}"
        fi
    done
    RECOVERY_REMOVED="yes"
else
    info "KEPT    account '${RECOVERY_USER}' and ${RECOVERY_HOME}"
    info "        It is a standing account whose credentials live on the root"
    info "        disk, so that a human has somewhere to get in when the ordinary"
    info "        accounts are unreachable. Removing the daemon is not a reason to"
    info "        remove the way in."
    info ""
    info "        If you really do want it gone:"
    info "          userdel ${RECOVERY_USER}                 # account only, keeps the home"
    info "          rm -rf ${RECOVERY_HOME}"
    info "          rm -f /etc/sudoers.d/*${RECOVERY_USER}*"
    info ""
    info "        Or let this script do it, which will make you type the account"
    info "        name to confirm:"
    info "          REMOVE_RECOVERY_USER=delete-permanently ${0}"
fi

say "Summary"
if is_yes "${PURGE}"; then
    printf '    removed:  unit, binary, %s, %s\n' "${CONFIG_PATH}" "${VAR_DIR}"
else
    printf '    removed:  unit, binary\n'
    printf '    kept:     %s, %s (PURGE=1 removes them)\n' "${CONFIG_PATH}" "${VAR_DIR}"
fi
if [ "${RECOVERY_REMOVED}" = "yes" ]; then
    printf '    recovery user %s: REMOVED\n' "${RECOVERY_USER}"
else
    printf '    recovery user %s: kept\n' "${RECOVERY_USER}"
fi
printf '\n    Not touched, deliberately: the simplevm metadata timers and\n'
printf '    /etc/fstab. The switch self-heals around the simplevm lockout bug\n'
printf '    rather than disabling it (docs/adr/0003), and the volume is repaired\n'
printf '    by rebooting, not by editing fstab (docs/adr/0004).\n'
printf '\n'
