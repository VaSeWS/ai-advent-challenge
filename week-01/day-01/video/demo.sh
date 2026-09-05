#!/usr/bin/env bash
# Demo for week-01/day-01: a prompt goes to the LLM over the API, the answer
# comes back in the console.
#
#   export GROQ_API_KEY=...
#   ./week-01/day-01/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 1" "Запрос в LLM через API, ответ в консоли"

demo_note 'Задание: отправить запрос в LLM через API и получить ответ в консоли.'
demo_run 'go run ./week-01/day-01 "Объясни одним предложением, что такое API."'

demo_note 'Prompt — обычный аргумент командной строки, ответ приходит живой.'
demo_run 'go run ./week-01/day-01 Назови три языка программирования списком, без пояснений.'

demo_note 'Ещё один запрос — модель отвечает по существу.'
demo_run 'go run ./week-01/day-01 "Сколько будет 17 умножить на 23? Ответь одним числом."'

demo_outro 'week-01/day-01'
