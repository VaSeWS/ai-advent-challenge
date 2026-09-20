#!/usr/bin/env bash
# Demo for week-03, день 13: состояние задачи как конечный автомат
# (stage, current step, expected action), пауза на стадии execution и
# продолжение после перезапуска без повторного объяснения цели.
#
# week-03 — полноэкранный Bubble Tea TUI: команды вводятся в поле сообщения
# через tmux. Результат каждой команды открывается на отдельном экране
# "Command result" (Esc возвращает в чат, PgUp/PgDn листают). Программа
# запускается дважды на одной и той же временной базе.
#
#   export GROQ_API_KEY=...
#   ./week-03/video/day-13-demo.sh

set -uo pipefail

TUI_SESSION=demo-tui-d13

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

demo_title "AI Advent Challenge — День 13" "Состояние задачи: planning → execution → validation → done"

RUN_CMD=$(printf 'cd %q && exec %q -db %q -provider groq' "$REPO_ROOT/week-03" "$BIN" "$DB")

# Введённая команда открывает экран результата: ждём, даём прочитать, Esc.
run_cmd() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause "${2:-2.5}"
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
}

# --- Первый запуск: задача, переход в execution, пауза ---
demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

(
    demo_pause 0.8
    demo_pause 1.5
    run_cmd '/task create Написать README для утилиты | Структура: установка, запуск, примеры | Согласовать план | Выполнить approve_plan' 3.5
    run_cmd '/task transition approve_plan' 2.5
    run_cmd '/task update step Написать раздел Установка' 2
    run_cmd '/task update action Показать черновик раздела' 2
    run_cmd '/task pause' 2.5
    run_cmd '/task show' 4
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_note "Перезапуск: диалога нет, состояние задачи хранится отдельно."
demo_pause 2

# --- Второй запуск: состояние восстановлено, продолжаем без объяснений ---
demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

(
    demo_pause 0.8
    demo_pause 2
    run_cmd '/task show' 4
    run_cmd '/task resume' 2.5

    # Что уходит в модель: снимок задачи уже в prompt
    demo_tui_type '/inspect Продолжай'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 5
    demo_tui_key PageDown
    demo_pause 4
    demo_tui_key Escape
    demo_pause 1

    # Продолжение без повторного объяснения цели
    demo_tui_type 'Продолжай. Кратко, 3-4 пункта.'
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    demo_pause 6
    demo_tui_key C-u
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-03'
