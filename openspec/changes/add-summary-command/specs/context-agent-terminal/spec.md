## MODIFIED Requirements

### Requirement: Команды режима и веток

TUI SHALL поддерживать `/mode`, `/window`, `/checkpoint`, `/fork`, `/branches`, `/switch`, `/facts`, `/summary`, `/stats`, `/stats all`, `/help` и `/quit` с описанными в help аргументами. Checkpoint и fork MUST быть доступны только в branching mode; validation error MUST сохранять textarea.

#### Scenario: Неподдерживаемая команда
- **WHEN** пользователь вводит неизвестную slash-команду
- **THEN** TUI показывает `unknown command: /...; use /help` и не очищает введённый текст

#### Scenario: Просмотр сохранённого резюме
- **WHEN** пользователь вводит `/summary` на ветке, для которой резюме уже построено
- **THEN** TUI показывает содержимое резюме и id сообщения, по которое оно построено, без обращения к LLM

#### Scenario: Резюме ещё не построено
- **WHEN** пользователь вводит `/summary` на ветке без резюме
- **THEN** TUI показывает `summary:` и `(no summary)`, а ветка не изменяется
