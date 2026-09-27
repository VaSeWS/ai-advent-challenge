#!/usr/bin/env bash
# Demo for week-04/day-20: let the agent discover and route MCP tools itself.
# Credentials must already be exported in the shell; their values are never shown.

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID

demo_title "AI Advent Challenge — День 20" "Агент сам выбирает инструменты и отправляет план в Telegram"

demo_note 'Просим составить план повторения Floats на 26 сентября 2026 года — агент сам выберет инструменты и отправит результат в Telegram.'
demo_run 'go run ./week-04/day-20 agent Floats 2026-09-26'
demo_note 'Проверяем сохранённый план, который был передан в Telegram.'
demo_run 'cat week-04/day-19/plan.md'
demo_outro 'week-04/day-20'
demo_note 'Открываем Telegram и показываем полученное сообщение бота.'
demo_run 'open -a Telegram'
demo_pause 6
