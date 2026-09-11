## Purpose

Определяет долговечное локальное состояние нескольких чатов, независимых веток и измерений выполненных API-вызовов.

## ADDED Requirements

### Requirement: Восстановление диалога

Программа SHALL создать `Chat 1` с веткой `main`, mode `full` и window 10 при пустой базе. После перезапуска она MUST восстановить последний обновлённый чат, его сохранённую активную ветку, mode, window и полный transcript выбранной lineage.

#### Scenario: Повторное открытие базы
- **WHEN** успешный turn сохранён, приложение закрыто и открыто с тем же `-db`
- **THEN** предыдущие сообщения и состояние активной ветки доступны до нового ввода

### Requirement: Неизменяемая lineage и fork

Каждое новое сообщение SHALL ссылаться на предыдущее, а fork MUST использовать checkpoint head без копирования prefix. Две ветки от одного checkpoint SHALL иметь общий prefix и не видеть suffix друг друга.

#### Scenario: Два fork одного checkpoint
- **WHEN** пользователь создаёт option-a и option-b от одного checkpoint и отправляет в них разные сообщения
- **THEN** каждый transcript показывает общий prefix и только собственный suffix

### Requirement: Атомарный успешный turn

Успешный turn MUST сохранить user и assistant messages, branch head, обновлённые facts/summary и метрики всех вызовов как единую транзакцию. При ошибке completion или сохранения частичный turn MUST отсутствовать.

#### Scenario: Ошибка внешнего completion
- **WHEN** completion завершается ошибкой
- **THEN** transcript и branch head не получают новое пользовательское или assistant сообщение

### Requirement: Историческая статистика

Каждый успешный main или auxiliary API call SHALL сохранять provider, model, strategy, kind, counter label, usage и стоимость с pricing tier. Статистика MUST группировать записи без смешения разных provider/model.

#### Scenario: Несколько provider profile
- **WHEN** одна база содержит вызовы Groq и DeepSeek
- **THEN** агрегированная статистика показывает их отдельными группами