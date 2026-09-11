## Context

См. `proposal.md` для мотивации. В репозитории есть только самостоятельные программы `week-01/day-01`…`day-05`: HTTP-клиент и usage JSON могут быть адаптированы, однако chat state, provider abstraction, SQLite и TUI отсутствуют. Новая программа остаётся единственным `package main` в `week-02` существующего root module.

## Goals / Non-Goals

**Goals:**
- Сохранить разделение UI, Agent, Store, provider profile и context strategy, чтобы смена backend не затрагивала остальные слои.
- Хранить message history как immutable linked lineage; fork выполняется ссылкой на checkpoint head, без копирования prefix.
- Сохранять usage и стоимость с каждым вызовом, чтобы historical stats не менялась после изменения цен.
- Дать проверяемые contracts для provider normalization, pricing boundaries, persistence, context builders и command state.

**Non-Goals:**
- Streaming, retry/backoff, экспорт, удаление и переименование сущностей.
- Общий framework для будущих недель, отдельный Go module или provider SDK.
- Загрузка `.env` приложением, хранение ключей либо изменение программ первой недели.

## Decisions

### Один registry provider profiles

`providers.go` содержит единственный `map[string]ProviderProfile`. Groq использует `openai/gpt-oss-20b`, endpoint Groq, `max_completion_tokens`, low reasoning и `o200k_harmony`; DeepSeek использует `deepseek-flash`, свой endpoint, `max_tokens`, disabled thinking и estimate counter. Profile несёт context window, reserves, request extras и pricing, поэтому `Agent`, strategy и TUI работают с одним интерфейсом completion.

Альтернатива — условные ветви в Agent — отвергнута: она размазывает provider differences и усложняет добавление profile.

### Store как источник состояния и атомарная граница turn

`Store.Open` включает foreign keys и WAL, применяет schema version 1 и отказывает большей версии. Таблицы chats, branches, messages, summaries, facts, checkpoints и api_calls следуют контракту change. Recursive CTE читает lineage один раз и разворачивает её хронологически. `SaveTurn` выполняет user/assistant inserts, branch head, strategy state и API metrics в одной SQL transaction.

Альтернатива с копированием message rows при fork отвергнута: она занимает место и допускает расхождение общего prefix.

### Agent координирует completion и контекст

`Agent.Send` получает chat/branch/input, считывает lineage и strategy state, строит prompt, выполняет нужные auxiliary calls, проверяет reserve до main request, нормализует usage/cost и передаёт подготовленные данные в одну Store transaction. UI не строит API messages и не пишет dialog state. Network и database error возвращаются из Agent; `main` завершает приложение, а command syntax/context overflow остаются message в TUI.

### Наблюдаемые стратегии без удаления raw history

`full` отправляет всю lineage. `sliding` отправляет последние `window_size` raw messages. `summary` создаёт auxiliary summary batches по 10 старых непокрытых сообщений и отправляет watermark suffix. `facts` rebuild делает chronologic batches по 10 при входе в mode и заменяет facts только после валидного полного результата; каждый main turn обновляет map отдельным call. `branching` использует только lineage active branch и checkpoint snapshot facts/summary.

Все auxiliary calls фиксируются как `summary`/`facts` в api_calls. Raw messages остаются в SQLite для сравнения режимов.

### Полноэкранный TUI без streaming

Bubble Tea v2 управляет sidebar, transcript viewport, textarea, spinner, status и command parsing. Agent completion выполняется как Bubble Tea command, а busy state запрещает параллельные submit/mode change. Минимальная ширина скрывает sidebar. `Enter`, `Ctrl+J`, paging и exit соответствуют terminal spec.

Полный response после completion выбран вместо streaming: usage и transaction boundary становятся однозначными.

## Risks / Trade-offs

- Локальный DeepSeek token count намеренно estimate; billing и cost опираются на API usage.
- Summary/facts auxiliary responses могут быть невалидны или API недоступен; транзакция не должна оставить partial strategy state.
- SQLite WAL создаёт `-wal`/`-shm`; соответствующая маска игнорируется Git.
- Реальный provider smoke зависит от уже экспортированного ключа. Отсутствующий ключ доказывается fail-fast path, остальные contracts — offline tests.