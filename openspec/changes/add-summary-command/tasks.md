## 1. Команда

- [x] 1.1 Добавить case `/summary` в `CommandService.Execute`, читающий `store.Summary(branchID)`, и `formatSummary`, печатающий `summary (through message <id>)` с содержимым либо `summary:\n(no summary)`; проверить тестом на пустом и заполненном резюме
- [x] 1.2 Добавить `/summary` в `helpText`; проверить, что `/help` перечисляет команду

## 2. Документация и проверка

- [x] 2.1 Добавить `/summary` в таблицу команд `week-02/README.md`
- [x] 2.2 Прогнать `go build ./...` и `go test ./week-02/...` — оба зелёные
