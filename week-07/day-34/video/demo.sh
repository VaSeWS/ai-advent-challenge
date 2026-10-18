#!/usr/bin/env bash
set -euo pipefail

DAY_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
ROOT=$(cd "$DAY_DIR/../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"

if [ ! -t 0 ] || [ ! -t 1 ]; then
    TERM=xterm
    export TERM
fi

demo_require_env DEEPSEEK_API_KEY

demo_title "AI Advent Challenge — День 34" "Реальные MCP-инструменты и локальное подтверждение docs diff"

demo_note 'Создаём новую чистую копию iRon на закреплённом revision; исходная рабочая папка не меняется.'
demo_run 'IRON_ROOT="$(bash ./week-07/day-34/video/prepare.sh)" && printf "Pinned iRon checkout: %s\\n" "$IRON_ROOT"'

DB=$(mktemp "${TMPDIR:-/tmp}/day34-agent.XXXXXX")
STATUS_FILE=$(mktemp "${TMPDIR:-/tmp}/day34-demo-status.XXXXXX")
if [ -n "${DAY34_DEMO_EVIDENCE_DIR:-}" ]; then
    mkdir -p "$DAY34_DEMO_EVIDENCE_DIR"
    DEMO_EVIDENCE_DIR=$(cd "$DAY34_DEMO_EVIDENCE_DIR" && pwd -P)
else
    DEMO_EVIDENCE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/day34-demo-evidence.XXXXXX")
fi
printf 'Demo screen evidence: %s\n' "$DEMO_EVIDENCE_DIR"
TUI_SESSION=${TUI_SESSION:-"day34-demo-${UID:-user}-$$-${RANDOM}"}
capture_stage() {
    local name=$1
    demo_tmux capture-pane -t "$TUI_SESSION" -p -S - > "$DEMO_EVIDENCE_DIR/$name.txt"
}
cleanup() {
    demo_tui_stop
    rm -f "$DB" "$DB-wal" "$DB-shm" "$STATUS_FILE"
}
trap cleanup EXIT

IRON_ROOT=${IRON_ROOT:?prepare.sh did not set IRON_ROOT}
demo_tui_start "go run ./week-07/day-34 -root '$IRON_ROOT' -db '$DB' -provider deepseek -max-tokens 8192 -tool-rounds 24" 120 34

wait_for_task_terminal() {
    local timeout=$1 start now pane
    start=$(date +%s)
    while true; do
        pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null || true)
        if [[ "$pane" == *'Response received.'* && "$pane" == *'You> Write a message or slash command'* ]]; then
            return 0
        fi
        if [[ "$pane" == *'Request failed.'* ]]; then
            demo_tmux capture-pane -t "$TUI_SESSION" -p -S - 2>/dev/null | tee "$DEMO_EVIDENCE_DIR/request-failed.txt"
            return 1
        fi
        now=$(date +%s)
        if [ $((now - start)) -ge "$timeout" ]; then
            demo_tmux capture-pane -t "$TUI_SESSION" -p -S - 2>/dev/null | tee "$DEMO_EVIDENCE_DIR/request-timeout.txt"
            return 1
        fi
        /bin/sleep 0.2 2>/dev/null || true
    done
}

submit_and_return() {
    local text=$1 stage=$2 expected=$3 pane
    stage=${stage:-${text#/}}
    demo_tui_paste "$text"
    demo_tui_wait_for "You> $text" 15 || return 1
    pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p) || return 1
    if [[ "$pane" != *"You> $text"* ]]; then
        capture_stage "${stage}-input"
        printf 'error: expected actual TUI input "You> %s" before submission\n' "$text" >&2
        return 1
    fi
    capture_stage "${stage}-input"
    demo_tui_key Enter
    demo_tui_wait_for 'Command result' 30 || return 1
    demo_tui_wait_stable 30 2 || return 1
    pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p) || return 1
    if [[ "$pane" != *'Command result'* || "$pane" != *"$expected"* ]]; then
        capture_stage "$stage"
        printf 'error: %s did not produce the expected local command result\n' "$text" >&2
        return 1
    fi
    capture_stage "$stage"
    demo_tui_key PageDown
    demo_tui_wait_stable 5 1 || return 1
    capture_stage "${stage}-page-2"
    demo_tui_key PageDown
    demo_tui_wait_stable 5 1 || return 1
    capture_stage "${stage}-page-3"
    demo_tui_key Escape
    demo_tui_wait_for 'You>' 15
}

