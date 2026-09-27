#!/usr/bin/env bash
set -uo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/../../../.claude/skills/record-task-demo/scripts/demolib.sh"

ROOT=$(demo_repo_root)
cd "$ROOT"

demo_title "AI Advent — День 19" "MCP-план повторения по темам"

demo_note 'Собираю план по Floats на выбранную дату: три этапа MCP.'
demo_run 'go run ./week-04/day-19 plan Floats 2026-09-26'
demo_pause 3

demo_note 'Теперь фильтрую по DB — состав плана меняется.'
demo_run 'go run ./week-04/day-19 plan DB 2026-09-26'
demo_pause 3

demo_note 'Открываю сохранённый результат планирования.'
demo_run 'cat week-04/day-19/plan.md'
demo_pause 2

demo_outro 'week-04/day-19'
