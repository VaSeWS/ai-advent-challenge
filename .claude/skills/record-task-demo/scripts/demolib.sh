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

demo_nap() { if [ "$1" != "0" ]; then sleep "$1"; fi; }

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
# Requires tmux. Each invocation uses its own private socket and session.

TUI_TMUX_DIR=
TUI_TMUX_SOCKET=
TUI_TMUX_OWNER_FILE=
TUI_TMUX_OWNER_IDENTITY=
TUI_TMUX_SOCKET_IDENTITY=
TUI_TMUX_SERVER_PID=
TUI_TMUX_SERVER_PGID=
TUI_TMUX_SERVER_STATE=
TUI_TMUX_SERVER_START=
TUI_TMUX_SERVER_JOINED=
TUI_TMUX_SERVER_EXIT=
TUI_TMUX_PANE_PID=
TUI_TMUX_PANE_STATE=
TUI_TMUX_PANE_START=
TUI_TMUX_PANE_PGID=
TUI_SESSION=${TUI_SESSION:-"demo-${UID:-user}-$$-${RANDOM}"}

demo_tui_require() {
    command -v tmux >/dev/null 2>&1 || {
        printf '%serror: tmux is required for TUI demos (brew install tmux)%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        exit 1
    }
}

demo_tui_state() {
    if [ -n "${DEMO_PRIVATE_TMUX_STATE_FILE:-}" ]; then
        printf '%s\n' "$1" > "$DEMO_PRIVATE_TMUX_STATE_FILE" || return 1
    fi
}
demo_tmux() {
    tmux -N -S "$TUI_TMUX_SOCKET" "$@"
}

# Return "pgid|state|start-time" for one process, status 1 if absent, and
# status 2 if the process table could not be inspected.
demo_tui_process_record() {
    local wanted_pid=$1 process_table line current_pid current_pgid current_state start_time
    process_table=$(LC_ALL=C /bin/ps -ax -o pid= -o pgid= -o stat= -o lstart= 2>/dev/null) || return 2
    while IFS= read -r line; do
        read -r current_pid current_pgid current_state start_time <<< "$line"
        if [ "$current_pid" = "$wanted_pid" ]; then
            printf '%s|%s|%s\n' "$current_pgid" "$current_state" "$start_time"
            return 0
        fi
    done <<< "$process_table"
    return 1
}

# Statuses: 0 is the exact live process, 1 is absent/zombie, 2 is an
# inspection failure, and 3 is a PID whose recorded start identity mismatches.
demo_tui_owned_process_is_active() {
    local record status current_state current_start
    if record=$(demo_tui_process_record "$1"); then
        current_state=${record#*|}
        current_state=${current_state%%|*}
        current_start=${record##*|}
        if [ "$current_start" != "$2" ]; then
            return 3
        fi
        case "$current_state" in
            *Z*) return 1 ;;
        esac
        return 0
    else
        status=$?
        return "$status"
    fi
}

# Return 0 when a live process remains in the recorded pane process group,
# 1 when it is empty (zombies are not running workload), and 2 if inspection
# failed.
demo_tui_process_group_present() {
    local wanted_pgid=$1 process_table line current_pgid current_state
    process_table=$(LC_ALL=C /bin/ps -ax -o pgid= -o stat= 2>/dev/null) || return 2
    while IFS= read -r line; do
        read -r current_pgid current_state <<< "$line"
        if [ "$current_pgid" = "$wanted_pgid" ]; then
            case "$current_state" in
                *Z*) continue ;;
                *) return 0 ;;
            esac
        fi
    done <<< "$process_table"
    return 1
}

demo_tui_socket_identity() {
    [ -S "$TUI_TMUX_SOCKET" ] && [ ! -L "$TUI_TMUX_SOCKET" ] || return 1
    /usr/bin/stat -f '%d:%i' "$TUI_TMUX_SOCKET"
}