submit_task_and_return() {
    local text=$1 stage=$2
    demo_tui_paste "$text"
    demo_tui_wait_stable 5 1 || return 1
    capture_stage "${stage}-input"
    demo_tui_key Enter
    wait_for_task_terminal 240 || return 1
    demo_tui_wait_stable 20 2 || return 1
    capture_stage "$stage"
    demo_tui_key Escape
    demo_tui_wait_for 'You>' 15
}

show_written_docs() {
    local entry path rc count=0 updated=0 created=0
    while IFS= read -r -d '' entry; do
        path=${entry:3}
        count=$((count + 1))
        if git -C "$IRON_ROOT" ls-files --error-unmatch -- "$path" >/dev/null 2>&1; then
            updated=$((updated + 1))
            git -C "$IRON_ROOT" diff HEAD -- "$path" || return $?
        else
            created=$((created + 1))
            rc=0
            git diff --no-index -- /dev/null "$IRON_ROOT/$path" || rc=$?
            [ "$rc" -eq 1 ] || return "$rc"
        fi
    done < <(git -C "$IRON_ROOT" status --porcelain -z --untracked-files=all -- docs memory-bank README.md)
    if [ "$count" -eq 0 ]; then
        printf 'error: no saved documentation changes found under docs/, memory-bank/, or README.md\n' >&2
        return 1
    fi
    if [ "$updated" -eq 0 ] || [ "$created" -eq 0 ]; then
        printf 'error: expected both a new guide and an existing documentation update on disk\n' >&2
        return 1
    fi
}

verify_sources_unchanged() {
    local changes
    changes=$(git -C "$IRON_ROOT" status --porcelain --untracked-files=all -- src tests) || return 1
    if [ -n "$changes" ]; then
        printf 'error: source or test files changed on disk:\n%s\n' "$changes" >&2
        return 1
    fi
}

page_saved_docs_diff() {
    local line count=0
    [ -s "$DEMO_EVIDENCE_DIR/saved-docs.diff" ] || {
        printf 'error: saved documentation diff is empty\n' >&2
        return 1
    }
    while IFS= read -r line || [ -n "$line" ]; do
        printf '%s\n' "$line"
        count=$((count + 1))
        if [ $((count % 30)) -eq 0 ]; then
            demo_pause
        fi
    done < "$DEMO_EVIDENCE_DIR/saved-docs.diff"
}




