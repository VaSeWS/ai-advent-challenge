#!/usr/bin/env bash
# Demo for week-NN/day-NN. Copy to week-NN/day-NN/video/demo.sh and fill in
# the shots. Everything below the header is the storyboard — one demo_note
# (the narration, there is no audio) followed by the command it explains.
#
#   export GROQ_API_KEY=...
#   ./week-NN/day-NN/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY

demo_title "AI Advent Challenge — День NN" "<что показывает это видео, одной строкой>"

demo_note '<что требует задание — своими словами, одна строка>'
demo_run '<первая команда, показывающая этот функционал>'

demo_note '<что меняется во второй команде и зачем>'
demo_run '<вторая команда>'

demo_outro 'week-NN/day-NN'
