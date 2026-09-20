#!/usr/bin/env bash
# Demo for week-03, день 15: контролируемые переходы состояний задачи —
# нельзя перепрыгнуть этап, пауза блокирует переходы, финал только через
# валидацию.
#
#   export GROQ_API_KEY=...
#   ./week-03/video/day-15-demo.sh

set -uo pipefail

TUI_SESSION=demo-tui-d15

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY
demo_tui_require

BIN=$(mktemp -t week03-demo-bin)
DBDIR=$(mktemp -d -t week03-demo-db)
DB="$DBDIR/week-03.db"
cleanup() { demo_tui_stop; rm -f "$BIN"; rm -rf "$DBDIR"; }
trap cleanup EXIT

(cd week-03 && go build -o "$BIN" .) || { echo "error: go build ./week-03 failed" >&2; exit 1; }

demo_title "AI Advent Challenge — День 15" "Контролируемые переходы состояний задачи"

RUN_CMD=$(printf 'cd %q && exec %q -db %q -provider groq' "$REPO_ROOT/week-03" "$BIN" "$DB")

# Slash-команда: дождаться вывода, дать прочитать, закрыть экран результата
# и очистить поле ввода (упавшая команда возвращает свой текст в поле).
# usage: send_cmd <command> <seconds-to-read>
send_cmd() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause "$2"
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
}

demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

# --- Фаза 1: задача в planning, попытка перепрыгнуть этап ---
(
    demo_pause 0.8
    send_cmd '/task create Реализовать экспорт | Схема и код экспорта | Согласовать план | Запросить approve_plan' 3.5
    send_cmd '/task transition submit_result' 2.5
    send_cmd '/task show' 3.5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Допустимые переходы: approve_plan, submit_result, validation_passed, validation_failed — по одному на этап."
demo_pause 3

# --- Фаза 2: просьба пропустить план в чате, затем пауза ---
(
    demo_pause 0.8
    demo_tui_type 'Можно ли сразу перейти к реализации, не утверждая план? Ответь коротко.'
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    demo_pause 3
    demo_tui_key C-u
    send_cmd '/task pause' 1
    send_cmd '/task transition approve_plan' 2
    send_cmd '/task resume' 1
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Пауза блокирует переходы; после resume задача продолжается с той же фазы."
demo_pause 3

# --- Фаза 3: допустимый путь, финал только через валидацию ---
(
    demo_pause 0.8
    send_cmd '/task transition approve_plan' 2
    send_cmd '/task transition validation_passed' 2
    send_cmd '/task transition submit_result' 2
    send_cmd '/task transition validation_passed' 2
    send_cmd '/task show' 2
    send_cmd '/task history' 5
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-03'
