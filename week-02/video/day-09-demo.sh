#!/usr/bin/env bash
# Demo for week-02, день 9: управление контекстом — сжатие истории.
#
# Один и тот же диалог-сбор ТЗ, один контрольный вопрос, два режима:
#  1. `full` — в запрос уходит вся сырая история. Ответ верный, но в
#     статусе видны большие history/sent и cost.
#  2. `summary` — последние `window` сообщений остаются как есть, всё
#     что старше сворачивается батчами по 10 сообщений в резюме.
#     Резюме хранится отдельно (`/summary` печатает сохранённое) и
#     подставляется в запрос вместо свёрнутой части истории.
#
# Сравнение честное: режимы живут в разных ветках одной истории
# (checkpoint + fork от общего префикса), поэтому `/stats` каждой ветки
# показывает только её собственные вызовы — отдельно strategy=full,
# отдельно strategy=summary с kind=main и kind=summary (цена самого
# сжатия).
#
# Поэтому же тут два экрана `/stats`, а не один `/stats all`: TUI
# обрезает строку статуса тремя строками (app.go, clipText(..., 3)), то
# есть в кадр попадают заголовок и ровно две строки учёта. `/stats all`
# показал бы две первые по алфавиту строки (full и sliding) и срезал бы
# как раз summary, ради которых всё и затевалось.
#
# Двенадцать реплик сбора ТЗ выбраны не случайно: к контрольному вопросу
# в ветке накапливается 26 сообщений, и первый же turn в `summary`
# сворачивает два батча по 10, оставляя сырым короткий хвост.
#
# Сам сбор ТЗ идёт в `sliding` с окном 2: в запрос уходят только
# последние два сообщения, хотя в SQLite ложится всё. Это не косметика — у аккаунта
# Groq лимит 8000 токенов в минуту, а сбор той же истории в `full`
# пересылает её целиком на каждом шаге и упирается в 429 примерно на
# десятой реплике. Контрольный вопрос всё равно задаётся в `full`,
# то есть baseline — настоящая полная история.
#
# Между фазами сессия tmux отсоединяется (demo_tui_detach), чтобы
# показать пояснение на обычном экране, затем снова подключается
# (demo_tui_watch) — тот же приём, что в day-08-demo.sh.
#
# Паузы после ответов модели сделаны через /bin/sleep, а не demo_pause:
# на записи это одно и то же, но dryrun.sh подменяет `sleep` заглушкой,
# и без реального ожидания весь диалог улетает в Groq за несколько
# секунд и снова ловит 429 посреди прогона.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-09-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
TUI_SESSION=demo-day09   # своя сессия: не конфликтовать с демо другого дня
cd "$REPO_ROOT"

demo_require_env GROQ_API_KEY
demo_tui_require

BIN=$(mktemp -t week02-demo-bin)
DBDIR=$(mktemp -d -t week02-demo-db)
DB="$DBDIR/agent.db"

cleanup() {
    demo_tui_stop
    rm -f "$BIN"
    rm -rf "$DBDIR"
}
trap cleanup EXIT

(cd week-02 && go build -o "$BIN" .) || { echo "error: go build ./week-02 failed" >&2; exit 1; }

# На записи посимвольный набор реплики сам по себе растягивает диалог;
# dryrun.sh ставит SPEED=0 и этого времени не тратит, поэтому сухой
# прогон бьёт по API заметно чаще живого и ловит 429 там, где запись
# проходит. Возвращаем эту разницу обратно только в сухом прогоне —
# тогда он честно репетирует темп записи.
demo_typing_slack() { [ "$SPEED" = "0" ] && /bin/sleep "$1"; }

demo_title "AI Advent Challenge — День 9" "Управление контекстом: сжатие истории в резюме"

demo_note 'Собираем ТЗ в режиме sliding: в запрос уходят только последние два сообщения,'
demo_note 'но в SQLite ложится вся история целиком. Потом включим full и увидим её вес.'
demo_pause 3

CMD=$(printf '%q -db %q -provider groq' "$BIN" "$DB")

# Широкая сессия: статус и /stats обрезаются по ширине панели, на 170
# колонках каждая строка учёта помещается целиком.
demo_tui_start "$CMD" 170 40
demo_tui_wait_for 'Write a message' 10 >/dev/null

