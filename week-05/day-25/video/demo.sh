#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"
demo_require_env DEEPSEEK_API_KEY

OUT=$(mktemp -d "${TMPDIR:-/tmp}/week05-day25.XXXXXX")
trap 'rm -rf "$OUT"' EXIT
REPORTER=week-05/video/show-report.py
mkdir -p "$OUT/reports"

demo_title "AI Advent — День 25" "Два RAG-диалога с памятью и checkpoint/resume"
demo_note "Создаю progression-сессию: 10 живых ходов и сохранённый checkpoint."
go run ./week-05 evaluate -scenario progression -provider deepseek -until 10 -interval 1s -temperature 0 -max-tokens 2048 -out "$OUT/reports" | tee "$OUT/progression-checkpoint.log"
PROGRESSION_SESSION=$(python3 "$REPORTER" checkpoint "$OUT/progression-checkpoint.log")
demo_note "Отдельный новый процесс продолжает ту же progression session с хода 11 до 12; предыдущие ответы не запрашиваются повторно."
go run ./week-05 evaluate -scenario progression -session "$PROGRESSION_SESSION" -until 12 -provider deepseek -interval 1s -temperature 0 -max-tokens 2048 -out "$OUT/reports"
demo_note "Создаю независимую ore-processing session и её собственный turn-10 checkpoint."
go run ./week-05 evaluate -scenario ore-processing -provider deepseek -until 10 -interval 1s -temperature 0 -max-tokens 2048 -out "$OUT/reports" | tee "$OUT/ore-checkpoint.log"
ORE_SESSION=$(python3 "$REPORTER" checkpoint "$OUT/ore-checkpoint.log")
demo_note "Новый процесс возобновляет именно ore-processing session и выполняет ходы 11–12."
go run ./week-05 evaluate -scenario ore-processing -session "$ORE_SESSION" -until 12 -provider deepseek -interval 1s -temperature 0 -max-tokens 2048 -out "$OUT/reports"
shopt -s nullglob
REPORTS=("$OUT/reports"/scenario-*.json)
demo_note "Инспектирую ровно четыре свежих JSON этого запуска; каждая страница показывает полный ответ, цитаты, источники и task state."
python3 "$REPORTER" scenarios "${REPORTS[@]}" --label 'LIVE RUN REPORTS · generated above; not replayed'
HIST_PROGRESSION=${WEEK05_PROGRESSION_REPORT:-week-05/scenario-reports/final-strict/scenario-progression-20261004T214412.361800000Z.json}
HIST_ORE=${WEEK05_ORE_REPORT:-week-05/scenario-reports/final-strict/scenario-ore-processing-20261004T214606.714306000Z.json}
HIST_REVIEW=${WEEK05_SCENARIO_REVIEW:-week-05/scenario-reports/final-strict/semantic-review.json}
demo_note "Отдельный SAVED REPORT review предыдущих сценариев: exact report paths/hashes и agent-authored замечания проверяются независимо от выполненной live-сессии выше."
python3 "$REPORTER" scenarios "$HIST_PROGRESSION" "$HIST_ORE" --review-json "$HIST_REVIEW" --label 'SAVED REPORT REVIEW · historical, not live output'
demo_outro "week-05/day-25"
