## MODIFIED Requirements

### Requirement: Выбор провайдера при запуске

Программа SHALL принимать `-db` с default `agent.db` и `-provider` с default `groq`. Она SHALL поддерживать только `groq`, `deepseek` и `local`; неизвестное значение MUST завершать процесс с `error: unknown provider %q (expected groq, deepseek or local)`.

#### Scenario: Неизвестный провайдер
- **WHEN** пользователь запускает программу с `-provider other`
- **THEN** программа печатает предписанную ошибку в stderr и завершается с кодом 1

#### Scenario: Локальный провайдер
- **WHEN** пользователь запускает программу с `-provider local`
- **THEN** программа работает с локальным OpenAI-compatible endpoint и его окном контекста, не обращаясь к внешним API

### Requirement: Безопасная аутентификация и completion

Для выбранного провайдера программа SHALL читать только его key environment variable. При отсутствии ключа она MUST напечатать `error: <KEY_ENV> environment variable is not set`, завершиться с кодом 1 и никогда не выводить значение ключа. Профиль MAY не требовать ключа: при пустой key environment variable программа SHALL не читать никакую переменную окружения и SHALL не отправлять заголовок авторизации. Успешный запрос SHALL использовать выбранные endpoint, model и provider-specific request fields.

#### Scenario: Отсутствует ключ выбранного профиля
- **WHEN** для выбранного provider profile с непустой key environment variable она не задана
- **THEN** HTTP-запрос не выполняется и программа завершается с безопасной ошибкой

#### Scenario: Профиль без ключа
- **WHEN** выбран provider profile, не требующий ключа
- **THEN** программа запускается без чтения переменных окружения ключей и выполняет completion без заголовка авторизации

### Requirement: Отображение usage и стоимости

После успешного основного ответа программа SHALL показать provider/model, локальные current/history/sent/response tokens с label counter, API prompt/completion/total tokens и стоимость. API usage SHALL считаться авторитетным для billing, а локальные counters, не совпадающие с токенизатором модели, SHALL маркироваться estimate. Профиль, выполняемый на машине пользователя, SHALL записывать нулевые input и output costs и pricing tier `local`.

#### Scenario: API вернул cached prompt usage
- **WHEN** provider возвращает cached и uncached prompt token breakdown
- **THEN** стоимость учитывает cached input отдельно и сохранённая запись фиксирует фактический pricing tier

#### Scenario: Ответ локального профиля
- **WHEN** основной ответ получен от профиля, выполняемого локально
- **THEN** строка статуса показывает `cost=$0.000000`, а сохранённый API-вызов имеет tier `local` и нулевые суммы

## ADDED Requirements

### Requirement: Достижимое переполнение контекста на локальном профиле

Локальный профиль SHALL объявлять окно контекста 2048 токенов и резерв основного completion 512 токенов, чтобы переполнение наступало в обычном диалоге, а не только при вставке искусственно большого текста. Guard-проверка перед основным вызовом SHALL применяться к нему так же, как к остальным профилям: при `prompt + reserve > context window` LLM MUST не вызываться, а TUI MUST показать ошибку переполнения с перечислением стратегий-выходов.

#### Scenario: Диалог перерастает окно локальной модели
- **WHEN** в режиме `full` накоплено достаточно сообщений, чтобы prompt плюс резерв превысили 2048 токенов
- **THEN** запрос не отправляется, а TUI показывает `context overflow: prompt <N> + reserve <N> exceeds <N>; use /mode summary, /mode sliding, or /mode facts`

#### Scenario: Смена стратегии возвращает диалог в окно
- **WHEN** после переполнения пользователь переключается на `sliding` с небольшим `window`
- **THEN** собранный prompt снова помещается в окно и следующий turn выполняется нормально