# --- Фаза 1: собираем ТЗ и задаём контрольный вопрос по полной истории ---
(
    demo_tui_type '/mode sliding'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    demo_tui_type '/window 2'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    # «Двумя предложениями» повторяется в каждой реплике не для красоты:
    # без напоминания модель к середине диалога начинает отвечать
    # таблицами на пол-экрана — и кадр нечитаем, и лимит выгорает.
    for line in \
        'Собираем ТЗ, без списков и таблиц. Проект: доставка еды. Двумя предложениями.' \
        'Цель — MVP в трёх городах. Что уточнить? Двумя предложениями.' \
        'Срок 4 месяца, дедлайн 1 декабря. Риск? Двумя предложениями.' \
        'Бюджет 3 млн, превышать нельзя. Где течёт? Двумя предложениями.' \
        'Стек: Go и Flutter. Чем это грозит? Двумя предложениями.' \
        'Хостинг только в РФ, база PostgreSQL. Риск? Двумя предложениями.' \
        'Интеграции: ЮKassa и карты 2ГИС. Что заложить? Двумя предложениями.' \
        'Без AWS и GCP. Чем заменить managed-сервисы? Двумя предложениями.' \
        'API до 200 мс, аптайм 99.9%. Это реально? Двумя предложениями.' \
        'Команда 4 человека, один бэкендер. Узкое место? Двумя предложениями.' \
        'Нужна поддержка iOS 15 и Android 10. Что учесть? Двумя предложениями.' \
        'Аналитика только self-hosted. Чем заменить GA? Двумя предложениями.'
    do
        demo_tui_type "$line"
        demo_tui_key Enter
        demo_tui_wait_stable 30 1 >/dev/null
        /bin/sleep 3
        demo_typing_slack 2
    done

    demo_tui_type '/mode full'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    demo_tui_type 'Перечисли одной строкой все ограничения, которые мы зафиксировали.'
    demo_tui_key Enter
    demo_tui_wait_stable 30 1 >/dev/null
    /bin/sleep 5
    demo_typing_slack 2

    demo_tui_type '/stats'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 5

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Ответ полный: в запрос ушла вся сырая история — смотри history и sent.'
demo_note 'В /stats видно цену: двенадцать дешёвых вызовов сбора strategy=sliding'
demo_note 'и один тяжёлый strategy=full — тот самый контрольный вопрос.'
demo_note 'Теперь тот же вопрос со сжатием. Чтобы учёт токенов был раздельным,'
demo_note 'делаем checkpoint и форкаем ветку от общего префикса истории.'
demo_pause 3

# --- Фаза 2: форк ветки, режим summary, тот же контрольный вопрос ---
(
    demo_tui_type '/mode branching'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    demo_tui_type '/checkpoint base'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    demo_tui_type '/fork base compressed'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    demo_tui_type '/mode summary'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    # window задаёт минимум сырых сообщений в хвосте, а не точное число:
    # сворачивание идёт батчами ровно по 10, поэтому остаток младше
    # батча тоже остаётся сырым. При 26 накопленных сообщениях и window 4
    # это означает два батча (первые 20) и сырой хвост длиной 6 — так и
    # показывает потом `/summary` (through message 20).
    demo_tui_type '/window 4'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 3

    # Первый же turn в summary сворачивает историю старше окна батчами
    # по 10 — это отдельные вызовы kind=summary, и только потом идёт
    # основной запрос с резюме вместо истории.
    demo_tui_type 'Перечисли одной строкой все ограничения, которые мы зафиксировали.'
    demo_tui_key Enter
    demo_tui_wait_stable 60 1 >/dev/null
    /bin/sleep 6
    demo_typing_slack 2

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Ответ тот же по содержанию, а история тут даже длиннее — в ней уже лежат'
demo_note 'вопрос и ответ из full. Но sent заметно меньше: вместо свёрнутой части'
demo_note 'истории ушло резюме. Оно хранится отдельно от сообщений — вот оно.'
demo_pause 3

# --- Фаза 3: сохранённое резюме ветки ---
(
    demo_tui_type '/summary'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 8

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Это и подставляется в запрос вместо первых двадцати сообщений ветки —'
demo_note 'они свёрнуты двумя батчами по 10. Смотрим, во что обошлось сжатие.'
demo_pause 3

# --- Фаза 4: учёт токенов ветки ---
(
    demo_tui_type '/stats'
    demo_tui_key Enter
    demo_tui_wait_stable 15 1 >/dev/null
    demo_pause 7

    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'kind=main — основной запрос по резюме, kind=summary — цена самого сжатия,'
demo_note 'два вызова на два батча. Их платят один раз, а экономия на sent —'
demo_note 'на каждом следующем сообщении ветки.'
demo_pause 3

(
    demo_tui_type '/quit'
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-02'
