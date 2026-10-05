#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"
demo_require_env DEEPSEEK_API_KEY
demo_tui_require

REPORT=${WEEK05_CONTROL_REPORT:-week-05/evaluations/deepseek-final-strict-diagnostic/evaluation-20261004T214200.834406000Z.json}
REVIEW=${WEEK05_CONTROL_REVIEW:-week-05/evaluations/deepseek-final-strict-diagnostic/semantic-review.json}
REPORTER=week-05/video/show-report.py
TUI_DRIVER=
cleanup() {
    if [ -n "$TUI_DRIVER" ]; then kill "$TUI_DRIVER" 2>/dev/null || true; fi
    demo_tui_stop
}
trap cleanup EXIT

demo_title "AI Advent — День 24" "Цитаты, provenance и grounded refusal"
demo_note "SAVED REPORT с agent-authored semantic review, привязанной проверкой exact path/SHA-256; все 10 grounded rows, ответы, цитаты и source/exact-quote checks. Это не human review и не live generation."
python3 "$REPORTER" evaluation --report "$REPORT" --review-json "$REVIEW" --label 'SAVED REPORT · agent-reviewed historical control' --pair rag-grounded
demo_note "Отдельное live experimental сравнение RAG / grounded на вопросе вне корпуса; состояние интерфейса показывает только этот новый запрос."
demo_tui_start 'go run ./week-05 chat -provider deepseek -experiment -pair rag-grounded -strategy structural' 150 40
demo_tui_wait_for 'You>' 60 || { echo 'error: chat UI did not become ready' >&2; exit 1; }
(
    demo_tui_type 'What will the weather be in London tomorrow?'
    demo_tui_key Enter
    demo_tui_wait_for 'Complete isolated experiment saved' 240 || { echo 'error: live grounded experiment did not complete' >&2; exit 1; }
    demo_tui_wait_stable 30 2 || { echo 'error: live grounded experiment did not settle' >&2; exit 1; }
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_tui_key Tab
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_pause 8
    demo_tui_key C-o
    /bin/sleep 0.5
    demo_tui_wait_stable 30 2 || { echo 'error: citation/context inspector did not settle' >&2; exit 1; }
    demo_pause 8
    demo_tui_key C-c
) &
TUI_DRIVER=$!
demo_tui_watch "$TUI_DRIVER"
wait "$TUI_DRIVER"
demo_outro "week-05/day-24"
