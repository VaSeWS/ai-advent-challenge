#!/usr/bin/env bash
# Demo for week-02, день 10: три стратегии управления контекстом без summary —
# sliding window, sticky facts и branching — на одном и том же диалоге.
#
# Сценарий: сбор ТЗ на внутренний портал заявок. Во второй реплике звучит
# ранняя деталь-маркер («дедлайн 14 марта, облако запрещено»), и её потом
# спрашивает один и тот же дословный контрольный вопрос в двух стратегиях:
#
#  1. /mode sliding + /window 2 — в запрос уходят только две последние
#     реплики, ранняя деталь до модели не доходит (маленький sent).
#  2. /mode facts — вход перестраивает блок фактов по всей истории
#     (вспомогательный вызов на каждые 10 сообщений), /facts показывает
#     key-value память, и тот же вопрос при том же окне 2 получает верный
#     ответ: деталь держит блок фактов, а не история.
#  3. /mode branching — /checkpoint и два /fork от одной точки, независимые
#     продолжения, /switch и /branches.
#
# Расход сравнивается строкой статуса (current/history/sent/response + cost)
# и /stats с колонкой strategy, который снимается в каждой фазе отдельно.
#
# Почему так много коротких tmux-блоков: результат команды TUI печатает в
# notice-строке, и следующая команда её затирает. Поэтому каждый
# кадр-доказательство (/facts, /stats, /branches, ответ на контрольный
# вопрос) заканчивает свой блок — demo_tui_detach, подпись на обычном
# экране, затем новый demo_tui_watch.
#
# Размер pane — 180x45: notice-строка вмещает треть высоты окна (app.go,
# statusLimit), так что таблица /stats попадает в кадр целиком, но только
# если каждая её строка (~140 символов) укладывается в одну строку экрана.
# При записи tmux растянет окно под клиента (window-size=latest), поэтому
# record.sh запускается с более широким окном Terminal, чем 1280 точек по
# умолчанию.
#
# Последняя реплика базового диалога намеренно про название кнопки: модель
# отвечает на неё двумя словами и не пересказывает ТЗ, иначе дедлайн попал
# бы в последнее сообщение и sliding-окно на 2 реплики его бы «помнило».
#
# Реплика «Кстати, интерфейс портала — только на русском» перед /mode facts
# нужна не для красоты: без неё в окно из 2 реплик попадает предыдущая пара
# «контрольный вопрос — не знаю», и модель в facts повторяет это «не знаю»
# даже при непустом блоке фактов. Нейтральная реплика ТЗ выталкивает пару из
# окна, и facts отвечает по фактам.
#
# Длина диалога подобрана так, что к моменту /mode facts в истории ровно 10
# сообщений: батчи rebuild'а — по 10, то есть это один вспомогательный
# вызов вместо двух. Каждый такой вызов — шанс, что gpt-oss-20b вернёт
# значение факта списком вместо строки или оформит JSON тулколом (Groq
# отвечает на это 400 tool_use_failed) и rebuild упадёт. Поэтому же в
# репликах нет перечислений вроде «роли: заявитель, исполнитель,
# администратор»: на них модель стабильно отдаёт массив, а хранилище
# принимает только строковые значения фактов.
#
#   export GROQ_API_KEY=...
#   ./week-02/video/day-10-demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
TUI_SESSION=demo-day10   # своя сессия: демо других дней могут идти параллельно
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

# Дождаться, пока напечатанное действительно оказалось в поле ввода.
# Смотрим именно строку ввода (она начинается с «You>»), а не весь экран:
# контрольный вопрос задаётся дважды, и первый его экземпляр уже висит в
# transcript. Реальный /bin/sleep, а не demo_pause: dryrun.sh подменяет
# sleep заглушкой.
tui_await_typed() {
    local text=$1 i
    for ((i = 0; i < 30; i++)); do
        tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null |
            grep '^You>' | grep -qF -- "$text" && return 0
        /bin/sleep 0.2
    done
    return 1
}

# Один шаг ввода в TUI: очистить поле, напечатать, отправить, дождаться
# конца ответа, подержать кадр.
#
# C-u в начале — не украшение: неуспешная команда или неуспешный turn
# оставляют текст в поле ввода для повтора, и следующая напечатанная строка
# иначе допишется к нему и уедет в модель одной сломанной командой.
#
# Ожидание перед Enter обязательно: при записи символы идут по одному, а в
# dry run — мгновенно, и тогда Enter попадает в TUI одним чтением вместе с
# хвостом текста. Bubble Tea принимает такой пакет за вставку и кладёт в
# поле перевод строки вместо отправки — реплика тихо не уходит.
tui_send() {
    local text=$1 timeout=${2:-30} stable=${3:-1} hold=${4:-3}
    demo_tui_key C-u
    demo_tui_type "$text"
    tui_await_typed "$text"
    /bin/sleep 0.2
    demo_tui_key Enter
    demo_tui_wait_stable "$timeout" "$stable" >/dev/null
    demo_pause "$hold"
}

