#!/usr/bin/env bash
# Demo for week-03, день 14: инварианты и ограничения состояния —
# ассистент не нарушает закреплённые правила и объясняет отказ.
#
# week-03 — полноэкранный Bubble Tea TUI: команды вводятся в поле сообщения
# через tmux. Результат команды открывается на экране "Command result"
# (Esc возвращает в чат, PgUp/PgDn листают).
#
#   export GROQ_API_KEY=...
#   ./week-03/video/day-14-demo.sh

set -uo pipefail

TUI_SESSION=demo-tui-d14

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

demo_title "AI Advent Challenge — День 14" "Инварианты: ассистент отказывается нарушать закреплённые правила"

RUN_CMD=$(printf 'cd %q && exec %q -db %q -provider groq' "$REPO_ROOT/week-03" "$BIN" "$DB")

# Введённая команда открывает экран результата: ждём, даём прочитать, Esc.
# Неудавшаяся команда возвращает свой текст в поле ввода, поэтому C-u.
run_cmd() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause "${2:-2.5}"
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
}

demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

# --- Фаза 1: задача и инварианты ---
(
    demo_pause 1.5
    run_cmd '/task create Выбрать хранилище | Использовать SQLite | Зафиксировать решение | Не менять storage' 2
    run_cmd '/invariant add task | plan | Использовать SQLite | План обязан сохранять SQLite. | architecture' 2
    run_cmd '/invariant add decision | storage | SQLite | Хранилище только SQLite. | architecture | PostgreSQL, MySQL' 2
    run_cmd '/invariant list' 3
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Invariants хранятся отдельно от диалога: хранилище только SQLite; PostgreSQL и MySQL запрещены."
demo_pause 3

# --- Фаза 2: структурированный конфликт, план не меняется ---
(
    demo_pause 0.8
    run_cmd '/task update plan Перейти на PostgreSQL' 3.5
    run_cmd '/task show' 3.5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Конфликт с планом отклонён, план прежний. Снимаем инвариант плана — правило про хранилище и запрещённые термины остаётся."
demo_pause 3.5

# --- Фаза 3: отказ в чате, допустимый вопрос, inspect ---
(
    demo_pause 0.8
    run_cmd '/invariant deactivate 1' 2

    # Детерминированный отказ без вызова LLM
    demo_tui_type 'Предложи PostgreSQL вместо SQLite'
    demo_tui_key Enter
    demo_tui_wait_stable 20 1 >/dev/null
    demo_pause 4

    # Допустимый запрос: обычный ответ в рамках ограничения
    demo_tui_type 'Как ускорить запросы в SQLite? Кратко, 3 пункта.'
    demo_tui_key Enter
    demo_tui_wait_stable 40 2 >/dev/null
    demo_pause 5
    demo_tui_key C-u

    # Почему отказ: сработавший rail и гипотетический запрос
    demo_tui_type '/inspect Предложи PostgreSQL вместо SQLite'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 5
    demo_tui_key PageDown
    demo_pause 4
    demo_tui_key Escape
    demo_pause 1
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-03'
