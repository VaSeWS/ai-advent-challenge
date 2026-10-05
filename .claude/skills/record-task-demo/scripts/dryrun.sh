#!/usr/bin/env bash
# Plays a demo script at full speed: no typing delay, no pauses, no sleeps.
#
#   .claude/skills/record-task-demo/scripts/dryrun.sh week-01/day-02/video/demo.sh
#
# Every command still really runs, so this is what catches a broken flag, a
# missing key or an API error before the camera rolls. Reads GROQ_API_KEY
# from the environment, falling back to <repo>/.env.

set -uo pipefail

DEMO=${1:?usage: dryrun.sh <demo-script>}
[ -f "$DEMO" ] || { echo "error: no such demo script: $DEMO" >&2; exit 1; }
DEMO=$(cd "$(dirname "$DEMO")" && pwd)/$(basename "$DEMO")

REPO_ROOT=$(cd "$(dirname "$DEMO")" && git rev-parse --show-toplevel)

# .env is gitignored, so a worktree does not have one — fall back to the
# main checkout that owns this worktree.
ENV_FILE=${ENV_FILE:-"$REPO_ROOT/.env"}
if [ ! -f "$ENV_FILE" ]; then
    common=$(cd "$REPO_ROOT" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    [ -n "$common" ] && ENV_FILE="$(dirname "$common")/.env"
fi

for key in GROQ_API_KEY DEEPSEEK_API_KEY YANDEX_DISK_TOKEN TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID; do
    if [ -z "${!key:-}" ] && [ -f "$ENV_FILE" ]; then
        value=$(grep -m1 "^${key}=" "$ENV_FILE" | cut -d= -f2- | tr -d '"')
        export "$key=$value"
    fi
done

# A no-op sleep on PATH makes every beat in demolib instant.
stub=$(mktemp -d -t demostub)
printf '#!/bin/sh\nexit 0\n' > "$stub/sleep"
chmod +x "$stub/sleep"

DEMO_DRY_RUN=1 PATH="$stub:$PATH" SPEED=0 PAUSE=0 bash "$DEMO"
rc=$?

rm -rf "$stub"
exit "$rc"
