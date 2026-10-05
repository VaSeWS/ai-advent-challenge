#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"
demo_require_env DEEPSEEK_API_KEY
demo_tui_require

REPORT=${WEEK05_CONTROL_REPORT:-week-05/evaluations/deepseek-final-strict-diagnostic/evaluation-20261004T214200.834406000Z.json}
REPORTER=week-05/video/show-report.py
TUI_DRIVER=
cleanup() {
    if [ -n "$TUI_DRIVER" ]; then kill "$TUI_DRIVER" 2>/dev/null || true; fi
    demo_tui_stop
}
trap cleanup EXIT

demo_title "AI Advent — День 22" "No-RAG и RAG: контрольный корпус"
demo_note "Исторический SAVED REPORT, не новый live evaluation: все 10 ожиданий; показываю baseline-rag и fixed-structural; статус, модель, время и настройки взяты из exact JSON."
python3 "$REPORTER" evaluation --report "$REPORT" --label 'SAVED REPORT · historical, not live generation' --pair baseline-rag --pair fixed-structural --review-json week-05/evaluations/deepseek-final-strict-diagnostic/full-semantic-review.json
demo_note "Отдельная свежая live TUI пара no-RAG / RAG; это независимый запрос, не переигрывание сохранённых ответов."
demo_tui_start 'go run ./week-05 chat -provider deepseek -experiment -pair baseline-rag' 150 40
demo_tui_wait_for 'You>' 60 || { echo 'error: chat UI did not become ready' >&2; exit 1; }
(
    demo_tui_type 'How do regular and perfect overclocks differ in recipe speed, EU/t and total energy?'
    demo_tui_key Enter
    demo_tui_wait_for 'Complete isolated experiment saved' 240 || { echo 'error: live experiment did not complete' >&2; exit 1; }
    demo_tui_wait_stable 30 2 || { echo 'error: live experiment did not settle' >&2; exit 1; }
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_tui_key Tab
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_pause 8
    demo_tui_key C-o
    /bin/sleep 0.5
    demo_tui_wait_stable 30 2 || { echo 'error: context inspector did not settle' >&2; exit 1; }
    demo_pause 8
    demo_tui_key Escape
    /bin/sleep 0.4
    demo_pause 8
    demo_tui_key C-c
) &
TUI_DRIVER=$!
demo_tui_watch "$TUI_DRIVER"
wait "$TUI_DRIVER"
demo_outro "week-05/day-22"
