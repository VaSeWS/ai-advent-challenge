## Why

Задания второй недели требуют сравнить стратегии управления длинным контекстом, но текущий репозиторий содержит только отдельные однократные CLI-программы первой недели без диалогового состояния, ветвлений или измеримых расходов. Нужна одна воспроизводимая терминальная программа, которая хранит чаты локально и позволяет наблюдать последствия каждого режима контекста.

## What Changes

- Добавить самостоятельную программу `week-02` с полноэкранным Bubble Tea TUI, несколькими чатами и slash-командами.
- Ввести выбираемые при запуске OpenAI-compatible provider profiles: Groq по умолчанию и DeepSeek как fallback, без SDK и без утечки ключей.
- Добавить SQLite-хранилище чатов, неизменяемых linked lineage, веток, checkpoint, summary, facts и сохранённых API-метрик.
- Реализовать стратегии `full`, `summary`, `sliding`, `facts` и `branching` с проверкой лимита контекста до обращения к API.
- Отображать локальные и API token usage, provider-aware стоимость и историческую агрегированную статистику.
- Добавить документацию запуска, провайдеров, цен и сценариев сравнения стратегий.

## Capabilities

### New Capabilities
- `context-agent-runtime`: запуск `week-02`, выбор provider profile, OpenAI-compatible completion и token/cost accounting.
- `context-agent-storage`: SQLite-персистентность чатов, branch lineage, checkpoints, context state и метрик.
- `context-agent-strategies`: построение prompt для пяти стратегий контекста и защита лимита контекста.
- `context-agent-terminal`: полноэкранный TUI, transcript и контракт slash-команд.

### Modified Capabilities
- Нет.

## Impact

- Новый каталог `week-02/` в существующем корневом Go module с Bubble Tea, SQLite и OmniToken как прямыми зависимостями.
- Обновления `go.mod`, `go.sum`, `.gitignore` и корневого `README.md`.
- Появятся локальные SQLite-файлы `week-02/agent.db*`, которые не должны попасть в Git.
- Существующие `week-01/day-*` программы и их контракты не изменяются.