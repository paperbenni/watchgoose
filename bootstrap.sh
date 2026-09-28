#!/usr/bin/env bash
set -euo pipefail

release_url=https://github.com/paperbenni/watchgoose/releases/latest/download

if [[ $(uname -s) != Linux ]]; then
    printf 'watchgoose setup requires Linux with systemd.\n' >&2
    exit 1
fi

case $(uname -m) in
    x86_64) asset=watchgoose-linux-amd64 ;;
    aarch64|arm64) asset=watchgoose-linux-arm64 ;;
    *) printf 'Unsupported architecture: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

for command in curl sha256sum mktemp; do
    if ! command -v "$command" >/dev/null 2>&1; then
        printf 'Required command is missing: %s\n' "$command" >&2
        exit 1
    fi
done

if ! ( : </dev/tty ) 2>/dev/null; then
    printf 'Run this command from an interactive terminal.\n' >&2
    exit 1
fi

stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT

printf 'Downloading %s...\n' "$asset"
curl -fsSL --retry 3 "$release_url/$asset" -o "$stage/$asset"
curl -fsSL --retry 3 "$release_url/$asset.sha256" -o "$stage/$asset.sha256"
(cd "$stage" && sha256sum -c "$asset.sha256")
chmod +x "$stage/$asset"

# The script may have arrived through stdin; give the wizard the terminal.
"$stage/$asset" setup </dev/tty
