#!/usr/bin/env bash
# Self-playing terminal demo for AI Advent day 1.
# Start the screen recorder, then run this script. It types every command by
# itself and runs the real code — nothing is faked except the masked key line.
#
#   export GROQ_API_KEY=...
#   ./week-01/day-01/video/demo.sh
#
# Tunables: SPEED (per-character delay), PAUSE (beat between steps).

set -uo pipefail

SPEED=${SPEED:-0.025}
PAUSE=${PAUSE:-1.0}
PROMPT_LABEL=${PROMPT_LABEL:-'~/DevProjects/ai-advent-challenge'}

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "$REPO_ROOT" || exit 1

BOLD=$'\033[1m'; DIM=$'\033[2m'; RESET=$'\033[0m'
GREEN=$'\033[32m'; BLUE=$'\033[34m'; RED=$'\033[31m'; CYAN=$'\033[36m'

nap() { [ "$1" != "0" ] && sleep "$1"; }

# Type a string one character at a time, then newline.
typewrite() {
    local text=$1 i
    for ((i = 0; i < ${#text}; i++)); do
        printf '%s' "${text:i:1}"
        nap "$SPEED"
    done
    printf '\n'
}

prompt() { printf '%s%s%s %s$%s ' "$BLUE" "$PROMPT_LABEL" "$RESET" "$GREEN" "$RESET"; }

# Type a command at a shell prompt, then run it.
run() {
    prompt
    typewrite "$1"
    nap 0.3
    eval "$1"
    nap "$PAUSE"
}

# Type one command but run another — used so the real key never hits the screen.
run_as() {
    prompt
    typewrite "$1"
    nap 0.3
    eval "$2"
    nap "$PAUSE"
}

note() {
    printf '%s# %s%s\n' "$DIM" "$1" "$RESET"
    nap 0.6
}

if [ -z "${GROQ_API_KEY:-}" ]; then
    printf '%serror: export GROQ_API_KEY before recording%s\n' "$RED" "$RESET" >&2
    exit 1
fi

# Warm the build cache so the recording has no first-compile stall.
go build -o /dev/null ./week-01/day-01 >/dev/null 2>&1

clear
printf '%s%sAI Advent Challenge — День 1%s\n' "$BOLD" "$CYAN" "$RESET"
printf '%sЗапрос в LLM через API, ответ в консоль. Go, только stdlib.%s\n\n' "$DIM" "$RESET"
nap 2.5

note 'Задание: минимальный код — запрос в LLM, ответ, вывод в консоль.'
run 'ls week-01/day-01'
run 'wc -l week-01/day-01/main.go'

note 'Endpoint, модель и структуры запроса/ответа.'
run 'sed -n "13,32p" week-01/day-01/main.go'
nap 1.5

note 'Ключ из окружения, prompt из argv. Ошибка — stderr и exit 1.'
run 'sed -n "34,54p" week-01/day-01/main.go'
nap 1.5

note 'Авторизация — обычный Bearer-заголовок.'
run 'grep -n "Header.Set" week-01/day-01/main.go'

note 'Без аргументов — usage и exit 1.'
run 'go run ./week-01/day-01; echo "exit=$?"'

note 'Без ключа — понятная ошибка, тоже exit 1.'
run 'env -u GROQ_API_KEY go run ./week-01/day-01 "привет"; echo "exit=$?"'

note 'Ключ берётся из окружения, в коде его нет.'
run_as 'export GROQ_API_KEY=gsk_••••••••••••••••••••••••' 'true'

note 'Живой запрос в Groq.'
run 'go run ./week-01/day-01 "Объясни одним предложением, что такое API."'

note 'Второй запрос — аргументы склеиваются в один prompt.'
run 'go run ./week-01/day-01 Назови три языка программирования списком, без пояснений.'

printf '\n%s%sГотово.%s %sweek-01/day-01/main.go — %s строк, net/http + encoding/json.%s\n' \
    "$BOLD" "$GREEN" "$RESET" "$DIM" "$(wc -l < week-01/day-01/main.go | tr -d ' ')" "$RESET"
nap 4
