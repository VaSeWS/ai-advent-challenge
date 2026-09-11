## Purpose

Определяет интерактивный полноэкранный терминальный интерфейс для управления чатами, ветками и стратегиями контекста.

## ADDED Requirements

### Requirement: Адаптивный интерактивный интерфейс

Программа SHALL использовать alternate screen и отображать transcript, textarea, provider/chat/branch/mode/window status и последние metrics/errors. При ширине менее 80 columns sidebar MUST скрываться без поломки transcript или input. Во время completion TUI MUST блокировать submit и strategy-changing commands, показывая busy state.

#### Scenario: Узкий терминал
- **WHEN** terminal шире́н менее 80 columns
- **THEN** transcript, input и status остаются доступны, а sidebar не отображается

### Requirement: Управление чатами

`/new <title>`, `/chats` и `/use <chat-id>` SHALL создавать, перечислять и переключать chats. После создания или переключения TUI MUST заменить transcript активной lineage, не смешивая сообщения chat.

#### Scenario: Два независимых чата
- **WHEN** пользователь создаёт второй чат, отправляет сообщение и возвращается в первый
- **THEN** transcript первого чата не содержит сообщения второго

### Requirement: Команды режима и веток

TUI SHALL поддерживать `/mode`, `/window`, `/checkpoint`, `/fork`, `/branches`, `/switch`, `/facts`, `/stats`, `/stats all`, `/help` и `/quit` с описанными в help аргументами. Checkpoint и fork MUST быть доступны только в branching mode; validation error MUST сохранять textarea.

#### Scenario: Неподдерживаемая команда
- **WHEN** пользователь вводит неизвестную slash-команду
- **THEN** TUI показывает `unknown command: /...; use /help` и не очищает введённый текст

### Requirement: Отправка и выход

`Enter` SHALL отправлять непустой input, `Ctrl+J` SHALL вставлять newline, `PgUp`/`PgDn` SHALL прокручивать transcript, а `Ctrl+C` и `/quit` SHALL корректно закрывать application.

#### Scenario: Пустой ввод
- **WHEN** пользователь нажимает Enter с пустым textarea
- **THEN** API-вызов и изменение transcript не происходят