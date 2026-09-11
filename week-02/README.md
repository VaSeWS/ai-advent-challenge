# Week 02 — Context Agent

Дни 6–10 объединены в одну самостоятельную программу `week-02`: полноэкранный терминальный агент с несколькими чатами, SQLite-персистентностью, учётом токенов и стоимости, ветками диалога и пятью способами собирать контекст. Это не пять программ по дням и не общий фреймворк для следующих недель.

## Что хранится и как устроено

Программа запускается из корневого Go-модуля как пакет `week-02`. SQLite — источник состояния: в ней хранятся чаты, ветки, неизменяемые сообщения со ссылкой на предыдущее сообщение, резюме, факты, checkpoints и записи каждого API-вызова. Ветка указывает на head сообщения, поэтому fork разделяет общий префикс истории без его копирования. При первом запуске создаются `Chat 1`, ветка `main`, режим `full` и окно `10`; при следующем запуске восстанавливается последний активный чат и его ветка.

Границы компонентов намеренно разделены:

- **App/TUI** показывает чаты и transcript, принимает ввод и slash-команды. Она не формирует HTTP-запросы и не знает API-ключей.
- **Agent** превращает пользовательский ввод в контекст выбранной стратегии, вызывает LLM, считает метрики и атомарно сохраняет успешный turn. Ошибка внешнего API или хранилища завершает программу; ошибка команды или переполнение контекста остаётся сообщением в TUI.
- **Store** отвечает за SQLite, восстановление lineage, чаты, ветки, checkpoints и атомарную запись сообщений, производных данных и API-метрик.
- **Provider profile** локализует различия Groq и DeepSeek: endpoint, модель, переменную ключа, лимиты, поля запроса, счётчик и тарифы. Agent, Store, стратегии и TUI от конкретного провайдера не зависят.
- **OpenAI-compatible client** делает один явный HTTP-запрос Chat Completions с Bearer-авторизацией и нормализует `usage` ответа.

Ключи берутся только из окружения через `GROQ_API_KEY` или `DEEPSEEK_API_KEY`. Программа не читает `.env` и никогда не выводит ключ в интерфейс, stderr, базу или документацию.

## Запуск

По умолчанию используется Groq и файл базы `agent.db` в текущем каталоге:

```sh
cd week-02
export GROQ_API_KEY='ваш_ключ_в_окружении'
go run .
```

Можно явно задать путь к базе и провайдера:

```sh
cd week-02
export GROQ_API_KEY='ваш_ключ_в_окружении'
go run . -db /путь/к/agent.db -provider groq
```

Для DeepSeek экспортируется другой ключ и выбирается другой профиль:

```sh
cd week-02
export DEEPSEEK_API_KEY='ваш_ключ_в_окружении'
go run . -db /путь/к/agent.db -provider deepseek
```

Допустимы только `-provider groq` и `-provider deepseek`. При отсутствии ключа выбранного профиля программа завершается с понятной ошибкой, не подставляя ключи из файлов конфигурации.

## Функции и точки входа

`week-02` — исполняемый `package main`, а не поддерживаемая Go-библиотека: ниже приведена карта реально существующих точек интеграции и операций программы, а не обещание стабильного API.

### Запуск и интерфейс

- `func main()` запускает программу; пользовательский вызов — `go run . [-db <путь>] [-provider <groq|deepseek>]`.
- Флаг `-db` имеет значение по умолчанию `agent.db` и задаёт путь к SQLite; `-provider` по умолчанию `groq` и принимает только `groq` либо `deepseek`.
- `func NewApp(store *Store, agent *Agent, profile ProviderProfile) tea.Model` создаёт полноэкранную TUI с восстановленным активным чатом.
- `func NewCommandService(store *Store, agent *Agent) *CommandService` создаёт обработчик slash-команд, а `func (s *CommandService) Execute(ctx context.Context, chatID, branchID int64, input string) (CommandResult, error)` разбирает и выполняет введённую команду.
- Полный список операций интерфейса показывает `/help`; он остаётся источником истины для синтаксиса slash-команд.

### Сборка Agent, Store и клиента

