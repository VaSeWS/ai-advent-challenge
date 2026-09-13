## 1. Профиль и токены

- [x] 1.1 Добавить profile `local` в `providerProfiles` (endpoint Ollama `/v1/chat/completions`, модель `llama3-ctx2k`, пустая `KeyEnv`, окно 2048, main 512, auxiliary 256, поле `max_tokens`, counter `cl100k_base`) и проверить `go test ./week-02/...` с тестом, читающим поля профиля
- [x] 1.2 Расширить текст ошибки `providerProfile` до `expected groq, deepseek or local` и проверить тестом на неизвестное значение
- [x] 1.3 Вернуть из `Price` нулевые input/output и tier `local` для профиля `local`, проверить тестом на ненулевом usage
- [x] 1.4 Добавить counter для encoding `cl100k_base` с label `cl100k-approx` и `Exact() == false`, проверить тестом, что `newTokenCounter` возвращает его для профиля `local` и counter считает непустой русский текст

## 2. Запуск без ключа

- [x] 2.1 Пропускать чтение key environment variable в `run`, когда `KeyEnv` профиля пуст, и проверить, что `go run . -provider local` стартует без экспортированных ключей
- [x] 2.2 Не отправлять заголовок `Authorization`, когда ключ пуст, и проверить тестом клиента на отсутствие заголовка

## 3. Документация

- [x] 3.1 Описать в `week-02/README.md` профиль `local`, создание модели `llama3-ctx2k` из Modelfile с `PARAMETER num_ctx 2048` и запуск `go run . -provider local`; проверить, что таблица профилей содержит три колонки
- [x] 3.2 Добавить в README пояснение, почему переполнение показывается на локальной модели (окно 2048 достижимо обычным диалогом, нет ключей и rate limit, сырой вызов молча обрезает промпт) — проверить наличием раздела

## 4. Проверка сценария

- [x] 4.1 Прогнать `go build ./...` и `go test ./week-02/...` — оба зелёные
- [x] 4.2 Вручную: `-provider local`, режим `full`, несколько реплик до сообщения `context overflow: prompt <N> + reserve <N> exceeds 2048`, затем `/mode sliding` + `/window 2` — следующий turn проходит
