#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"
if [ ! -t 0 ] || [ ! -t 1 ]; then
    export TERM=xterm
fi
DAY28_PYTHON=${DAY28_PYTHON:-"$HOME/.local/share/ai-advent/day28-renderer/bin/python"}
if [ ! -x "$DAY28_PYTHON" ]; then
    printf 'Renderer interpreter is unavailable: %s\nCreate the isolated venv using the day-28 README.\n' "$DAY28_PYTHON" >&2
    exit 1
fi
if ! "$DAY28_PYTHON" -c 'import regex, wcwidth' >/dev/null 2>&1; then
    printf 'Renderer dependencies are unavailable in %s; install video/requirements.txt into the isolated venv described in the day-28 README.\n' "$DAY28_PYTHON" >&2
    exit 1
fi

: "${DAY28_LOCAL_REPORT:?Set the immutable NEW selected4B native A20 JSONL, never an old27B report}"
: "${DAY28_CLOUD_REPORT:?Set the existing immutable DeepSeek A20 JSONL}"
: "${DAY28_LOCAL_REVIEW:?Set the independent digest-bound NEW selected4B all20 review}"
: "${DAY28_CLOUD_REVIEW:?Set the existing corrected DeepSeek all20 review}"
: "${DAY28_FROZEN:?Set the approved immutable day28-frozen-real-v1.json}"
if [ "${DAY28_ALLOW_HISTORICAL:-0}" != 0 ]; then
    printf 'Final video never substitutes historical27B data. Use compare.py --historical-confound separately for archive playback.\n' >&2
    exit 1
fi
comparison=(
    "$DAY28_PYTHON" week-06/day-28/video/compare.py
    "$DAY28_LOCAL_REPORT" "$DAY28_CLOUD_REPORT"
    --left-provider local --right-provider deepseek --left-profile A --right-profile A
    --left-label 'NEW Qwen3.5-4B A' --right-label 'EXISTING DeepSeek A'
    --require-selected-model --frozen "$DAY28_FROZEN"
    --left-review "$DAY28_LOCAL_REVIEW" --right-review "$DAY28_CLOUD_REVIEW"
)
# Validate the saved real experiment before presenting any scene.
"${comparison[@]}" --check-final-only
printf -v renderer '%q ' "${comparison[@]}"

demo_title 'AI Advent Challenge — День 28' 'SAVED: локальный 4B RAG / DeepSeek'
demo_note 'SAVED: NEW4B A20 против EXISTING DeepSeek20. Все captured requests/schema/frozen-контексты и независимые reviews проверены; здесь новых вызовов моделей нет.'
demo_run "${renderer}--question q03 --repeat 1 --pause 4 --page-hold 6"
demo_note 'SAVED: q09 — фактический отказ, ответ или error. Ошибки не превращаются в корректные refusals.'
demo_run "${renderer}--question q09 --repeat 1 --pause 4 --page-hold 6"
demo_note 'Независимое качество / повторы / фактическое generation-path wall time. Знаменатель всегда 20; это не GPU compute и не benchmark выбора модели.'
demo_run "${renderer}--quality-summary --pause 6 --page-hold 6"
demo_outro 'week-06/day-28'
