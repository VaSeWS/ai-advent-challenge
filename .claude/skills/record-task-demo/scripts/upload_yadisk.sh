#!/usr/bin/env bash
# Publish a homework recording via the local Yandex Disk MCP server.
#   .claude/skills/record-task-demo/scripts/upload_yadisk.sh week-04/day-17/video/day-17-demo.mp4
#
# Only removes the local file after the MCP tool returns a public link.

set -euo pipefail

FILE=${1:?usage: upload_yadisk.sh <week-NN/day-NN/video/*.mp4>}
[ -f "$FILE" ] || { echo "error: no such file: $FILE" >&2; exit 1; }

REPO_ROOT=$(cd "$(dirname "$FILE")" && git rev-parse --show-toplevel)
ABS_FILE=$(cd "$(dirname "$FILE")" && pwd)/$(basename "$FILE")

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

if [ -z "${YANDEX_DISK_TOKEN:-}" ]; then
    echo "error: export YANDEX_DISK_TOKEN or add it to <repo>/.env" >&2
    exit 1
fi

cd "$REPO_ROOT"
go run ./week-04/day-17 upload "$ABS_FILE"
rm -f "$ABS_FILE"
echo "removed local copy: $FILE"
