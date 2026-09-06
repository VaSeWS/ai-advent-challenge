#!/usr/bin/env bash
# Demo for week-01/day-03: one task, four reasoning methods (direct, step,
# meta, panel), the same model call chain, four different answers.
#
#   export GROQ_API_KEY=...
#   ./week-01/day-03/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 3" "Одна задача, четыре способа рассуждения"

demo_note 'Одна и та же задача для всех четырёх способов.'
demo_run 'TASK="В семье у Маши есть три брата и две сестры. Сколько братьев и сколько сестёр у Ивана, одного из братьев Маши? В конце ответа явно укажите итог в формате: Ответ: <число> братьев, <число> сестёр"'

demo_note 'Способ 1 — прямой ответ, без system prompt.'
demo_run 'go run ./week-01/day-03 -method=direct "$TASK"'

demo_note 'Способ 2 — «решай пошагово», рассуждение видно целиком.'
demo_run 'go run ./week-01/day-03 -method=step "$TASK"'

demo_note 'Способ 3 — модель сама составляет промпт для задачи, затем решает по нему.'
demo_run 'go run ./week-01/day-03 -method=meta "$TASK"'

demo_note 'Способ 4 — панель из трёх экспертов: аналитик, инженер, критик.'
demo_run 'go run ./week-01/day-03 -method=panel "$TASK"'

demo_outro 'week-01/day-03'
