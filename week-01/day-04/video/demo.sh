#!/usr/bin/env bash
# Demo for week-01/day-04.
#
#   export GROQ_API_KEY=...
#   ./week-01/day-04/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День 4" "Один вопрос, три температуры"

demo_note 'Один и тот же вопрос отправляется при temperature=0 / 0.7 / 1.2, ответы выводятся рядом в одной таблице'
demo_run 'go run ./week-01/day-04 "Объясни, что такое TCP handshake, и придумай для него метафору"'

demo_outro 'week-01/day-04'
