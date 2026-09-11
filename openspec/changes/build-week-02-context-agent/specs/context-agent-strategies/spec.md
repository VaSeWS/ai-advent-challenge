## Purpose

Определяет пять переключаемых пользователем стратегий, которые позволяют сравнивать объём передаваемого контекста без потери raw history.

## ADDED Requirements

### Requirement: Выбор и сохранение стратегии

Активная branch SHALL хранить выбранные `full`, `summary`, `sliding`, `facts` или `branching` и положительное window size. Смена режима или окна MUST не удалять raw messages и сохраняться после перезапуска.

#### Scenario: Смена режима
- **WHEN** пользователь выбирает `sliding` и window 2
- **THEN** последующий prompt содержит только последние два raw message перед текущим вводом, а более старая история остаётся доступна в базе

### Requirement: Контекстные prompt contracts

`full` SHALL передавать system message и всю активную lineage. `summary` SHALL передавать сохранённое краткое резюме и raw messages после watermark. `facts` SHALL передавать только валидированный map важных facts. `branching` SHALL передавать полную lineage только активной branch.

#### Scenario: Summary backlog
- **WHEN** у summary-ветки есть не менее десяти непокрытых старых messages вне окна
- **THEN** агент получает краткое резюме этих сообщений перед основным completion и raw messages не удаляются

### Requirement: Проверка context limit

Перед основным API-вызовом программа MUST проверить prompt tokens вместе с provider reserve. При превышении completion SHALL не вызываться, а TUI получает `context overflow: prompt %d + reserve %d exceeds %d; use /mode summary, /mode sliding, or /mode facts`.

#### Scenario: Слишком длинный input
- **WHEN** выбранный provider context window меньше prompt плюс completion reserve
- **THEN** приложение остаётся запущенным, показывает overflow error и не сохраняет новый turn

### Requirement: Facts JSON contract

Facts rebuild и update SHALL принимать только JSON `{"facts":{"ключ":"значение"}}`; иная форма MUST отклоняться без замены существующих facts.

#### Scenario: Невалидный facts ответ
- **WHEN** auxiliary completion возвращает не соответствующий contract JSON
- **THEN** facts map и transcript не изменяются, а пользователь получает ошибку