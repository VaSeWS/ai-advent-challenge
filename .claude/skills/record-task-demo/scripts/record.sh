#!/usr/bin/env bash
# Records a demo script into a silent .mp4.
#
#   .claude/skills/record-task-demo/scripts/record.sh week-01/day-02/video/demo.sh [out.mp4]
#
# Opens a dedicated Terminal window, plays the demo in it, captures that
# window with macOS ScreenCaptureKit, and stops as soon as
# the demo ends. Output defaults to day-NN-demo.mp4 next to the demo script.
#
# Needs Screen Recording permission for the terminal app running this
# script (System Settings > Privacy & Security > Screen & System Audio
# Recording) and a restart of that app after granting it.
#
# Knobs (env): X, Y, W, H (window rect in points), TIMEOUT (seconds to wait
# for the demo), ENV_FILE (defaults to <repo>/.env), CAPTURE_WINDOW=0
# (opt into screen-rectangle capture instead of the owned Terminal window).

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
TIMEOUT=${TIMEOUT:-7200}
CAPTURE_WINDOW=${CAPTURE_WINDOW:-1}

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
status_file="$work/exit-status"
capture_ready="$work/capture-ready"
if [ "$CAPTURE_WINDOW" = 1 ]; then
    swiftc "$REPO_ROOT/.claude/skills/record-task-demo/scripts/record-window.swift" -o "$work/record-window"
fi

# The launcher reads the key itself, so no secret ever reaches the Terminal
# window, the AppleScript, or this script's arguments.
cat > "$launcher" <<LAUNCHER
#!/usr/bin/env bash
set -uo pipefail
cd "$REPO_ROOT"
for key in GROQ_API_KEY DEEPSEEK_API_KEY YANDEX_DISK_TOKEN TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID; do
    if [ -z "\${!key:-}" ] && [ -f "$ENV_FILE" ]; then
        value=\$(grep -m1 "^\${key}=" "$ENV_FILE" | cut -d= -f2- | tr -d '"')
        export "\$key=\$value"
    fi
done
for ((attempt = 0; attempt < 300; attempt++)); do
    [ -f "$capture_ready" ] && break
    sleep 0.1
done
if [ -f "$capture_ready" ]; then
    bash "$DEMO"
    status=\$?
else
    printf 'error: recorder did not become ready\n' >&2
    status=1
fi
printf '%s\n' "\$status" > "$status_file"
touch "$done_flag"
exit "\$status"
LAUNCHER
chmod +x "$launcher"

# Pre-warm the Go build cache: a first-compile stall on camera looks bad.
if [ -n "$DAY" ] && [ -d "$REPO_ROOT/week-0$WEEK/day-$DAY" ]; then
    (cd "$REPO_ROOT" && go build -o /dev/null "./week-0$WEEK/day-$DAY") >/dev/null 2>&1 || true
fi

window_id=$(osascript <<APPLESCRIPT
set profileName to system attribute "DEMO_TERMINAL_PROFILE"
tell application "Terminal"
    activate
    set demoTab to do script "clear; exec '$launcher'"
    set demoTTY to tty of demoTab
    repeat with demoWindow in windows
        repeat with candidateTab in tabs of demoWindow
            if tty of candidateTab is demoTTY then
                if profileName is not "" then set current settings of demoWindow to settings set profileName
                set bounds of demoWindow to {$X, $Y, $((X + W)), $((Y + H))}
                return id of demoWindow
            end if
        end repeat
    end repeat
    error "could not locate the demo Terminal window"
end tell
APPLESCRIPT
) || fail "could not open the demo Terminal window"
[[ "$window_id" =~ ^[1-9][0-9]*$ ]] ||
    fail "Terminal returned an invalid demo window ID"

sleep 2

rm -f "$OUT"
if [ "$CAPTURE_WINDOW" = 1 ]; then
    # The launcher tab owns this window; never recapture an unrelated front window.
    "$work/record-window" "$window_id" "$OUT" "$capture_ready" &
else
    screencapture -v -x -R "$X,$Y,$W,$H" "$OUT" &
    touch "$capture_ready"
fi
rec_pid=$!

waited=0
while [ ! -f "$done_flag" ] && [ "$waited" -lt "$TIMEOUT" ]; do
    sleep 1
    waited=$((waited + 1))
done

kill -INT "$rec_pid" 2>/dev/null || true
recorder_status=0
wait "$rec_pid" || recorder_status=$?
if [ "$CAPTURE_WINDOW" = 1 ] && [ "$recorder_status" -ne 0 ]; then
    rm -rf "$work"
    fail "window recorder exited with status $recorder_status; failed recording retained at $OUT"
fi

[ -s "$OUT" ] || {
    rm -rf "$work"
    fail "recording produced no file — check Screen Recording permission"
}
[ -f "$status_file" ] || {
    rm -rf "$work"
    fail "demo did not report an exit status; failed recording retained at $OUT"
}
if ! IFS= read -r demo_status < "$status_file" ||
    [[ ! "$demo_status" =~ ^[0-9]+$ ]]; then
    rm -rf "$work"
    fail "demo reported an invalid exit status; failed recording retained at $OUT"
fi
rm -rf "$work"
[ "$demo_status" -eq 0 ] ||
    fail "demo exited with status $demo_status; failed recording retained at $OUT"


printf 'recorded: %s (%s)\n' "$OUT" "$(du -h "$OUT" | cut -f1)"
if [ -n "$DAY" ] && [ -n "$WEEK" ]; then
    printf 'suggested remote: /ai-advent-challenge/week-%s/day-%s-demo.mp4\n' "$WEEK" "$DAY"
fi
