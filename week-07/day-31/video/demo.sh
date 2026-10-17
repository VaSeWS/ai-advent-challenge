#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"
if [ ! -t 0 ] || [ ! -t 1 ]; then
    export TERM=xterm
fi

PROJECT=${FOODGRAM_PROJECT:-"$ROOT/../foodgram-project-react"}
if [ ! -d "$PROJECT" ]; then
    printf 'Foodgram checkout is unavailable: %s (set FOODGRAM_PROJECT)\n' "$PROJECT" >&2
    exit 1
fi
if [ -z "${GROQ_API_KEY:-}" ]; then
    printf 'Export GROQ_API_KEY in the environment before running the live demo.\n' >&2
    exit 1
fi
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/day31-foodgram.XXXXXX")
cleanup() { rm -rf "$build_dir"; }
trap cleanup EXIT
binary="$build_dir/day31"
go build -o "$binary" ./week-07/day-31
MODEL=${FOODGRAM_MODEL:-qwen/qwen3.8-27b}
foodgram() {
    if [[ "${1:-}" == /help ]]; then
        python3 -c 'import os, sys, time
path = sys.argv[1]
now = time.monotonic()
try:
    with open(path, encoding="ascii") as stamp:
        previous = float(stamp.read())
except FileNotFoundError:
    previous = None
if previous is not None:
    delay = 35 - (now - previous)
    if delay > 0:
        time.sleep(delay)
with open(path, "w", encoding="ascii") as stamp:
    stamp.write(str(time.monotonic()))' "$build_dir/request-start"
    fi
    "$binary" -project "$PROJECT" -model "$MODEL" "$@"
}
presenter="$ROOT/week-07/day-31/video/present.py"

demo_title 'AI Advent Challenge — День 31' 'Foodgram: grounded developer assistant'
clear
demo_note "Проект: $PROJECT | Модель: $MODEL. Реальные ответы Groq; ключ API не показывается."
demo_note 'Для учёта бесплатной квоты между запросами /help выдерживается интервал не менее 35 секунд. CLI вне демо остаётся fail-fast.'
demo_pause 2
clear
demo_note 'Диагностика: состав документационного корпуса и живое подключение MCP. Ключ API не выводится.'
demo_run 'foodgram -inspect | python3 "$presenter"'
demo_pause 2
clear
demo_note 'Документация проекта: структура приложения с точными источниками и диапазонами строк.'
demo_run 'foodgram /help "Как устроена структура проекта?" | python3 "$presenter"'
demo_pause 2
clear
demo_note 'Модель данных: где задаются количество и единица измерения ингредиента? Ответ опирается на документацию.'
demo_run 'foodgram /help "Где задаются количество и единица измерения ингредиента?" | python3 "$presenter"'
demo_pause 2
clear
demo_note 'API: что известно о GET /api/recipes/download_shopping_cart/ и аутентификации? Документация не является runtime-проверкой.'
demo_run 'foodgram /help "Какая аутентификация нужна для GET /api/recipes/download_shopping_cart/?" | python3 "$presenter"'
demo_pause 2
clear
demo_note 'Git-контекст получен через MCP вживую; это ветка Foodgram, не ветка домашнего репозитория.'
demo_run 'foodgram /help "На какой ветке находится checkout Foodgram?" | python3 "$presenter"'
demo_pause 2
demo_outro 'week-07/day-31'
