#!/usr/bin/env bash
# Demo for week-02, день 8: подсчёт токенов, сравнение короткого/длинного/
# переполненного диалога, и что именно ломается при переполнении.
#
# Четыре части, все — в одном и том же TUI week-02:
#  1. Groq: короткая реплика — видно все четыре счётчика в статусе
#     (current — текущий запрос, history — вся история, sent — реально
#     собранный prompt, response — ответ) плюс API usage и cost.
#  2. Groq: длинный диалог — те же счётчики растут, /stats показывает
#     накопленный расход.
#  3. Локальная модель через тот же TUI (`-provider local`): llama3-ctx2k
#     с окном 2048. У Groq окно 131072, у DeepSeek — 1000000, и набрать
#     столько диалогом невозможно; с окном 2048 переполнение наступает на
#     обычных репликах, без ключей, без rate limit и с cost=$0.000000.
#     Guard week-02 ловит его ДО вызова API, а /mode sliding возвращает
#     диалог в окно.
#  4. Тот же промпт напрямую в ту же модель, без guard'а
#     (week-02/video/local-overflow-probe.sh): Ollama не сообщает об
#     ошибке — она молча обрезает промпт и отвечает неверно.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-08-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

TUI_SESSION=demo-day08

demo_require_env GROQ_API_KEY
demo_tui_require
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is required" >&2; exit 1; }

OLLAMA_BIN=/opt/homebrew/bin/ollama
[ -x "$OLLAMA_BIN" ] || OLLAMA_BIN=/opt/homebrew/opt/ollama/bin/ollama
[ -x "$OLLAMA_BIN" ] || { echo "error: ollama not found (brew install ollama)" >&2; exit 1; }

BIN=$(mktemp -t week02-demo-bin)
DBDIR=$(mktemp -d -t week02-demo-db)
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

# Профиль local ожидает llama3 с окном 2048 — это обычная llama3, пересобранная
# из Modelfile. Создаём её один раз, если её ещё нет на машине.
if ! "$OLLAMA_BIN" list 2>/dev/null | grep -q '^llama3-ctx2k'; then
    MODELFILE=$(mktemp -t llama3-ctx2k)
    printf 'FROM llama3:latest\nPARAMETER num_ctx 2048\n' > "$MODELFILE"
    "$OLLAMA_BIN" create llama3-ctx2k -f "$MODELFILE" >/dev/null 2>&1 \
        || { echo "error: ollama create llama3-ctx2k failed" >&2; exit 1; }
    rm -f "$MODELFILE"
fi

demo_title "AI Advent Challenge — День 8" "Токены: короткий диалог, длинный диалог и что ломается при переполнении"

# --- Часть 1-2: короткий и длинный диалог (Groq) ---
demo_note 'Статус после ответа: current — токены текущего запроса, history — вся история,'
demo_note 'sent — реально собранный prompt, response — ответ. Рядом — usage от API и стоимость.'
demo_pause 3

demo_tui_start "$(printf '%q -db %q -provider groq' "$BIN" "$DBDIR/groq.db")" 100 30
demo_tui_wait_for 'Write a message' 10 >/dev/null

(
    demo_tui_type 'Что такое токен в контексте LLM? Ответь одним предложением.'
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    demo_pause 5

    demo_tui_type 'Теперь подробнее: как считаются токены запроса, истории и ответа и как это влияет на стоимость? Три пункта.'
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    demo_pause 5

    demo_tui_type '/stats'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 5

    demo_tui_detach
) &
demo_tui_watch
wait

# --- Часть 3: переполнение на локальной модели, тот же TUI ---
demo_note 'Окно Groq — 131072 токена, DeepSeek — 1000000: диалогом столько не набрать.'
demo_note 'Поэтому переполнение показываем на локальной модели в том же TUI: llama3 с окном 2048,'
demo_note 'через Ollama на M1 Pro — без ключей, без rate limit, cost=$0.000000.'
demo_pause 4

demo_tui_start "$(printf '%q -db %q -provider local' "$BIN" "$DBDIR/local.db")" 100 30
demo_tui_wait_for 'Write a message' 15 >/dev/null

FRAGMENT=$(python3 -c "
lines = [
    'Система должна выдерживать 500 одновременных пользователей в часы пик. ',
    'Данные хранятся только в российском ЦОД, вынос за периметр запрещён. ',
    'Обмен с 1С идёт по расписанию раз в час, ретраи не чаще трёх подряд. ',
    'Отчёты выгружаются в XLSX, срок хранения выгрузок — девяносто дней. ',
]
print(''.join(lines[i % len(lines)] for i in range(24)), end='')
")

(
    demo_tui_type 'Привет! Одним предложением: зачем агенту считать токены?'
    demo_tui_key Enter
    demo_tui_wait_stable 40 1 >/dev/null
    demo_pause 5

    demo_tui_paste "$FRAGMENT"
    /bin/sleep 1   # let the bracketed paste land before Enter — real wall-clock
                   # wait, not demo_pause: dry-run's sleep stub must not zero this
    demo_tui_key Enter
    demo_tui_wait_stable 40 1 >/dev/null
    demo_pause 5

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Один фрагмент ТЗ — и sent уже больше тысячи токенов. Добавляем второй такой же.'
demo_pause 3

(
    demo_tui_paste "$FRAGMENT"
    /bin/sleep 1
    demo_tui_key Enter
    demo_tui_wait_stable 20 1 >/dev/null
    demo_pause 6

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Guard сработал до вызова API: запрос не отправлен, история не потеряна.'
demo_note 'Диагностика сама называет выход — сменить стратегию сборки контекста.'
demo_pause 4

(
    demo_tui_key C-u   # overflow leaves the huge input preserved for retry —
                       # clear it first or the next command just appends to it
    demo_tui_type '/mode sliding'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_tui_type '/window 2'
    demo_tui_key Enter
    demo_tui_wait_stable 10 1 >/dev/null
    demo_pause 2

    demo_tui_type 'Какие ограничения мы зафиксировали?'
    demo_tui_key Enter
    demo_tui_wait_stable 40 1 >/dev/null
    demo_pause 6

    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

# --- Часть 4: тот же промпт напрямую в модель, без guard'а ---
demo_note 'sent снова в окне: история осталась в базе, в prompt ушли последние две реплики.'
demo_pause 3
demo_note 'А теперь тот же переполненный промпт напрямую в ту же модель, мимо guard-проверки:'
demo_pause 2

demo_run './week-02/video/local-overflow-probe.sh'
demo_pause 6

demo_note 'Промпт на ~10000 токенов ушёл целиком, модель приняла 1036 и кода не нашла:'
demo_note 'он остался в отброшенной части. Ошибки при этом никто не показал — запрос'
demo_note 'выглядит успешным. Вот что ломается при переполнении, если не проверять его до вызова.'
demo_pause 4

demo_outro 'week-02'