- `func OpenStore(path string) (*Store, error)` открывает и мигрирует SQLite-хранилище, создавая начальный чат при новой базе; `func (s *Store) Close() error` освобождает его соединение.
- `type OpenAICompatibleClient struct` — встроенная HTTP-реализация клиента Chat Completions; `func NewOpenAICompatibleClient(profile ProviderProfile, key string) *OpenAICompatibleClient` создаёт её для уже полученного из окружения ключа.
- `type Agent struct` координирует сборку контекста, completion и атомарную запись turn; `func NewAgent(store *Store, llm Completer, tokens TokenCounter, profile ProviderProfile) *Agent` связывает его зависимости.
- `func (a *Agent) Send(ctx context.Context, chatID, branchID int64, input string) (TurnResult, error)` выполняет один пользовательский turn и атомарно сохраняет успешный результат; `func (a *Agent) RebuildFacts(ctx context.Context, chatID, branchID int64) error` заново строит facts текущей lineage.
- `type Completer interface { Complete(context.Context, CompletionRequest) (Completion, error) }` — контракт LLM-клиента; `func (c *OpenAICompatibleClient) Complete(ctx context.Context, req CompletionRequest) (Completion, error)` — его встроенная OpenAI-compatible реализация.

### Хранилище и его данные

- `store.ActiveChat()`, `store.Chat(chatID)`, `store.Branch(chatID, branchID)`, `store.Chats()`, `store.Branches(chatID)` и `store.Lineage(branchID)` читают активный или указанный разговор, ветки и историю.
- `store.CreateChat(title)`, `store.UseChat(chatID)`, `store.SwitchBranch(chatID, name)`, `store.SetBranchMode(branchID, strategy)` и `store.SetWindow(branchID, windowSize)` создают либо выбирают разговор и изменяют настройки ветки.
- `store.Summary(branchID)`, `store.SaveSummary(branchID, update)`, `store.Facts(branchID)`, `store.ReplaceFacts(branchID, facts)` и `store.SaveFactsRebuild(chatID, branchID, facts, calls)` управляют производной памятью ветки.
- `store.CreateCheckpoint(branchID, name)`, `store.Checkpoint(branchID, name)` и `store.Fork(sourceBranchID, checkpointName, newBranchName)` сохраняют checkpoint и создают ветку без копирования общего префикса.
- `store.SaveTurn(input)`, `store.Stats(branchID)` и `store.AllStats()` атомарно записывают turn и возвращают учёт API-вызовов соответственно для ветки или всех чатов.
- `type Store struct` владеет долговечным графом разговоров; `type Chat struct`, `type Branch struct` и `type Message struct` описывают сохранённые чат, ветку и сообщение.
- `type Summary struct`, `type SummaryUpdate struct`, `type Fact struct`, `type Checkpoint struct` и `type SaveTurnInput struct` передают производную память, её обновления, facts, снимки веток и данные записи turn.
- `type APICall struct`, `type Usage struct`, `type StatsRow struct` и `type TurnResult struct` описывают учёт вызова, usage, агрегированную статистику и результат отправки.

### Провайдеры, токены и контекст

- `type ProviderProfile struct` задаёт параметры провайдера; `func (p ProviderProfile) Price(usage Usage, startedAt time.Time) Price` рассчитывает тариф вызова, а `type Price struct` содержит его tier и суммы input/output.
- `type TokenCounter interface { Count(string) int; Label() string; Exact() bool }` задаёт локальный счётчик; выбор профиля через `-provider` определяет используемый счётчик и переменную окружения ключа, но не раскрывает её значение.
- `func BuildMainPrompt(branch Branch, lineage []Message, summary *Summary, facts map[string]string, input string) ([]CompletionMessage, error)` собирает основной prompt выбранной стратегии.
- `func LastWindow(lineage []Message, windowSize int) []Message`, `func MessagesAfter(lineage []Message, throughMessageID int64) ([]Message, error)`, `func SummaryBatches(lineage []Message, throughMessageID int64, windowSize int) ([][]Message, error)` и `func FactsBatches(lineage []Message) [][]Message` выделяют нужные части истории.
- `func BuildSummaryPrompt(previousSummary string, batch []Message) ([]CompletionMessage, error)`, `func BuildFactsPrompt(facts map[string]string, batch []Message) ([]CompletionMessage, error)` и `func FactsSystemBlock(facts map[string]string) string` формируют вспомогательные запросы и блок facts.
- `func ParseFactsJSON(content string) (map[string]string, error)` валидирует ответ facts; `func PromptTokenCount(counter TokenCounter, prompt []CompletionMessage) int` и `func CheckContextOverflow(counter TokenCounter, profile ProviderProfile, prompt []CompletionMessage) error` считают prompt и проверяют резерв completion.
- `type CompletionMessage struct`, `type CompletionRequest struct` и `type Completion struct` описывают сообщения, запрос и результат LLM; `type CommandService struct` и `type CommandResult struct` представляют обработчик команд и его display-ready результат.

