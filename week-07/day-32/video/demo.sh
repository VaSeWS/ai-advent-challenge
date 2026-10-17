#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"

for name in INDEX_RUN REVIEW_RUN DEMO_PR; do
    if [[ -z "${!name:-}" ]]; then
        printf 'Set %s to the confirmed GitHub run/PR identifier before the demo.\n' "$name" >&2
        exit 1
    fi
done
for name in INDEX_RUN REVIEW_RUN DEMO_PR; do
    if [[ ! "${!name}" =~ ^[0-9]+$ ]]; then
        printf '%s must be a numeric GitHub identifier.\n' "$name" >&2
        exit 1
    fi
done
if ! command -v gh >/dev/null 2>&1; then
    printf 'Install and authenticate GitHub CLI (gh) before running this live demo.\n' >&2
    exit 1
fi
presenter="$ROOT/week-07/day-32/video/present.py"

demo_title 'AI Advent Challenge — День 32' 'Реальные GitHub Actions и AI review PR'

demo_note 'Доверенный main-index: provenance запуска, corpus hash и результат reuse читаются из GitHub Actions.'
demo_run 'python3 "$presenter" index "$INDEX_RUN"'
demo_pause 2
clear
demo_note 'Настоящий PR VaSeWS/iRon_fork: заголовок, ссылка и все изменённые файлы из GitHub.'
demo_run 'python3 "$presenter" pr "$DEMO_PR"'
demo_pause 2
clear
demo_note 'Источники, выбранные review CLI, показаны из лога завершённого GitHub Actions run.'
demo_run 'python3 "$presenter" sources "$REVIEW_RUN"'
demo_pause 2
clear
demo_note 'Опубликованный комментарий найден по маркеру и показан целиком; выводятся реальные категории и findings.'
demo_run 'python3 "$presenter" comment "$DEMO_PR"'
demo_outro 'week-07/day-32'
