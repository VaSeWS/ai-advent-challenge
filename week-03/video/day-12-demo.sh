#!/usr/bin/env bash
# Demo for week-03, день 12: персонализация ассистента — два профиля,
# один и тот же вопрос, разный стиль ответа; профиль подмешивается в
# каждый запрос автоматически (видно через /inspect).
#
#   export GROQ_API_KEY=...
#   ./week-03/video/day-12-demo.sh

set -uo pipefail

TUI_SESSION=demo-tui-d12

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

demo_title "AI Advent Challenge — День 12" "Персонализация ассистента: профиль пользователя в каждом запросе"

RUN_CMD=$(printf 'cd %q && exec %q -db %q -provider groq' "$REPO_ROOT/week-03" "$BIN" "$DB")
Q='Что такое SQLite? Не больше 60 слов.'

# Команда открывает экран результата: ждём, даём прочитать, Esc, чистим поле
# (после неудачной команды её текст возвращается в поле ввода).
run_cmd() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause "${2:-2}"
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
}

# Обычное сообщение: ждём ответ модели и даём его прочитать.
ask() {
    demo_tui_type "$1"
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    demo_pause "${2:-5}"
    demo_tui_key C-u
}

demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null || { echo 'error: TUI did not start' >&2; exit 1; }

# --- Фаза 1: два профиля, выбран «Краткий» ---
(
    demo_pause 0.8
    demo_pause 1.5
    run_cmd '/profile create Краткий | Russian | concise | bullets | Без эмодзи, не более 4 пунктов' 1.5
    run_cmd '/profile create Подробный | Russian | detailed | prose | Приводи источники в конце' 1.5
    run_cmd '/profile select 1' 1.5
    run_cmd '/profile show' 3
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Профиль «Краткий»: concise / bullets. Задаём вопрос."
demo_pause 2

# --- Фаза 2: ответ под профилем «Краткий» ---
(
    demo_pause 0.8
    ask "$Q" 6
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Тот же вопрос под профилем «Подробный»: detailed / prose / со ссылками."
demo_pause 2

# --- Фаза 3: ответ под профилем «Подробный» (новый чат, профиль сохраняется) ---
(
    demo_pause 0.8
    run_cmd '/new Подробный ответ' 1.5
    run_cmd '/profile select 2' 1.5
    ask "$Q" 6
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note "Профиль подключается к каждому запросу автоматически. Смотрим запрос к модели."
demo_pause 2

# --- Фаза 4: /inspect в том же чате, что дал последний ответ ---
(
    demo_pause 0.8
    demo_tui_type "/inspect $Q"
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 6
    demo_tui_key PageDown
    demo_pause 4
    demo_tui_key Escape
    demo_pause 1
    demo_tui_key C-u
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-03'