## Профили провайдеров

| Свойство | Groq | DeepSeek |
| --- | --- | --- |
| Переменная окружения | `GROQ_API_KEY` | `DEEPSEEK_API_KEY` |
| Endpoint | `https://api.groq.com/openai/v1/chat/completions` | `https://api.deepseek.com/chat/completions` |
| Модель | `openai/gpt-oss-20b` | `deepseek-flash` |
| Окно контекста | 131072 | 1000000 |
| Максимум completion для основного вызова | 2048 | 2048 |
| Максимум completion для summary/facts | 512 | 512 |
| Поле лимита в запросе | `max_completion_tokens` | `max_tokens` |
| Дополнительные параметры | `reasoning_effort="low"` | `thinking={"type":"disabled"}`, `reasoning_effort="none"` |

Перед основным запросом программа резервирует максимум основного completion: `guard_tokens + 2048` не должен превышать окно профиля. Если условие нарушено, LLM не вызывается и TUI сообщает:

```text
context overflow: prompt <N> + reserve <N> exceeds <N>; use /mode summary, /mode sliding, or /mode facts
```

## Контекстные стратегии

Во всех режимах основной запрос содержит базовое system-сообщение: «Ты полезный ассистент. Отвечай на языке пользователя.» и текущий ввод пользователя.

- **`full`** — добавляет всю raw lineage активной ветки. Это baseline: максимальное сохранение контекста и наиболее наглядный рост prompt до overflow.
- **`summary`** — хранит raw сообщения, но перед основным вызовом сворачивает старую непокрытую историю порциями по 10 сообщений, когда она старше последних `window_size` сообщений. В основной prompt попадают system-блок с резюме и raw-хвост после watermark. Резюме обязано сохранять цели, ограничения, предпочтения, решения и договорённости.
- **`sliding`** — отправляет только последние `window_size` raw сообщений активной lineage. Старые сообщения остаются в SQLite и доступны при переключении стратегии, но не уходят в этот prompt.
- **`facts`** — при входе перестраивает память фактов по полной lineage батчами по 10 сообщений; далее перед основным вызовом обновляет facts текущим вводом. В prompt передаётся структурированная память целей, ограничений, предпочтений, решений и договорённостей. Устаревшие значения должны удаляться. Хранилище принимает только ответ вида `{"facts":{"ключ":"значение"}}`.
- **`branching`** — отправляет полную lineage только активной ветки. Checkpoint фиксирует её head, summary и facts; fork создаёт ветку от checkpoint без копирования сообщений. Сестринские ветки видят общий префикс, но не последующие ответы друг друга.

Режим и размер окна принадлежат ветке. Смена режима не удаляет историю. Для `summary` устаревшая часть догоняется лениво при следующем сообщении; вход в `facts` сначала выполняет rebuild.

## Команды

Все команды вводятся в поле сообщения.

| Команда | Действие |
| --- | --- |
| `/new <title>` | Создать и сразу открыть новый чат. |
| `/chats` | Открыть или обновить sidebar и вывести id, title, mode и активную ветку чатов. |
| `/use <chat-id>` | Переключиться на чат. |
| `/mode <full|summary|sliding|facts|branching>` | Установить режим активной ветки; переход в `facts` запускает rebuild. |
| `/window <N>` | Установить положительный размер окна активной ветки. |
| `/checkpoint <name>` | В режиме `branching` сохранить checkpoint активной ветки; имя уникально в этой исходной ветке. |
| `/fork <checkpoint> <new-branch>` | В режиме `branching` создать и сразу открыть ветку от checkpoint. |
| `/branches` | Вывести имена веток и отметку активной. |
| `/switch <branch-name>` | Переключить активную ветку. |
| `/facts` | Вывести facts активной ветки в сортированном виде. |
| `/stats` | Показать метрики активной ветки, сгруппированные по strategy и kind. |
| `/stats all` | Показать метрики всех чатов. |
| `/help` | Показать эту поверхность команд. |
| `/quit` | Закрыть базу и выйти. |

