#!/usr/bin/env bash
set -euo pipefail

readonly REMOTE='https://github.com/VaSeWS/iRon_fork.git'
readonly REVISION='cb76b89633554e1dd7f288087054f30b00bd9a55'

if [ "$#" -gt 1 ]; then
    printf 'usage: %s [new-target-directory]\n' "$0" >&2
    exit 2
fi

if [ "$#" -eq 1 ]; then
    target=$1
    if [ -e "$target" ] || [ -L "$target" ]; then
        printf 'refusing to use existing target: %s\n' "$target" >&2
        exit 1
    fi
    case "$target" in
        /*) ;;
        *) target="./$target" ;;
    esac
    parent=$(dirname "$target")
    if [ ! -d "$parent" ]; then
        printf 'target parent does not exist: %s\n' "$parent" >&2
        exit 1
    fi
else
    parent=$(mktemp -d "${TMPDIR:-/tmp}/day34-iron-parent.XXXXXX")
    target="$parent/iRon_fork"
fi

if ! git clone --no-checkout "$REMOTE" "$target" >&2; then
    printf 'clone failed; any created target was left untouched: %s\n' "$target" >&2
    exit 1
fi
if ! git -C "$target" checkout --detach "$REVISION" >&2; then
    printf 'checkout failed; clone was left untouched for inspection: %s\n' "$target" >&2
    exit 1
fi
actual=$(git -C "$target" rev-parse HEAD)
if [ "$actual" != "$REVISION" ]; then
    printf 'unexpected revision in clone: %s\n' "$actual" >&2
    exit 1
fi

printf '%s\n' "$(cd "$target" && pwd -P)"
