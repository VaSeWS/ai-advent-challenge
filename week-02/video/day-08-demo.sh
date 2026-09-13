#!/usr/bin/env bash
# Demo for week-02, день 8: подсчёт токенов, сравнение короткого/длинного/
# переполненного диалога, и что именно ломается при переполнении.
#
# Три части:
#  1. TUI (Groq): короткое и длинное сообщение — виден рост
#     current/history/sent/response и cost в статус-строке.
#  2. TUI: заранее откалиброванный блок ~130000 токенов вставляется одним
#     куском (не печатается) — guard week-02 ловит переполнение ДО вызова
#     API и показывает точную ошибку. Между шагами 1 и 2 сессия tmux
#     отсоединяется (demo_tui_detach), чтобы показать пояснение на обычном
#     экране, затем снова подключается (demo_tui_watch).
#  3. Тот же класс переполнения напрямую в модель, без guard'а — локальная
#     llama3 через Ollama на M1 Pro (week-02/video/local-overflow-probe.sh).
#     Модель не сообщает об ошибке: она молча обрезает промпт и отвечает
#     неверно — контраст с явным guard'ом week-02.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-08-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY
demo_tui_require
command -v jq >/dev/null 2>&1 || { echo "error: jq is required" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is required" >&2; exit 1; }

OLLAMA_BIN=/opt/homebrew/opt/ollama/bin/ollama
[ -x "$OLLAMA_BIN" ] || { echo "error: ollama not found at $OLLAMA_BIN (brew install ollama)" >&2; exit 1; }

BIN=$(mktemp -t week02-demo-bin)
DBDIR=$(mktemp -d -t week02-demo-db)
DB="$DBDIR/agent.db"
STARTED_OLLAMA=0

cleanup() {
    demo_tui_stop
    rm -f "$BIN"
    rm -rf "$DBDIR"
    if [ "$STARTED_OLLAMA" = "1" ]; then
        pkill -f "ollama serve" 2>/dev/null || true
    fi
}
trap cleanup EXIT

(cd week-02 && go build -o "$BIN" .) || { echo "error: go build ./week-02 failed" >&2; exit 1; }

if ! curl -s -o /dev/null http://localhost:11434/api/version; then
    "$OLLAMA_BIN" serve >/dev/null 2>&1 &
    disown
    STARTED_OLLAMA=1
    for _ in $(seq 1 30); do
        curl -s -o /dev/null http://localhost:11434/api/version && break
        /bin/sleep 0.5
    done
fi

demo_title "AI Advent Challenge — День 8" "Токены: короткий/длинный диалог и что ломается при переполнении"

CMD=$(printf '%q -db %q -provider groq' "$BIN" "$DB")

# --- Часть 1: короткий и длинный диалог (Groq) ---
demo_tui_start "$CMD" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null

(
    demo_tui_type 'Привет! Что такое токен в контексте LLM?'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4

    demo_tui_type 'Опиши подробно, как считаются токены для текущего запроса, истории диалога и ответа модели, и почему это важно для стоимости и лимитов контекста.'
    demo_tui_key Enter
    demo_tui_wait_stable 25 1 >/dev/null
    demo_pause 4

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Groq: окно модели — 131072 токена. Набрать столько вручную нереально —'
demo_note 'дальше одним блоком вставляется заранее подготовленный текст на ~130000 токенов.'
demo_pause 3

# --- Часть 2: переполнение — guard week-02 ловит его ДО вызова API ---
UNIT="1 2 3 4 5 6 7 8 9 0 "
OVERFLOW_TEXT=$(python3 -c "print('$UNIT' * 6500, end='')")

(
    demo_tui_paste "$OVERFLOW_TEXT"
    /bin/sleep 1   # let the bracketed paste land before Enter — real wall-clock
                   # wait, not demo_pause: dry-run's sleep stub must not zero this
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 5
    demo_tui_key C-u   # overflow leaves the huge input preserved for retry —
                        # clear it first or "/quit" just appends to it
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

# --- Часть 3: тот же приём напрямую в модель, без guard'а (локально) ---
demo_note 'Тот же приём напрямую в модель, без guard-проверки week-02:'
demo_note 'локальная llama3 через Ollama на M1 Pro — без ключей, без rate limit.'
demo_pause 3

demo_run './week-02/video/local-overflow-probe.sh'
demo_pause 5

demo_note 'Guard week-02 — единственное, что явно сообщает о переполнении;'
demo_note 'сырой вызов модели может просто промолчать об этом и ответить неверно.'
demo_pause 3

demo_outro 'week-02'
