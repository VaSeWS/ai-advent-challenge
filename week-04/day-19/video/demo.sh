#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"

if [ ! -t 0 ] || [ ! -t 1 ]; then
    TERM=xterm
    export TERM
fi


TUI_SESSION=${TUI_SESSION:-week04-day19-$$}
demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 19" "MCP-инструменты в TUI-агенте"

db=$(mktemp "${TMPDIR:-/tmp}/week04-day19.XXXXXX")
status_file=$(mktemp "${TMPDIR:-/tmp}/week04-day19-status.XXXXXX")
cleanup() {
    demo_tui_stop
    rm -f "$db" "$status_file"
}
trap cleanup EXIT

wait_for() {
    local pattern=$1 timeout=${2:-90}
    demo_tui_wait_for "$pattern" "$timeout" || {
        printf 'error: TUI did not show expected result: %s\n' "$pattern" >&2
        return 1
    }
}


type_text() {
    demo_tui_type "$1" || true
}

submit_command() {
    local command=$1 result=$2
    type_text "$command"
    demo_tui_key Enter
    wait_for "$result" 120 || return 1
    demo_tui_key Escape
    wait_for 'You>' 10 || return 1
}

export GROQ_IPV4=1
demo_tui_start "go run ./week-04/agent -db '$db' -provider groq" 120 34

drive_demo() {
    phase='waiting for TUI startup'
    response_kind=''
    wait_for 'Write a message or slash command' 120 || return 1
    phase='adding and connecting day19'
    submit_command '/mcp add day19 --cwd . -- go run ./week-04/day-19 server' 'added MCP server "day19"' || return 1
    submit_command '/mcp connect day19' 'connected MCP server "day19"' || return 1
    phase='discovering day19 tools'
    type_text '/mcp list'
    demo_tui_key Enter
    wait_for 'find_due_questions' 10 || return 1
    wait_for 'build_review_plan' 10 || return 1
    wait_for 'save_plan' 10 || return 1
    demo_tui_key Escape
    wait_for 'You>' 10 || return 1

    type_text 'Составь план повторения темы #Architectural на 2026-09-26, сохрани его вне Obsidian и назови путь к файлу.'
    phase='requesting and saving the review plan'
    demo_tui_key Enter
    wait_for 'find_due_questions (day19): completed' 180 || return 1
    wait_for 'build_review_plan (day19): completed' 180 || return 1
    wait_for 'save_plan (day19): completed' 180 || return 1
    wait_for 'ASSISTANT:' 30 || return 1
    demo_tui_wait_stable 20 2 || {
        printf 'error: day 19 TUI did not settle after the assistant status\n' >&2
        return 1
    }
    test -s week-04/day-19/plan.md || {
        printf 'error: expected saved plan is missing or empty: week-04/day-19/plan.md\n' >&2
        return 1
    }
    local transcript
    transcript=$(tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null) || return 1
    if [[ "$transcript" == *'week-04/day-19/plan.md'* ]]; then
        response_kind=path
    elif [[ "$transcript" == *'The review plan was saved.'* ]]; then
        response_kind=fallback
    else
        printf 'error: assistant showed neither the saved path nor a confirmed-save status\n' >&2
        return 1
    fi
    phase=complete
    return 0
}


(
    set +e
    drive_demo
    rc=$?
    if [ "$rc" -ne 0 ]; then
        printf 'error: day 19 demo failed during %s (status %s)\n' "$phase" "$rc" >&2
    fi
    if ! printf '%s|%s\n' "$rc" "$response_kind" > "$status_file"; then
        printf 'error: could not write TUI driver status file: %s\n' "$status_file" >&2
        rc=1
    fi
    demo_tui_detach || true
    exit "$rc"
) &
watch_status=0
demo_tui_watch || watch_status=$?
if [ ! -f "$status_file" ]; then
    printf 'error: missing TUI driver status (watch status %s)\n' "$watch_status" >&2
    exit 1
fi
if ! IFS='|' read -r driver_status response_kind < "$status_file"; then
    printf 'error: could not read TUI driver status file: %s\n' "$status_file" >&2
    exit 1
fi
case "$driver_status" in
    0) ;;
    ''|*[!0-9]*)
        printf 'error: invalid TUI driver status: %s\n' "$driver_status" >&2
        exit 1
        ;;
    *)
        printf 'error: TUI demo driver failed with status %s\n' "$driver_status" >&2
        exit "$driver_status"
        ;;
esac
if [ "$response_kind" = fallback ]; then
    demo_note 'Путь подтверждён сохранённым файлом.'
    demo_run 'ls -l week-04/day-19/plan.md'
fi
exit 0
