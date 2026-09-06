#!/usr/bin/env bash
# Uploads a recording to Yandex.Disk and prints a public link.
#
#   .claude/skills/record-task-demo/scripts/upload_yadisk.sh week-01/day-02/video/day-02-demo.mp4
#
# With no remote path given, the file lands at
# /ai-advent-challenge/week-<N>/day-<NN>-demo.mp4, derived from the local
# path. Missing folders are created. NO_PUBLISH=1 uploads without publishing.
#
# Token: https://yandex.ru/dev/disk/poligon/ (OAuth token for the Disk API).
# Reads YANDEX_DISK_TOKEN from the environment, falling back to <repo>/.env
# (same file and gitignore as GROQ_API_KEY) — add a YANDEX_DISK_TOKEN=...
# line there once and every later run picks it up automatically.

set -euo pipefail

FILE=${1:?usage: upload_yadisk.sh <file> [remote-path]}
[ -f "$FILE" ] || { echo "error: no such file: $FILE" >&2; exit 1; }

REPO_ROOT=$(cd "$(dirname "$FILE")" && git rev-parse --show-toplevel)

# .env is gitignored, so a worktree does not have one — fall back to the
# main checkout that owns this worktree.
ENV_FILE=${ENV_FILE:-"$REPO_ROOT/.env"}
if [ ! -f "$ENV_FILE" ]; then
    common=$(cd "$REPO_ROOT" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    [ -n "$common" ] && ENV_FILE="$(dirname "$common")/.env"
fi

if [ -z "${YANDEX_DISK_TOKEN:-}" ] && [ -f "$ENV_FILE" ]; then
    YANDEX_DISK_TOKEN=$(grep -m1 '^YANDEX_DISK_TOKEN=' "$ENV_FILE" | cut -d= -f2- | tr -d '"')
    export YANDEX_DISK_TOKEN
fi

API=https://cloud-api.yandex.net/v1/disk/resources
: "${YANDEX_DISK_TOKEN:?export YANDEX_DISK_TOKEN or add it to <repo>/.env (https://yandex.ru/dev/disk/poligon/)}"

auth=(-H "Authorization: OAuth $YANDEX_DISK_TOKEN")

derive_remote() {
    local abs week day
    abs=$(cd "$(dirname "$FILE")" && pwd)/$(basename "$FILE")
    week=$(printf '%s' "$abs" | sed -n 's|.*/week-0*\([0-9][0-9]*\)/.*|\1|p')
    day=$(printf '%s' "$abs" | sed -n 's|.*/day-\([0-9][0-9]*\)/.*|\1|p')
    if [ -n "$week" ] && [ -n "$day" ]; then
        printf '/ai-advent-challenge/week-%s/day-%s-demo.mp4' "$week" "$day"
    else
        printf '/ai-advent-challenge/%s' "$(basename "$FILE")"
    fi
}

REMOTE=${2:-$(derive_remote)}

# Yandex.Disk does not create intermediate folders; 409 means it exists.
ensure_dirs() {
    local path=$1 acc=""
    local IFS=/
    for part in ${path%/*}; do
        [ -n "$part" ] || continue
        acc="$acc/$part"
        curl -sS -X PUT "${auth[@]}" -G --data-urlencode "path=$acc" "$API" >/dev/null || true
    done
}
ensure_dirs "$REMOTE"

href=$(curl -sS "${auth[@]}" -G \
    --data-urlencode "path=$REMOTE" --data-urlencode "overwrite=true" \
    "$API/upload" | jq -r '.href // empty')
[ -n "$href" ] || { echo "error: Yandex.Disk refused an upload URL for $REMOTE" >&2; exit 1; }

curl -sS --fail -T "$FILE" "$href"
echo "uploaded: $REMOTE"

if [ "${NO_PUBLISH:-0}" = "1" ]; then
    exit 0
fi

curl -sS -X PUT "${auth[@]}" -G --data-urlencode "path=$REMOTE" "$API/publish" >/dev/null
curl -sS "${auth[@]}" -G --data-urlencode "path=$REMOTE" --data-urlencode "fields=public_url" "$API" |
    jq -r '"public link: " + (.public_url // "not published")'
