#!/usr/bin/env bash
# Records the day-1 demo into an .mp4, no audio.
#
# Opens a dedicated Terminal window, plays demo.sh in it, captures that
# screen rect with the macOS built-in screencapture, and stops recording
# as soon as the demo ends.
#
#   ./week-01/day-01/video/record.sh [output.mp4]
#
# Requires Screen Recording permission for the terminal app that runs this
# script (System Settings > Privacy & Security > Screen & System Audio
# Recording), and a restart of that app after granting it.
#
# Window geometry: X, Y, W, H env vars (points, top-left origin).

set -euo pipefail

VIDEO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$VIDEO_DIR/../../.." && pwd)

OUT=${1:-"$VIDEO_DIR/day-01-demo.mp4"}
ENV_FILE=${ENV_FILE:-"$REPO_ROOT/.env"}
X=${X:-40}; Y=${Y:-60}; W=${W:-1280}; H=${H:-800}
TIMEOUT=${TIMEOUT:-300}

fail() { printf 'error: %s\n' "$1" >&2; exit 1; }

# Screen recording is a system permission; probe it before opening windows.
probe="${TMPDIR:-/tmp}/day01-screenprobe-$$.png"
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

[ -n "${GROQ_API_KEY:-}" ] || [ -f "$ENV_FILE" ] || fail "no GROQ_API_KEY and no $ENV_FILE"

work=$(mktemp -d -t day01video)
launcher="$work/launch.sh"
done_flag="$work/done"

# The launcher reads the key itself, so it never appears in the Terminal
# window, in the AppleScript, or in this script's arguments.
cat > "$launcher" <<LAUNCHER
#!/usr/bin/env bash
set -uo pipefail
if [ -z "\${GROQ_API_KEY:-}" ] && [ -f "$ENV_FILE" ]; then
    export GROQ_API_KEY=\$(grep -m1 '^GROQ_API_KEY=' "$ENV_FILE" | cut -d= -f2- | tr -d '"')
fi
bash "$VIDEO_DIR/demo.sh"
touch "$done_flag"
LAUNCHER
chmod +x "$launcher"

# Pre-warm the Go build cache: a first-compile stall on camera looks bad.
(cd "$REPO_ROOT" && go build -o /dev/null ./week-01/day-01) >/dev/null 2>&1 || true

osascript >/dev/null <<APPLESCRIPT
tell application "Terminal"
    activate
    set w to do script "clear; exec '$launcher'"
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
