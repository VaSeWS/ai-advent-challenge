#!/usr/bin/env bash
# Demo for week-03, день 11: модель памяти агента — раздельные слои
# (краткосрочный диалог, рабочая память задачи, долговременная память) и
# их влияние на ответ.
#
# week-03 — полноэкранный Bubble Tea TUI: команды вводятся в поле сообщения
# через tmux. Результат каждой команды открывается на отдельном экране
# "Command result" (Esc возвращает в чат, PgUp/PgDn листают).
#
#   export GROQ_API_KEY=...
#   ./week-03/video/day-11-demo.sh

set -uo pipefail

TUI_SESSION=demo-tui-d11

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

demo_title "AI Advent Challenge — День 11" "Модель памяти агента: краткосрочная, рабочая и долговременная память"

RUN_CMD=$(printf 'cd %q && exec %q -db %q -provider groq' "$REPO_ROOT/week-03" "$BIN" "$DB")

# Введённая команда открывает экран результата: ждём, даём прочитать, Esc.
# Упавшая команда возвращает свой текст в поле ввода, поэтому C-u в конце.
run_cmd() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause "${2:-2.5}"
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
}

# Вопрос в чат: основной вызов + извлечение памяти.
ask() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 40 2 >/dev/null
    demo_pause "${2:-4}"
    demo_tui_key C-u
}

demo_note "Три слоя памяти: диалог, рабочая память задачи, долговременная память. Что и куда сохранять, выбираем явно."
demo_pause 3.5

# --- Фаза 1: наполняем слои и смотрим на ответ ---
demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

(
    demo_pause 1
    # Долговременная память: профиль (create его не выбирает — select нужен)
    run_cmd '/profile create Краткий | Russian | concise | bullets | Без эмодзи' 2
    run_cmd '/profile select 1' 1
    # Рабочая память: задача
    run_cmd '/task create Выбрать хранилище для заметок | Сравнить варианты | Выписать критерии | Показать сравнение' 3.5
    run_cmd '/task transition approve_plan' 1.5
    # Долговременная память: явный выбор области для каждой записи
    run_cmd '/memory add task | decision | storage | SQLite' 1
    run_cmd '/memory add profile | knowledge | editor | Neovim' 1
    run_cmd '/memory list' 3.5
    # Краткосрочная память + влияние на ответ
    ask 'Напомни: какое решение по хранилищу мы приняли и какой редактор я использую?' 3
    # Что лежит в каждом слое
    demo_tui_type '/inspect Напомни: какое решение по хранилищу мы приняли и какой редактор я использую?'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 3
    demo_tui_key PageDown
    demo_pause 3
    demo_tui_key PageDown
    demo_pause 2
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Новый чат: диалог и рабочая память пусты, долговременная память и профиль остались."
demo_pause 2

# --- Фаза 2: новый чат — что осталось в каждом слое ---
(
    demo_pause 0.8
    run_cmd '/new Пустой чат' 1.5
    run_cmd '/memory list' 3
    run_cmd '/task show' 3
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-03'