Неизвестная команда сообщает `unknown command: /...; use /help`. Команды `/checkpoint` и `/fork` допустимы только в `branching`. После `/new`, `/use`, `/fork` и `/switch` transcript загружается заново, поэтому сообщения разных чатов и веток не смешиваются.

### Клавиши

- **Enter** — отправить непустой ввод.
- **Ctrl+J** — вставить перевод строки в многострочный ввод.
- **PgUp / PgDn** — прокрутить transcript.
- **Ctrl+C** — закрыть приложение, как `/quit`.

Во время обращения Agent/API отправка и изменяющие стратегию команды блокируются, отображается spinner; streaming не используется, ответ появляется после завершения completion. На терминале уже 80 колонок sidebar может быть скрыт, чтобы сохранить transcript, статус и ввод.

## Токены, usage и стоимость

Статус после основного ответа имеет форму:

```text
provider=<name>/<model> | local[<counter-label>] current=<n> history=<n> sent=<n> response=<n> | api prompt=<n> completion=<n> total=<n> | cost=$<6 decimals>
```

`current` — локальная оценка текущего ввода, `history` — вся raw lineage до него, `sent` — реально собранный strategy prompt, `response` — видимый ответ. Они различаются намеренно: например, sliding отправляет меньше, чем хранится в истории.

Для Groq локальный счётчик `o200k_harmony` точный (`exact=true`) для модели GPT-OSS. Для DeepSeek локальный `deepseek-v4-estimate` — консервативная оценка, а не billing-истина: ASCII-символы оцениваются как `ceil(0.3*n)`, не-ASCII как `ceil(0.6*n)`, а для guard берётся максимум этой оценки и числа runes. В обоих случаях авторитетные значения для биллинга и статистики — поля `usage`, возвращённые API. Клиент сохраняет prompt, completion, total, cached и uncached prompt usage каждого основного и вспомогательного вызова.

### Тарифы

Стоимость записывается вместе с выполненным API-вызовом, поэтому последующее изменение тарифа не меняет историю.

**Groq (`standard`), USD за 1M токенов:**

| Тип | Цена |
| --- | ---: |
| Cached input | $0.037 |
| Uncached input | $0.075 |
| Output | $0.30 |

**DeepSeek Flash, USD за 1M токенов:**

| Tier | Cached input | Uncached input | Output |
| --- | ---: | ---: | ---: |
| `peak` | $0.006 | $0.30 | $1.20 |
| `off-peak` | $0.003 | $0.15 | $0.60 |

Для DeepSeek `peak` определяется в UTC только с понедельника по пятницу в интервалах `[01:00, 04:00)` и `[06:00, 10:00)`; всё остальное, включая выходные и границы интервалов, — `off-peak`. Cached/uncached breakdown нормализуется из provider-specific API usage; если API не прислал breakdown, весь prompt считается uncached.

## Сценарии сравнения

Ниже — ручные сценарии использования, а не заявления о проведённом тестировании.

1. **Короткий диалог.** В `full` отправьте несколько связанных коротких реплик. Затем переключите `/mode sliding` и `/window 2`: последние две raw реплики останутся в prompt, а старые сохранятся в базе. Вернитесь в `full`, чтобы сравнить полный контекст с ограниченным окном.
2. **Длинный диалог.** В `summary` накопите более десяти сообщений, оставляя небольшой `/window`. Следующий turn создаст вспомогательное резюме старой части; `/stats` позволит увидеть отдельно расходы `summary` и `main`. В `facts` выполните `/facts` после rebuild, чтобы сравнить сжатую ключевую память с narrative summary.
3. **Overflow.** Увеличивайте объём сообщений в `full` до сообщения `context overflow`. Затем используйте `/mode summary`, `/mode sliding` или `/mode facts` для уменьшения отправляемого контекста; эти варианты предложены самой диагностикой.
4. **Все пять стратегий.** На одном и том же диалоге сравните: `full` для полного дословного прошлого, `summary` для сохранения смысловой нити, `sliding` для недавнего хвоста, `facts` для устойчивых договорённостей и `branching` для альтернативных продолжений. Сравнение корректнее проводить с одинаковыми input и window, отслеживая `sent`, API usage и cost.
5. **Ветки.** Включите `/mode branching`, создайте `/checkpoint base`, затем `/fork base option-a` и продолжите вариант A. Вернитесь на исходную ветку через `/switch main`, создайте `/fork base option-b` и продолжите вариант B. `/branches` и переключение веток должны показывать общий prefix и изолированные suffix.