demo_tui_write_owner() {
    local lifecycle=$1 current_identity
    [ -f "$TUI_TMUX_OWNER_FILE" ] && [ ! -L "$TUI_TMUX_OWNER_FILE" ] || return 1
    current_identity=$(/usr/bin/stat -f '%d:%i' "$TUI_TMUX_OWNER_FILE") || return 1
    [ "$current_identity" = "$TUI_TMUX_OWNER_IDENTITY" ] || return 1
    {
        printf 'state=%s\n' "$lifecycle"
        printf 'directory=%s\n' "$TUI_TMUX_DIR"
        printf 'socket=%s\n' "$TUI_TMUX_SOCKET"
        printf 'socket_identity=%s\n' "$TUI_TMUX_SOCKET_IDENTITY"
        printf 'server_pid=%s\n' "$TUI_TMUX_SERVER_PID"
        printf 'server_pgid=%s\n' "$TUI_TMUX_SERVER_PGID"
        printf 'server_state=%s\n' "$TUI_TMUX_SERVER_STATE"
        printf 'server_exit_status=%s\n' "$TUI_TMUX_SERVER_EXIT"
        printf 'server_start=%s\n' "$TUI_TMUX_SERVER_START"
        printf 'pane_pid=%s\n' "$TUI_TMUX_PANE_PID"
        printf 'pane_state=%s\n' "$TUI_TMUX_PANE_STATE"
        printf 'pane_start=%s\n' "$TUI_TMUX_PANE_START"
        printf 'pane_pgid=%s\n' "$TUI_TMUX_PANE_PGID"
    } > "$TUI_TMUX_OWNER_FILE"
}

