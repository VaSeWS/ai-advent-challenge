## Purpose

Определяет запуск агента, выбор OpenAI-compatible провайдера и наблюдаемое token/cost accounting без раскрытия секретов.

## ADDED Requirements

### Requirement: Выбор провайдера при запуске

Программа SHALL принимать `-db` с default `agent.db` и `-provider` с default `groq`. Она SHALL поддерживать только `groq` и `deepseek`; неизвестное значение MUST завершать процесс с `error: unknown provider %q (expected groq or deepseek)`.

#### Scenario: Неизвестный провайдер
- **WHEN** пользователь запускает программу с `-provider other`
- **THEN** программа печатает предписанную ошибку в stderr и завершается с кодом 1

### Requirement: Безопасная аутентификация и completion

Для выбранного провайдера программа SHALL читать только его key environment variable. При отсутствии ключа она MUST напечатать `error: <KEY_ENV> environment variable is not set`, завершиться с кодом 1 и никогда не выводить значение ключа. Успешный запрос SHALL использовать выбранные endpoint, model и provider-specific request fields.

#### Scenario: Отсутствует ключ выбранного профиля
- **WHEN** для выбранного provider profile не задана его environment variable
- **THEN** HTTP-запрос не выполняется и программа завершается с безопасной ошибкой

### Requirement: Отображение usage и стоимости

После успешного основного ответа программа SHALL показать provider/model, локальные current/history/sent/response tokens с label counter, API prompt/completion/total tokens и стоимость. API usage SHALL считаться авторитетным для billing, а локальный DeepSeek counter SHALL маркироваться estimate.

#### Scenario: API вернул cached prompt usage
- **WHEN** provider возвращает cached и uncached prompt token breakdown
- **THEN** стоимость учитывает cached input отдельно и сохранённая запись фиксирует фактический pricing tier