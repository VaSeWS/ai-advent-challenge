#!/usr/bin/env bash
# Local MCP connection and live tool discovery.
set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_title "AI Advent Challenge — День 16" "Локальный MCP: подключение и список инструментов"

demo_note 'Официальный Go SDK для MCP установлен в проекте.'
demo_run 'go list -m github.com/modelcontextprotocol/go-sdk'

demo_note 'Клиент подключается к локальному серверу и запрашивает tools/list.'
demo_run 'go run ./week-04/day-16'

demo_note 'Вызываем найденный инструмент: текущая погода в Москве.'
demo_run 'go run ./week-04/day-16 -weather'

demo_outro 'week-04/day-16'
