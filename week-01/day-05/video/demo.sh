#!/usr/bin/env bash
# Demo for week-01/day-05.
#
#   export GROQ_API_KEY=...
#   ./week-01/day-05/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 5" "Топ / средняя / аутсайдер модель — один промпт, одна таблица"

demo_note 'Промпт задаётся аргументом CLI — по умолчанию классическая логическая загадка про братьев и сестру'
demo_run 'go run ./week-01/day-05'

demo_outro 'week-01/day-05'