demo_title "AI Advent Challenge — День 10" "Три стратегии контекста без summary: sliding, facts, branching"

CMD=$(printf '%q -db %q -provider groq' "$BIN" "$DB")

# Дословно один и тот же контрольный вопрос для sliding и facts, иначе
# сравнение нечестное.
CONTROL='Контрольный вопрос: назови дедлайн и правило про облако. Если не знаешь — так и скажи.'

# --- Фаза 1: базовый диалог сбора ТЗ в режиме full ---
demo_tui_start "$CMD" 180 45
demo_tui_wait_for 'Write a message' 10 >/dev/null

(
    tui_send 'Собираем ТЗ на внутренний портал заявок. Отвечай одной короткой фразой только по последней реплике, без пересказа ТЗ.'
    tui_send 'Жёсткий дедлайн — 14 марта, облако запрещено: только наши серверы.'
    tui_send 'Как назвать кнопку создания заявки?' 30 1 4
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Ранняя деталь-маркер прозвучала во второй реплике: дедлайн 14 марта и запрет облака.'
demo_note 'Дальше та же история идёт через три стратегии с одним контрольным вопросом.'
demo_pause 3

# --- Фаза 2: sliding window — в запрос уходят только последние 2 реплики ---
(
    tui_send '/mode sliding'
    tui_send '/window 2'
    tui_send "$CONTROL" 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'История цела в базе, но в запрос ушли только две последние реплики:'
demo_note 'ранняя деталь до модели не дошла, и это видно по маленькому sent.'
demo_pause 3

# --- Фаза 3: расход sliding ---
(
    tui_send '/stats' 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Диалог продолжается как обычно, и на той же истории включается вторая'
demo_note 'стратегия: вход в facts перестраивает память фактов по всему диалогу.'
demo_pause 3

# --- Фаза 4: sticky facts — rebuild и содержимое памяти фактов ---
(
    tui_send 'Понятно. Кстати, интерфейс портала — только на русском.' 30 1 4
    tui_send '/mode facts' 60 2 4
    tui_send '/facts' 30 1 6
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Факты — key-value память: цель, ограничения, предпочтения, решения, договорённости.'
demo_note 'Окно осталось прежним, 2 реплики: меняется только блок фактов в запросе.'
demo_pause 3

# --- Фаза 5: тот же контрольный вопрос в facts ---
(
    tui_send "$CONTROL" 45 2 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Тот же вопрос и то же окно — но деталь из начала диалога на месте:'
demo_note 'её держит блок фактов, а не история диалога.'
demo_pause 3

# --- Фаза 6: расход facts ---
(
    tui_send '/stats' 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'У facts есть своя цена: строка kind=facts — это вспомогательные вызовы памяти.'
demo_note 'Третья стратегия — ветки: одна точка истории, два независимых продолжения.'
demo_pause 3

# --- Фаза 7: branching — checkpoint и первая ветка ---
(
    tui_send '/mode branching'
    tui_send '/checkpoint base'
    tui_send '/fork base option-a'
    tui_send 'Вариант А: только веб-интерфейс, без интеграции с 1С. Плюсы и минусы одной строкой.' 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Ветка option-a продолжает общий префикс истории, ничего не копируя.'
demo_note 'Возвращаемся на main и от того же checkpoint делаем вторую ветку.'
demo_pause 3

# --- Фаза 8: вторая ветка от той же точки ---
(
    tui_send '/switch main'
    tui_send '/fork base option-b'
    tui_send 'Вариант Б: с интеграцией с 1С и импортом заявок. Плюсы и минусы одной строкой.' 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'В option-b нет ответа из option-a: префикс общий, продолжения независимые.'
demo_pause 3

# --- Фаза 9: возврат в первую ветку и её расход ---
(
    tui_send '/switch option-a' 30 1 4
    tui_send '/stats' 30 1 5
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Переключение назад вернуло продолжение option-a целиком.'
demo_pause 3

# --- Фаза 10: список веток ---
(
    tui_send '/branches' 30 1 6
    demo_tui_detach
) &
demo_tui_watch
wait

demo_note 'Стратегия — это одна команда на той же истории: ничего не удалялось,'
demo_note 'все реплики, факты и ветки остались в одной базе.'
demo_pause 3

# --- Фаза 11: выход ---
(
    demo_tui_key C-u
    demo_tui_type '/quit'
    tui_await_typed '/quit'
    /bin/sleep 0.2
    demo_tui_key Enter
) &
demo_tui_watch
wait

demo_outro 'week-02'
