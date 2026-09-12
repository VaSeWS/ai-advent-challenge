#!/usr/bin/env bash
# Demo for week-02, день 7: история диалога сохраняется в SQLite и
# восстанавливается после перезапуска агента.
#
# Тот же tmux-подход, что и в day-06-demo.sh (week-02 — полноэкранный
# Bubble Tea TUI, не CLI), но здесь программа запускается дважды на
# одном и том же файле базы (без -db: используется default `agent.db`
# в рабочей директории week-02, ровно как в README) — второй запуск
# должен показать восстановленный transcript сразу после старта.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-07-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY
demo_tui_require

BIN=$(mktemp -t week02-demo-bin)
WORKDIR="$REPO_ROOT/week-02"
cleanup() { demo_tui_stop; rm -f "$BIN"; rm -f "$WORKDIR"/agent.db*; }
trap cleanup EXIT

(cd week-02 && go build -o "$BIN" .) || { echo "error: go build ./week-02 failed" >&2; exit 1; }
rm -f "$WORKDIR"/agent.db*

demo_title "AI Advent Challenge — День 7" "Сохранение контекста: SQLite переживает перезапуск агента"

RUN_CMD=$(printf 'cd %q && exec %q -provider groq' "$WORKDIR" "$BIN")

# --- Первый запуск: заводим факт в истории ---
demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null

(
    demo_tui_type 'Меня зовут Василий. Цель — сдать домашку второй недели.'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

# --- Второй запуск: та же база, эмулируем перезапуск агента ---
demo_tui_start "$RUN_CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null
demo_pause 2.5

(
    demo_tui_type 'Как меня зовут и какова моя цель?'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-02'
