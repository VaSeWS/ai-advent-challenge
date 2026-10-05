#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"
demo_require_env DEEPSEEK_API_KEY
demo_tui_require

REPORT=${WEEK05_CONTROL_REPORT:-week-05/evaluations/deepseek-final-strict-diagnostic/evaluation-20261004T214200.834406000Z.json}
REPORTER=week-05/video/show-report.py
QUESTION='What are the override/default/output priorities in an MV EnderIO central ore-processing line?'
TUI_DRIVER=
cleanup() {
    if [ -n "$TUI_DRIVER" ]; then kill "$TUI_DRIVER" 2>/dev/null || true; fi
    demo_tui_stop
}
trap cleanup EXIT

demo_title "AI Advent — День 23" "Фильтр и переписывание retrieval-запроса"
demo_note "Точный исторический SAVED REPORT: фильтр / rewritten-filtered rows по всем 10 вопросам; это калиброванный threshold 0.60, candidate-K 10, context-K 3 — не сравнение с 0.25 и не новое generation."
python3 "$REPORTER" evaluation --report "$REPORT" --label 'SAVED REPORT · calibrated threshold 0.60' --pair rag-filtered --pair filtered-rewritten --review-json week-05/evaluations/deepseek-final-strict-diagnostic/full-semantic-review.json
demo_note "Новый live эксперимент filtered / rewritten-filtered; inspector показывает оригинальный и переписанный запросы, без replay report-ответов."
demo_tui_start 'go run ./week-05 chat -provider deepseek -experiment -pair filtered-rewritten -candidate-k 10 -context-k 3 -threshold 0.60 -calibration week-05/calibration.json' 150 40
demo_tui_wait_for 'You>' 60 || { echo 'error: chat UI did not become ready' >&2; exit 1; }
(
    demo_tui_type "$QUESTION"
    demo_tui_key Enter
    demo_tui_wait_for 'Complete isolated experiment saved' 240 || { echo 'error: live rewrite experiment did not complete' >&2; exit 1; }
    demo_tui_wait_stable 30 2 || { echo 'error: live rewrite experiment did not settle' >&2; exit 1; }
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_tui_key Tab
    demo_tmux send-keys -t "$TUI_SESSION" -N 80 PageUp
    demo_pause 8
    demo_tui_key C-o
    /bin/sleep 0.5
    demo_tui_wait_stable 30 2 || { echo 'error: retrieval inspector did not settle' >&2; exit 1; }
    demo_pause 8
    demo_tui_key C-c
) &
TUI_DRIVER=$!
demo_tui_watch "$TUI_DRIVER"
wait "$TUI_DRIVER"
demo_outro "week-05/day-23"