demo_tui_start() {
    local cmd=$1 cols=${2:-100} rows=${3:-30}
    local attempt server_record actual_server_pid pane_record server_fields pane_fields
    demo_tui_require
    if [ -n "$TUI_TMUX_DIR" ] || [ -n "$TUI_TMUX_SOCKET" ]; then
        printf '%serror: a private TUI lifecycle is already active or unreconciled%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    fi
    demo_tui_state starting || return 1
    TUI_TMUX_DIR=$(mktemp -d "${TMPDIR:-/tmp}/taskdemo-tmux.XXXXXX") || return 1
    TUI_TMUX_SOCKET="$TUI_TMUX_DIR/server.sock"
    if [ -n "${DEMO_PRIVATE_TMUX_STATE_FILE:-}" ]; then
        TUI_TMUX_OWNER_FILE="${DEMO_PRIVATE_TMUX_STATE_FILE}.owner"
    else
        TUI_TMUX_OWNER_FILE="${TUI_TMUX_DIR}.owner"
    fi
    if [ -e "$TUI_TMUX_OWNER_FILE" ] || [ -L "$TUI_TMUX_OWNER_FILE" ]; then
        printf '%serror: private tmux owner record already exists: %s%s\n' \
            "$DEMO_RED" "$TUI_TMUX_OWNER_FILE" "$DEMO_RESET" >&2
        return 1
    fi
    (
        umask 077
        set -C
        : > "$TUI_TMUX_OWNER_FILE"
    ) || return 1
    TUI_TMUX_OWNER_IDENTITY=$(/usr/bin/stat -f '%d:%i' "$TUI_TMUX_OWNER_FILE") || return 1
    demo_tui_write_owner starting || return 1

    # Retain the foreground server as a child, but keep its stdio off the
    # recording TTY: tmux refuses to attach a client to its server's TTY.
    # -D disables exit-empty for this private server only.
    tmux -D -f /dev/null -S "$TUI_TMUX_SOCKET" </dev/null >/dev/null 2>&1 &
    TUI_TMUX_SERVER_PID=$!
    demo_tui_write_owner starting || return 1
    for ((attempt = 0; attempt < 100; attempt++)); do
        if [ -S "$TUI_TMUX_SOCKET" ] && [ ! -L "$TUI_TMUX_SOCKET" ]; then
            break
        fi
        /bin/sleep 0.02 || return 1
    done
    TUI_TMUX_SOCKET_IDENTITY=$(demo_tui_socket_identity) || {
        printf '%serror: private tmux server did not create its owned socket%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    demo_tui_write_owner starting || return 1
    server_record=$(demo_tui_process_record "$TUI_TMUX_SERVER_PID") || {
        printf '%serror: private tmux server identity is unavailable%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    TUI_TMUX_SERVER_PGID=${server_record%%|*}
    server_fields=${server_record#*|}
    TUI_TMUX_SERVER_STATE=${server_fields%%|*}
    TUI_TMUX_SERVER_START=${server_record##*|}
    demo_tui_write_owner starting || return 1
    case "$TUI_TMUX_SERVER_STATE" in
        *Z*)
            printf '%serror: private tmux server exited before startup completed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
            ;;
    esac
    actual_server_pid=$(demo_tmux display-message -p '#{pid}') || {
        printf '%serror: private tmux server did not accept its owned socket%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    if [ "$actual_server_pid" != "$TUI_TMUX_SERVER_PID" ]; then
        printf '%serror: private tmux socket resolved to an unowned server%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    fi
    demo_tui_write_owner starting || return 1

    demo_tmux new-session -d -s "$TUI_SESSION" -x "$cols" -y "$rows" "$cmd" || return 1
    TUI_TMUX_PANE_PID=$(demo_tmux display-message -p -t "$TUI_SESSION" '#{pane_pid}') || {
        printf '%serror: private TUI pane identity is unavailable%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    demo_tui_write_owner starting || return 1
    case "$TUI_TMUX_PANE_PID" in
        ''|*[!0-9]*)
            printf '%serror: private TUI pane returned an invalid PID%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
            ;;
    esac
    pane_record=$(demo_tui_process_record "$TUI_TMUX_PANE_PID") || {
        printf '%serror: private TUI pane start identity is unavailable%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    TUI_TMUX_PANE_PGID=${pane_record%%|*}
    pane_fields=${pane_record#*|}
    TUI_TMUX_PANE_STATE=${pane_fields%%|*}
    TUI_TMUX_PANE_START=${pane_record##*|}
    demo_tui_write_owner starting || return 1
    case "$TUI_TMUX_PANE_STATE" in
        *Z*)
            printf '%serror: private TUI pane exited before startup completed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
            ;;
    esac
    demo_tui_write_owner running || return 1
    demo_tui_state running || return 1
}


# Type text into the running TUI, one character at a time (visible typing).
demo_tui_type() {
    local text=$1 i ch
    for ((i = 0; i < ${#text}; i++)); do
        ch=${text:i:1}
        demo_tmux send-keys -t "$TUI_SESSION" -l -- "$ch" 2>/dev/null || true
        demo_nap "$SPEED"
    done
}

# Send a named key (Enter, C-c, PageUp, PageDown, ...) to the TUI.
demo_tui_key() {
    demo_tmux send-keys -t "$TUI_SESSION" "$1" 2>/dev/null || true
}

# Insert a whole block of text in one shot (no per-character typing effect) —
# for payloads too large or too uninteresting to type out, e.g. a calibrated
# oversized paste used to demonstrate a context-overflow guard.
#
# Uses tmux's paste-buffer mechanism, not send-keys: `send-keys -l` passes
# the text as a single tmux command argument, which tmux itself refuses past
# a length far below what a context-overflow payload needs ("command too
# long"). paste-buffer has no such limit, but ALSO needs -p (bracketed
# paste) — without it, tmux still feeds the buffer to the pane one byte at a
# time, exactly like typing, so a 130,000-character payload can take tens of
# seconds to land and races whatever comes next in the script. With -p the
# target program receives it as a single paste event (if it handles
# bracketed paste, as bubbles/textarea does) and it appears in well under a
# second.
demo_tui_paste() {
    local text=$1 file
    file=$(mktemp -t demo-tui-paste)
    printf '%s' "$text" > "$file"
    demo_tmux load-buffer -b demo_tui_paste "$file" 2>/dev/null || true
    demo_tmux paste-buffer -p -t "$TUI_SESSION" -b demo_tui_paste 2>/dev/null || true
    demo_tmux delete-buffer -b demo_tui_paste 2>/dev/null || true
    rm -f "$file"
}

# Detach the attached recording terminal from the TUI session without
# stopping the program — lets the demo script regain the outer terminal to
# print a caption (demo_note/demo_pause) between two full-screen phases,
# then call demo_tui_watch again to reattach. No-op if nothing is attached
# (e.g. during a headless dry run).
demo_tui_detach() {
    demo_tmux detach-client -s "$TUI_SESSION" 2>/dev/null || true
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
        demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null | grep -qF "$pattern" && return 0
        now=$(date +%s)
        [ $((now - start)) -ge "$timeout" ] && return 1
        /bin/sleep 0.2 2>/dev/null || true
    done
}

demo_tui_wait_stable() {
    local timeout=${1:-20} stable_for=${2:-1} start now prev cur last_change
    start=$(date +%s); last_change=$start; prev=$'\x01'
    while true; do
        cur=$(demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null || true)
        now=$(date +%s)
        if [ "$cur" != "$prev" ]; then prev=$cur; last_change=$now; fi
        [ $((now - last_change)) -ge "$stable_for" ] && return 0
        [ $((now - start)) -ge "$timeout" ] && return 1
        /bin/sleep 0.3 2>/dev/null || true
    done
}

# Attach the recording terminal to the TUI session when there is a real tty;
# otherwise this is a dry run with no tty, so just wait for the caller's
# background sender and print the final pane content for inspection.
demo_tui_watch() {
    local child_pids child_pid wait_status=0
    if [ -t 1 ] && [ -t 0 ]; then
        demo_tmux attach -t "$TUI_SESSION"
    else
        if [ $# -gt 0 ]; then
            wait "$1"
        else
            child_pids=$(jobs -pr)
            while IFS= read -r child_pid; do
                if [ -n "$child_pid" ] && [ "$child_pid" != "$TUI_TMUX_SERVER_PID" ]; then
                    if wait "$child_pid"; then :; else wait_status=$?; fi
                fi
            done <<< "$child_pids"
            [ "$wait_status" -eq 0 ] || return "$wait_status"
        fi
        demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null || true
    fi
}

demo_tui_socket_has_no_holder() {
    local output status
    if ! command -v lsof >/dev/null 2>&1; then
        printf '%serror: lsof is unavailable; private tmux socket ownership is unconfirmed%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 2
    fi
    output=$(lsof +w -nP -a -U "$TUI_TMUX_SOCKET" 2>&1)
    status=$?
    if [ "$status" -eq 1 ] && [ -z "$output" ]; then
        return 0
    fi
    if [ -n "$output" ]; then
        printf '%serror: private tmux socket holder check was inconclusive or found a holder: %s%s\n' \
            "$DEMO_RED" "$output" "$DEMO_RESET" >&2
    else
        printf '%serror: private tmux socket holder check exited with status %s%s\n' \
            "$DEMO_RED" "$status" "$DEMO_RESET" >&2
    fi
    return 1
}

demo_tui_stop() {
    local process_status actual_server_pid command_output command_status
    local pane_status group_status current_socket_identity owner_identity
    if [ -z "$TUI_TMUX_DIR" ] && [ -z "$TUI_TMUX_SOCKET" ]; then
        return 0
    fi
    if [ -z "$TUI_TMUX_DIR" ] || [ -z "$TUI_TMUX_SOCKET" ] ||
        [ -z "$TUI_TMUX_SERVER_PID" ] || [ -z "$TUI_TMUX_SERVER_START" ] ||
        [ -z "$TUI_TMUX_SOCKET_IDENTITY" ] || [ -z "$TUI_TMUX_PANE_PID" ] ||
        [ -z "$TUI_TMUX_PANE_START" ] || [ -z "$TUI_TMUX_PANE_PGID" ]; then
        printf '%serror: private tmux ownership evidence is incomplete; retaining its state%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    fi
    demo_tui_write_owner stopping || {
        printf '%serror: private tmux owner record changed or is unavailable%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }

    if demo_tui_owned_process_is_active "$TUI_TMUX_SERVER_PID" "$TUI_TMUX_SERVER_START"; then
        if [ ! -S "$TUI_TMUX_SOCKET" ] || [ -L "$TUI_TMUX_SOCKET" ]; then
            printf '%serror: owned tmux server is live but its socket identity is missing%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
        current_socket_identity=$(demo_tui_socket_identity) || {
            printf '%serror: private tmux socket identity could not be read%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        }
        if [ "$current_socket_identity" != "$TUI_TMUX_SOCKET_IDENTITY" ]; then
            printf '%serror: private tmux socket identity changed; refusing shutdown%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
        if actual_server_pid=$(demo_tmux display-message -p '#{pid}' 2>&1); then
            if [ "$actual_server_pid" != "$TUI_TMUX_SERVER_PID" ]; then
                printf '%serror: private tmux socket belongs to a different server%s\n' \
                    "$DEMO_RED" "$DEMO_RESET" >&2
                return 1
            fi
            if command_output=$(demo_tmux kill-server 2>&1); then
                command_status=0
            else
                command_status=$?
            fi
            if demo_tui_owned_process_is_active "$TUI_TMUX_SERVER_PID" "$TUI_TMUX_SERVER_START"; then
                if [ "$command_status" -ne 0 ]; then
                    printf '%serror: private tmux shutdown failed: %s%s\n' \
                        "$DEMO_RED" "$command_output" "$DEMO_RESET" >&2
                    return 1
                fi
            else
                process_status=$?
                if [ "$process_status" -ne 1 ]; then
                    printf '%serror: private tmux server liveness is unconfirmed%s\n' \
                        "$DEMO_RED" "$DEMO_RESET" >&2
                    return 1
                fi
                if [ "$command_status" -ne 0 ]; then
                    printf 'private tmux server exited during shutdown; reconciling recorded identities (status %s): %s\n' \
                        "$command_status" "$command_output" >&2
                fi
            fi
        else
            command_output=$actual_server_pid
            if demo_tui_owned_process_is_active "$TUI_TMUX_SERVER_PID" "$TUI_TMUX_SERVER_START"; then
                printf '%serror: private tmux server could not be queried: %s%s\n' \
                    "$DEMO_RED" "$command_output" "$DEMO_RESET" >&2
                return 1
            else
                process_status=$?
                if [ "$process_status" -ne 1 ]; then
                    printf '%serror: private tmux server liveness is unconfirmed%s\n' \
                        "$DEMO_RED" "$DEMO_RESET" >&2
                    return 1
                fi
                printf 'private tmux server exited during shutdown; reconciling recorded identities: %s\n' \
                    "$command_output" >&2
            fi
        fi
    else
        process_status=$?
        if [ "$process_status" -ne 1 ]; then
            printf '%serror: private tmux server liveness is unconfirmed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
    fi

    if [ "$TUI_TMUX_SERVER_JOINED" != 1 ]; then
        if wait "$TUI_TMUX_SERVER_PID"; then
            TUI_TMUX_SERVER_EXIT=0
        else
            TUI_TMUX_SERVER_EXIT=$?
        fi
        TUI_TMUX_SERVER_JOINED=1
    fi
    if demo_tui_owned_process_is_active "$TUI_TMUX_SERVER_PID" "$TUI_TMUX_SERVER_START"; then
        printf '%serror: owned tmux server remains live after joining its child%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    else
        process_status=$?
        if [ "$process_status" -ne 1 ]; then
            printf '%serror: private tmux server exit could not be confirmed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
    fi

    if demo_tui_owned_process_is_active "$TUI_TMUX_PANE_PID" "$TUI_TMUX_PANE_START"; then
        printf '%serror: owned TUI pane process is still live%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    else
        pane_status=$?
        if [ "$pane_status" -ne 1 ]; then
            printf '%serror: owned TUI pane process liveness is unconfirmed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
    fi
    if demo_tui_process_group_present "$TUI_TMUX_PANE_PGID"; then
        printf '%serror: owned TUI pane process group is still live%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    else
        group_status=$?
        if [ "$group_status" -ne 1 ]; then
            printf '%serror: owned TUI pane process group liveness is unconfirmed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
    fi

    if [ -e "$TUI_TMUX_SOCKET" ] || [ -L "$TUI_TMUX_SOCKET" ]; then
        if [ ! -S "$TUI_TMUX_SOCKET" ] || [ -L "$TUI_TMUX_SOCKET" ]; then
            printf '%serror: private tmux socket path was replaced; refusing removal%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
        current_socket_identity=$(demo_tui_socket_identity) || {
            printf '%serror: private tmux socket identity could not be read%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        }
        if [ "$current_socket_identity" != "$TUI_TMUX_SOCKET_IDENTITY" ]; then
            printf '%serror: private tmux socket identity changed; refusing removal%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
        demo_tui_socket_has_no_holder || return 1
        current_socket_identity=$(demo_tui_socket_identity) || {
            printf '%serror: private tmux socket changed before removal%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        }
        if [ "$current_socket_identity" != "$TUI_TMUX_SOCKET_IDENTITY" ]; then
            printf '%serror: private tmux socket identity changed before removal%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
        rm "$TUI_TMUX_SOCKET" || return 1
        if [ -e "$TUI_TMUX_SOCKET" ] || [ -L "$TUI_TMUX_SOCKET" ]; then
            printf '%serror: private tmux socket removal is unconfirmed%s\n' \
                "$DEMO_RED" "$DEMO_RESET" >&2
            return 1
        fi
    fi

    if [ "$TUI_TMUX_SERVER_EXIT" != 0 ]; then
        printf 'private tmux server exited with status %s; recorded process and socket checks confirm cleanup\n' \
            "$TUI_TMUX_SERVER_EXIT" >&2
    fi
    demo_tui_write_owner stopped || {
        printf '%serror: could not record confirmed private tmux shutdown%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    }
    owner_identity=$(/usr/bin/stat -f '%d:%i' "$TUI_TMUX_OWNER_FILE") || return 1
    if [ "$owner_identity" != "$TUI_TMUX_OWNER_IDENTITY" ]; then
        printf '%serror: private tmux owner record changed before cleanup%s\n' \
            "$DEMO_RED" "$DEMO_RESET" >&2
        return 1
    fi
    if ! rmdir "$TUI_TMUX_DIR"; then
        printf '%serror: private tmux directory is not empty; retaining it at %s%s\n' \
            "$DEMO_RED" "$TUI_TMUX_DIR" "$DEMO_RESET" >&2
        return 1
    fi
    if [ -z "${DEMO_PRIVATE_TMUX_STATE_FILE:-}" ]; then
        rm "$TUI_TMUX_OWNER_FILE" || return 1
    fi
    demo_tui_state stopped || return 1

    TUI_TMUX_DIR=
    TUI_TMUX_SOCKET=
    TUI_TMUX_OWNER_FILE=
    TUI_TMUX_OWNER_IDENTITY=
    TUI_TMUX_SOCKET_IDENTITY=
    TUI_TMUX_SERVER_PID=
    TUI_TMUX_SERVER_PGID=
    TUI_TMUX_SERVER_STATE=
    TUI_TMUX_SERVER_START=
    TUI_TMUX_SERVER_JOINED=
    TUI_TMUX_SERVER_EXIT=
    TUI_TMUX_PANE_PID=
    TUI_TMUX_PANE_STATE=
    TUI_TMUX_PANE_START=
    TUI_TMUX_PANE_PGID=
}
