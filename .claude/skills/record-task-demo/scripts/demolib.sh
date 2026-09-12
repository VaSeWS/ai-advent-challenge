# Shared helpers for day demo scripts. Source it, then describe the shots:
#
#   source "$(dirname "${BASH_SOURCE[0]}")/../../../.claude/skills/record-task-demo/scripts/demolib.sh"
#   demo_title "AI Advent — День 2" "Контроль формата ответа"
#   demo_note  "Без ограничений — модель отвечает как хочет."
#   demo_run   'go run ./week-01/day-02 "..."'
#   demo_outro "week-01/day-02"
#
# Knobs (env): SPEED (per-character delay), PAUSE (beat after each step),
# PROMPT_LABEL (the path shown in the fake prompt).
#
# A dry run sets SPEED=0 PAUSE=0 and stubs sleep, so the same script plays
# instantly when you are only checking that the commands work.

SPEED=${SPEED:-0.025}
PAUSE=${PAUSE:-1.0}
PROMPT_LABEL=${PROMPT_LABEL:-'~/DevProjects/ai-advent-challenge'}

DEMO_BOLD=$'\033[1m'; DEMO_DIM=$'\033[2m'; DEMO_RESET=$'\033[0m'
DEMO_GREEN=$'\033[32m'; DEMO_BLUE=$'\033[34m'; DEMO_RED=$'\033[31m'; DEMO_CYAN=$'\033[36m'

demo_nap() { [ "$1" != "0" ] && sleep "$1"; }

