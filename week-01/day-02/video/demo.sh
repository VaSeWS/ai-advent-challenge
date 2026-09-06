#!/usr/bin/env bash
# Demo for week-01/day-02: один prompt уходит в Groq дважды — без ограничений
# и с ограничениями (строгий JSON-схема, лимит длины, stop sequence).
#
#   export GROQ_API_KEY=...
#   ./week-01/day-02/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 2" "Один запрос: без ограничений vs с ограничениями"

demo_note 'Без ограничений vs строгий JSON-ответ (ровно 3 буллета)'
demo_run 'go run ./week-01/day-02 "Расскажи про пользу утренней зарядки"'

demo_note 'Stop sequence реально обрывает генерацию даже внутри JSON — тут "2." ловит модель на полуслове'
demo_run 'go run ./week-01/day-02 -stop "2." "Перечисли нумерованным списком (1. 2. 3. 4. ...) занятия для бодрого утра"'

demo_note 'Ограничение длины: жёсткий -max-tokens делает буллеты короче'
demo_run 'go run ./week-01/day-02 -max-tokens 350 -reasoning off "Что такое рекурсия?"'

demo_outro 'week-01/day-02'
