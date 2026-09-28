#!/usr/bin/env bash
# Install goosepoke on this machine and manage one service instance per server.
set -euo pipefail

die() { printf 'install-client: %s\n' "$*" >&2; exit 1; }

[[ "$(uname -s)" == Linux ]] || die 'run this on the Linux machine that will send pokes'
command -v systemctl >/dev/null || die 'systemctl is required'
[[ -d /run/systemd/system ]] || die 'systemd is not running'
[[ -f "${DIST}/goosepoke-local" ]] || die 'missing built client; run just install-client'
[[ -f "${GOOSEPOKE_INSTANCE_UNIT}" ]] || die 'missing goosepoke@.service template'

if (( EUID == 0 )); then
    as_root() { "$@"; }
else
    command -v sudo >/dev/null || die 'sudo is required to install the service'
    as_root() { sudo "$@"; }
fi

printf 'Enter each server\047s full reassurance URL, including its path.\n' >&2
printf 'Example: http://100.64.0.10:9099/reassure\n' >&2
printf 'Press Enter on a blank line when done. Pokes run every 5 minutes.\n\n' >&2

urls=()
declare -A seen=()
while true; do
    printf 'Server %d URL: ' "$((${#urls[@]} + 1))" >&2
    if ! IFS= read -r url; then
        printf '\n' >&2
        break
    fi
    [[ -n "$url" ]] || break
    # A full path is required because goosepoke posts to exactly this URL.
    # Reject quoting and control characters so the value is safe in YAML.
    [[ "$url" =~ ^https?://[^/?#@[:space:]]+/[^#[:space:]]*$ ]] &&
        [[ "$url" != *"'"* && "$url" != *'"'* && "$url" != *"\\"* ]] || \
        die "invalid URL: $url (use a full http(s) URL with a path and no credentials or fragment)"
    [[ -z "${seen[$url]:-}" ]] || die "duplicate URL: $url"
    seen["$url"]=1
    urls+=("$url")
done
(( ${#urls[@]} > 0 )) || die 'at least one server URL is required; nothing was installed'

stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
for i in "${!urls[@]}"; do
    printf "url: '%s'\n" "${urls[$i]}" > "$stage/$((i + 1)).yaml"
done

as_root install -d -m 0755 -o root -g root /etc/goosepoke
if ! getent passwd goosepoke >/dev/null; then
    as_root useradd --system --no-create-home --shell "$(command -v nologin)" goosepoke
fi

# Replace by rename so a running instance never sees an overwritten executable.
as_root install -m 0755 -o root -g root "${DIST}/goosepoke-local" /usr/local/bin/goosepoke.new
as_root mv -f /usr/local/bin/goosepoke.new /usr/local/bin/goosepoke
as_root install -m 0644 -o root -g root "${GOOSEPOKE_INSTANCE_UNIT}" /etc/systemd/system/goosepoke@.service
for i in "${!urls[@]}"; do
    number=$((i + 1))
    as_root install -m 0644 -o root -g root "$stage/$number.yaml" "/etc/goosepoke/$number.yaml.new"
    as_root mv -f "/etc/goosepoke/$number.yaml.new" "/etc/goosepoke/$number.yaml"
done

as_root systemctl daemon-reload
for i in "${!urls[@]}"; do
    number=$((i + 1))
    as_root systemctl enable "goosepoke@$number.service" >/dev/null
    as_root systemctl restart "goosepoke@$number.service"
done

# Older numbered instances are no longer part of this sender's server list.
for config in /etc/goosepoke/*.yaml; do
    [[ -e "$config" ]] || continue
    number="${config##*/}"
    number="${number%.yaml}"
    [[ "$number" =~ ^[1-9][0-9]*$ ]] || continue
    if (( number > ${#urls[@]} )); then
        as_root systemctl disable --now "goosepoke@$number.service" >/dev/null
        as_root rm -f -- "$config"
    fi
done

# Retire the old single-server unit if a previous install put it here.
if [[ -f /etc/systemd/system/goosepoke.service ]]; then
    as_root systemctl disable --now goosepoke.service >/dev/null
fi

printf '\nInstalled goosepoke; each instance pokes every 5 minutes:\n'
for i in "${!urls[@]}"; do
    number=$((i + 1))
    as_root systemctl is-enabled --quiet "goosepoke@$number.service" || die "instance $number is not enabled"
    as_root systemctl is-active --quiet "goosepoke@$number.service" || die "instance $number is not active"
    printf '  goosepoke@%d.service  %s\n' "$number" "${urls[$i]}"
done
printf '\nLogs: journalctl -u "goosepoke@*.service" -f\n'
