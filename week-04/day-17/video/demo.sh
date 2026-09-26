#!/usr/bin/env bash
# The source-day-16.mp4 file is a copy of the approved day-16 recording.
set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"
demo_require_env GROQ_API_KEY YANDEX_DISK_TOKEN

demo_title "AI Advent Challenge — День 17" "Агент вызывает собственный MCP Яндекс Диска"

demo_note 'MCP-сервер регистрирует инструмент и описывает его параметры.'
demo_run 'go run ./week-04/day-17 list'

demo_note 'Агент публикует запись дня 16 и использует ссылку из ответа инструмента.'
demo_run 'go run ./week-04/day-17 agent week-04/day-17/video/source-day-16.mp4'

demo_outro 'week-04/day-17'
