#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/week05-day21.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
DB="$WORK/day21.db"
SNAPSHOT=week-05/corpus/gtnh-20261004.json

demo_title "AI Advent — День 21" "Индексация GT:NH и сравнение чанкинга"
demo_note "Краткая сводка реального снимка: все страницы и revision provenance без повторяющегося текстового манифеста."
go run ./week-05 corpus -report "$SNAPSHOT" > "$WORK/corpus-report.txt"
python3 week-05/video/show-report.py corpus --snapshot "$SNAPSHOT" --cli-output "$WORK/corpus-report.txt"
demo_note "Создаю fixed и structural индексы в отдельной временной базе; рабочая база остаётся нетронутой."
demo_run "go run ./week-05 index -snapshot '$SNAPSHOT' -db '$DB' -size 400 -overlap 80"
demo_note "Build сводка один раз: фактические размеры fixed/structural и полное покрытие источника."
go run ./week-05 index -inspect -db "$DB" > "$WORK/index-summary.txt"
python3 week-05/video/show-report.py index-summary --input "$WORK/index-summary.txt"
QUERY='EU/t для perfect overclock'
demo_note "Fixed: один лучший полный chunk с provenance для запроса; повторяющиеся build summaries не выводятся."
go run ./week-05 index -inspect -db "$DB" -strategy fixed -search "$QUERY" -k 1 > "$WORK/fixed-search.txt"
python3 week-05/video/show-report.py index-search --input "$WORK/fixed-search.txt"
demo_note "Structural: тот же поиск, один лучший полный chunk и provenance для прямого сравнения."
go run ./week-05 index -inspect -db "$DB" -strategy structural -search "$QUERY" -k 1 > "$WORK/structural-search.txt"
python3 week-05/video/show-report.py index-search --input "$WORK/structural-search.txt"
demo_outro "week-05/day-21"
