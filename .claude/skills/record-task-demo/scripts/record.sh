#!/usr/bin/env bash
# Records a demo script into a silent .mp4.
#
#   .claude/skills/record-task-demo/scripts/record.sh week-01/day-02/video/demo.sh [out.mp4]
#
# Opens a dedicated Terminal window, plays the demo in it, captures that
# screen rect with the macOS built-in screencapture, and stops as soon as
# the demo ends. Output defaults to day-NN-demo.mp4 next to the demo script.
#
# Needs Screen Recording permission for the terminal app running this
# script (System Settings > Privacy & Security > Screen & System Audio
# Recording) and a restart of that app after granting it.
#
# Knobs (env): X, Y, W, H (window rect in points), TIMEOUT (seconds to wait
# for the demo), ENV_FILE (defaults to <repo>/.env).

set -euo pipefail

DEMO=${1:?usage: record.sh <demo-script> [output.mp4]}
[ -f "$DEMO" ] || { echo "error: no such demo script: $DEMO" >&2; exit 1; }
DEMO=$(cd "$(dirname "$DEMO")" && pwd)/$(basename "$DEMO")

REPO_ROOT=$(cd "$(dirname "$DEMO")" && git rev-parse --show-toplevel)
REL=${DEMO#"$REPO_ROOT"/}
WEEK=$(printf '%s' "$REL" | sed -n 's|^week-0*\([0-9][0-9]*\)/.*|\1|p')
DAY=$(printf '%s' "$REL" | sed -n 's|^week-[0-9][0-9]*/day-\([0-9][0-9]*\)/.*|\1|p')

if [ -n "$DAY" ]; then
    DEFAULT_OUT="$(dirname "$DEMO")/day-$DAY-demo.mp4"
else
    DEFAULT_OUT="$(dirname "$DEMO")/demo.mp4"
fi
OUT=${2:-$DEFAULT_OUT}

# .env is gitignored, so a worktree does not have one — fall back to the
# main checkout that owns this worktree.
ENV_FILE=${ENV_FILE:-"$REPO_ROOT/.env"}
if [ ! -f "$ENV_FILE" ]; then
    common=$(cd "$REPO_ROOT" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    [ -n "$common" ] && ENV_FILE="$(dirname "$common")/.env"
fi

X=${X:-40}; Y=${Y:-60}; W=${W:-1280}; H=${H:-800}
TIMEOUT=${TIMEOUT:-300}

fail() { printf 'error: %s\n' "$1" >&2; exit 1; }

# Screen recording is a system permission — probe it before opening windows.
probe="${TMPDIR:-/tmp}/demo-screenprobe-$$.png"
if ! screencapture -x "$probe" 2>/dev/null || [ ! -s "$probe" ]; then
    rm -f "$probe"
    cat >&2 <<'MSG'
error: no Screen Recording permission for this terminal app.

  1. System Settings > Privacy & Security > Screen & System Audio Recording
  2. Enable the terminal app you are running this from
  3. Quit and reopen that app, then run this script again
MSG
    exit 1
fi
rm -f "$probe"

work=$(mktemp -d -t taskdemo)
launcher="$work/launch.sh"
done_flag="$work/done"

# The launcher reads the key itself, so no secret ever reaches the Terminal
# window, the AppleScript, or this script's arguments.
cat > "$launcher" <<LAUNCHER
#!/usr/bin/env bash
set -uo pipefail
cd "$REPO_ROOT"
if [ -z "\${GROQ_API_KEY:-}" ] && [ -f "$ENV_FILE" ]; then
    export GROQ_API_KEY=\$(grep -m1 '^GROQ_API_KEY=' "$ENV_FILE" | cut -d= -f2- | tr -d '"')
fi
bash "$DEMO"
touch "$done_flag"
LAUNCHER
chmod +x "$launcher"

# Pre-warm the Go build cache: a first-compile stall on camera looks bad.
if [ -n "$DAY" ] && [ -d "$REPO_ROOT/week-0$WEEK/day-$DAY" ]; then
    (cd "$REPO_ROOT" && go build -o /dev/null "./week-0$WEEK/day-$DAY") >/dev/null 2>&1 || true
fi

osascript >/dev/null <<APPLESCRIPT
tell application "Terminal"
    activate
    do script "clear; exec '$launcher'"
    delay 0.4
    set bounds of front window to {$X, $Y, $((X + W)), $((Y + H))}
end tell
APPLESCRIPT

sleep 2

rm -f "$OUT"
screencapture -v -x -R "$X,$Y,$W,$H" "$OUT" &
rec_pid=$!

waited=0
while [ ! -f "$done_flag" ] && [ "$waited" -lt "$TIMEOUT" ]; do
    sleep 1
    waited=$((waited + 1))
done

sleep 1
kill -INT "$rec_pid" 2>/dev/null || true
wait "$rec_pid" 2>/dev/null || true
rm -rf "$work"

[ -s "$OUT" ] || fail "recording produced no file — check Screen Recording permission"

printf 'recorded: %s (%s)\n' "$OUT" "$(du -h "$OUT" | cut -f1)"
if [ -n "$DAY" ] && [ -n "$WEEK" ]; then
    printf 'suggested remote: /ai-advent-challenge/week-%s/day-%s-demo.mp4\n' "$WEEK" "$DAY"
fi
