#!/usr/bin/env bash
# Demo for week-02, день 6: агент — отдельная сущность, которая принимает
# запрос пользователя, вызывает LLM через API и показывает ответ в
# полноэкранном терминальном интерфейсе.
#
# week-02 — не CLI с одноразовыми командами, а Bubble Tea TUI: чтобы
# показать живой диалог, программа запускается в detached tmux-сессии,
# а keystrokes шлются в неё снаружи через `tmux send-keys` (см.
# demo_tui_* в demolib.sh). Во время реальной записи Terminal.app
# делает `tmux attach` к этой же сессии — это и попадает в кадр.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-06-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY
demo_tui_require

BIN=$(mktemp -t week02-demo-bin)
DBDIR=$(mktemp -d -t week02-demo-db)
DB="$DBDIR/agent.db"
cleanup() { demo_tui_stop; rm -f "$BIN"; rm -rf "$DBDIR"; }
trap cleanup EXIT

(cd week-02 && go build -o "$BIN" .) || { echo "error: go build ./week-02 failed" >&2; exit 1; }

demo_title "AI Advent Challenge — День 6" "Первый агент: запрос пользователя → LLM через API → ответ в интерфейсе"

CMD=$(printf '%q -db %q -provider groq' "$BIN" "$DB")
demo_tui_start "$CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null

(
    demo_tui_type 'Привет! Объясни одним предложением, что такое AI-агент.'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4

    demo_tui_type 'Предложи одно применение такого агента в реальном проекте.'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4

    demo_tui_type '/quit'
    demo_tui_key Enter
) &

demo_tui_watch
wait

demo_outro 'week-02'
