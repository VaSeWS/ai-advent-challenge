#!/usr/bin/env bash
# Демонстрация для дня 8: тот же класс переполнения, что ловит guard
# week-02, но напрямую в модель, без него. Endpoint и модель те же, что у
# профиля `-provider local`: OpenAI-compatible /v1/chat/completions Ollama
# и llama3-ctx2k (llama3 с num_ctx 2048). Разница только в guard'е.
#
# В отличие от week-02, здесь модель НЕ сообщает об ошибке — она молча
# теряет часть промпта и отвечает неверно.
#
# Как это устроено (проверено эмпирически на Ollama 0.33.3):
# - Размер окна задаётся не запросом, а самой моделью: options.num_ctx
#   через /v1/... игнорируется, поэтому используется модель llama3-ctx2k,
#   созданная из Modelfile с `PARAMETER num_ctx 2048`.
# - Если промпт больше окна, Ollama не возвращает ошибку: она молча
#   обрезает промпт примерно до num_ctx/2 токенов и оставляет короткий
#   "keep"-хвост в начале (num_keep, по умолчанию 24 токена). Поэтому
#   секретный факт ставится НЕ в самое начало (иначе он попадёт в
#   защищённый keep-хвост и выживет), а сразу после него — тогда при
#   достаточном объёме текста после факта он окажется в отброшенной части.
#
#   ./week-02/video/local-overflow-probe.sh

set -uo pipefail

MODEL=llama3-ctx2k
BEFORE=8   # реплик filler до факта — выводит факт за пределы num_keep
AFTER=300  # реплик filler после факта — гарантирует, что факт попадёт
           # в отброшенную (не «keep», не «tail») часть промпта

BODY=$(python3 -c "
import json
unit = 'Программирование как дисциплина развивалось десятилетиями, проходя путь от перфокарт и ассемблера до современных языков высокого уровня с автоматическим управлением памятью. '
fact = 'Секретный код доступа к серверу: 4817-ZEBRA. '
text = unit * $BEFORE + fact + unit * $AFTER
text += chr(10) + chr(10) + 'Найди в тексте выше секретный код доступа и назови только его, без пояснений.'
print(json.dumps({'model': '$MODEL', 'messages': [{'role': 'user', 'content': text}], 'stream': False, 'max_tokens': 64}))
")

curl -sS http://localhost:11434/v1/chat/completions -H 'Content-Type: application/json' -d "$BODY" \
    | python3 -c "
import json, sys
response = json.load(sys.stdin)
usage = response.get('usage', {})
print(json.dumps({
    'model': '$MODEL',
    'num_ctx': 2048,
    'prompt_tokens_actually_used': usage.get('prompt_tokens'),
    'answer': response['choices'][0]['message']['content'].strip(),
}, ensure_ascii=False, indent=2))
"