drive_demo() {
    local pane start now
    phase='waiting for TUI startup'
    demo_tui_wait_for 'Write a message or slash command' 120 || return 1
    capture_stage tui-ready

    phase='investigating Delta implementation, call sites, and tests'
    submit_task_and_return 'Разберись, как Delta overlay выбирает масштаб, где используется эта логика и какие тесты проверяют её границы. Приведи пути и строки.' investigation || return 1

    phase='preparing a focused documentation guide and architecture link'
    submit_task_and_return 'Сделай два изменения: 1) создай новый краткий гайд по масштабированию Delta overlay на основе кода и тестов; 2) обнови существующую архитектурную документацию, добавив ссылку на этот гайд. Подготовь оба изменения через файловые инструменты. Исходники не меняй и не объявляй документацию устаревшей без оснований.' staged-docs || return 1

    phase='reviewing staged documentation diff'
    submit_and_return '/diff' diff 'Staged diff (not saved)' || return 1

    phase='requesting explicit local apply confirmation'
    submit_and_return '/apply' apply 'Для подтверждения введите /confirm' || return 1

    phase='confirming the reviewed diff'
    demo_tui_paste '/confirm'
    demo_tui_wait_for 'You> /confirm' 15 || return 1
    pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p) || return 1
    if [[ "$pane" != *'You> /confirm'* ]]; then
        capture_stage confirm-input
        printf 'error: /confirm was not present in the actual TUI input before submission\n' >&2
        return 1
    fi
    capture_stage confirm-input
    demo_tui_key Enter
    demo_tui_wait_for 'Command result' 30 || return 1
    demo_tui_wait_stable 30 2 || return 1
    pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p) || return 1
    if [[ "$pane" != *'Command result'* || "$pane" != *'Подтверждённые staged-изменения сохранены на диске.'* ]]; then
        capture_stage confirmed
        printf 'error: /confirm did not report that the reviewed changes were saved to disk\n' >&2
        return 1
    fi
    capture_stage confirmed
    demo_tui_key Escape
    demo_tui_wait_for 'You>' 15 || return 1

    phase='verifying the actual saved documentation diff and unchanged sources'
    show_written_docs > "$DEMO_EVIDENCE_DIR/saved-docs.diff" || return 1
    verify_sources_unchanged || return 1

    phase='reading the actual saved documentation through project tools'
    submit_task_and_return 'Теперь проверь уже сохранённый результат реальными инструментами: найди новый или изменённый Delta-гайд среди docs/ и memory-bank/, прочитай его и memory-bank/architecture.md, затем назови точные сохранённые пути и кратко укажи, какая ссылка их связывает. Не записывай новые изменения.' saved-doc-verification || return 1
    verify_sources_unchanged || return 1
    show_written_docs > "$DEMO_EVIDENCE_DIR/saved-docs.diff" || return 1
    verify_sources_unchanged || return 1

    phase='closing the TUI'
    demo_tui_paste '/quit'
    demo_tui_wait_for 'You> /quit' 15 || return 1
    pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p) || return 1
    if [[ "$pane" != *'You> /quit'* ]]; then
        capture_stage quit-input
        printf 'error: /quit was not present in the actual TUI input before submission\n' >&2
        return 1
    fi
    capture_stage quit-input
    demo_tui_key Enter
    start=$(date +%s)
    while true; do
        if ! demo_tmux has-session -t "$TUI_SESSION" 2>/dev/null; then
            return 0
        fi
        now=$(date +%s)
        if [ $((now - start)) -ge 20 ]; then
            capture_stage quit-not-exited
            printf 'error: TUI did not exit after /quit\n' >&2
            return 1
        fi
        /bin/sleep 0.2 2>/dev/null || true
    done
}

(
    set +e
    drive_demo
    rc=$?
    if [ "$rc" -ne 0 ]; then
        printf 'error: day34 demo failed during %s (status %s)\n' "$phase" "$rc" >&2
    fi
    if ! printf '%s\n' "$rc" > "$STATUS_FILE"; then
        printf 'error: could not write TUI driver status file: %s\n' "$STATUS_FILE" >&2
        rc=1
    fi
    demo_tui_detach || true
    exit "$rc"
) &
driver_pid=$!
watch_status=0
demo_tui_watch "$driver_pid" || watch_status=$?
if [ -t 0 ] && [ -t 1 ]; then
    wait "$driver_pid" || watch_status=$?
fi
if [ ! -f "$STATUS_FILE" ]; then
    printf 'error: missing TUI driver status (watch status %s)\n' "$watch_status" >&2
    exit 1
fi
if ! IFS= read -r driver_status < "$STATUS_FILE"; then
    printf 'error: could not read TUI driver status file: %s\n' "$STATUS_FILE" >&2
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
demo_run 'page_saved_docs_diff'

demo_outro 'week-07/day-34'