# Print a string one character at a time, then a newline.
demo_typewrite() {
    local text=$1 i
    for ((i = 0; i < ${#text}; i++)); do
        printf '%s' "${text:i:1}"
        demo_nap "$SPEED"
    done
    printf '\n'
}

demo_prompt() {
    printf '%s%s%s %s$%s ' "$DEMO_BLUE" "$PROMPT_LABEL" "$DEMO_RESET" "$DEMO_GREEN" "$DEMO_RESET"
}

# Type a command at the prompt, then run it.
demo_run() {
    demo_prompt
    demo_typewrite "$1"
    demo_nap 0.3
    eval "$1"
    demo_nap "$PAUSE"
}

# Type one command but run another. The only honest use is hiding a secret:
# type `export GROQ_API_KEY=gsk_••••` while the real value stays in the env.
demo_run_masked() {
    demo_prompt
    demo_typewrite "$1"
    demo_nap 0.3
    eval "$2"
    demo_nap "$PAUSE"
}

# A dim comment line above a step — this is the narration, there is no audio.
demo_note() {
    printf '%s# %s%s\n' "$DEMO_DIM" "$1" "$DEMO_RESET"
    demo_nap 0.6
}

demo_pause() { demo_nap "${1:-1.5}"; }

# Opening card: clears the screen, states which day and what it shows.
demo_title() {
    clear
    printf '%s%s%s%s\n' "$DEMO_BOLD" "$DEMO_CYAN" "$1" "$DEMO_RESET"
    [ $# -gt 1 ] && printf '%s%s%s\n' "$DEMO_DIM" "$2" "$DEMO_RESET"
    printf '\n'
    demo_nap 2.5
}

# Closing card. Give it the day's path; it holds long enough to be readable.
demo_outro() {
    printf '\n%s%sГотово.%s %s%s%s\n' \
        "$DEMO_BOLD" "$DEMO_GREEN" "$DEMO_RESET" "$DEMO_DIM" "${1:-}" "$DEMO_RESET"
    demo_nap 4
}

# Fail before the camera rolls, not halfway through a take.
demo_require_env() {
    local var
    for var in "$@"; do
        if [ -z "${!var:-}" ]; then
            printf '%serror: %s is not set%s\n' "$DEMO_RED" "$var" "$DEMO_RESET" >&2
            exit 1
        fi
    done
}

# Absolute repo root, so demos can cd there regardless of where they live.
demo_repo_root() {
    local dir=${1:-$PWD}
    (cd "$dir" && git rev-parse --show-toplevel 2>/dev/null) || printf '%s' "$dir"
}

# --- Full-screen TUI helpers (tmux-driven) --------------------------------
#
# demo_run/demo_typewrite assume a plain shell prompt that returns after one
# command. A full-screen program (Bubble Tea, etc.) owns the whole terminal
# instead, so keystrokes have to be injected into its pty from outside:
#
#   demo_tui_start "go run ./week-02 -db \$DB -provider groq" 100 30
#   demo_tui_wait_for 'Write a message' 10   # app fully rendered, raw mode on
#   demo_tui_type  'Привет!'
#   demo_tui_key   Enter
#   demo_tui_wait_stable 25 1
#   demo_tui_type  '/quit'
#   demo_tui_key   Enter
#   demo_tui_watch   # attaches when recording (real tty), else runs headless
#   demo_tui_stop
#
# Requires tmux. Session name is $TUI_SESSION (default: demo-tui).

TUI_SESSION=${TUI_SESSION:-demo-tui}

demo_tui_require() {
    command -v tmux >/dev/null 2>&1 || {
        printf '%serror: tmux is required for TUI demos (brew install tmux)%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        exit 1
    }
}

# Start the program detached inside a tmux session.
demo_tui_start() {
    local cmd=$1 cols=${2:-100} rows=${3:-30}
    demo_tui_require
    tmux kill-session -t "$TUI_SESSION" 2>/dev/null || true
    tmux new-session -d -s "$TUI_SESSION" -x "$cols" -y "$rows" "$cmd"
}

# Type text into the running TUI, one character at a time (visible typing).
demo_tui_type() {
    local text=$1 i ch
    for ((i = 0; i < ${#text}; i++)); do
        ch=${text:i:1}
        tmux send-keys -t "$TUI_SESSION" -l -- "$ch" 2>/dev/null || true
        demo_nap "$SPEED"
    done
}

# Send a named key (Enter, C-c, PageUp, PageDown, ...) to the TUI.
demo_tui_key() {
    tmux send-keys -t "$TUI_SESSION" "$1" 2>/dev/null || true
}

# Block until the visible pane stops changing for `stable_for` seconds (the
# async response finished rendering) or `timeout` seconds pass. The actual
# API call this waits on is real and cannot be sped up, so the poll delay
# uses /bin/sleep (absolute path) rather than bare `sleep` — dryrun.sh puts a
# no-op `sleep` earlier on PATH to make one-shot demo_run commands replay
# instantly, and a zeroed poll delay here would busy-loop capture-pane fast
# enough to starve the tmux server and race the real response.
demo_tui_wait_for() {
    local pattern=$1 timeout=${2:-20} start now
    start=$(date +%s)
    while true; do
        tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null | grep -qF "$pattern" && return 0
        now=$(date +%s)
        [ $((now - start)) -ge "$timeout" ] && return 1
        /bin/sleep 0.2 2>/dev/null || true
    done
}

demo_tui_wait_stable() {
    local timeout=${1:-20} stable_for=${2:-1} start now prev cur last_change
    start=$(date +%s); last_change=$start; prev=$'\x01'
    while true; do
        cur=$(tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null || true)
        now=$(date +%s)
        if [ "$cur" != "$prev" ]; then prev=$cur; last_change=$now; fi
        [ $((now - last_change)) -ge "$stable_for" ] && return 0
        [ $((now - start)) -ge "$timeout" ] && return 1
        /bin/sleep 0.3 2>/dev/null || true
    done
}

# Attach the recording terminal to the TUI session when there is a real tty
# to attach from (the Terminal.app window record.sh opens); otherwise this is
# a dry run with no tty, so just wait for the caller's background sender
# and print the final pane content for inspection.
demo_tui_watch() {
    if [ -t 1 ] && [ -t 0 ]; then
        tmux attach -t "$TUI_SESSION"
    else
        wait 2>/dev/null || true
        tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null || true
    fi
}

demo_tui_stop() {
    tmux kill-session -t "$TUI_SESSION" 2>/dev/null || true
}
