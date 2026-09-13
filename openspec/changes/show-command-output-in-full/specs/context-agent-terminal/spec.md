## MODIFIED Requirements

### Requirement: Адаптивный интерактивный интерфейс

Программа SHALL использовать alternate screen и отображать transcript, textarea, provider/chat/branch/mode/window status и последние metrics/errors. При ширине менее 80 columns sidebar MUST скрываться без поломки transcript или input. Во время completion TUI MUST блокировать submit и strategy-changing commands, показывая busy state. Многострочный результат команды SHALL отображаться в пределах доли доступной высоты, а не фиксированными тремя строками, и MUST оставлять transcript не менее одной строки.

#### Scenario: Узкий терминал
- **WHEN** terminal шире́н менее 80 columns
- **THEN** transcript, input и status остаются доступны, а sidebar не отображается

#### Scenario: Многострочный результат команды
- **WHEN** команда возвращает таблицу из нескольких строк, например `/stats` с несколькими стратегиями
- **THEN** на обычном по высоте терминале видна вся таблица или её часть длиннее трёх строк, а transcript и input остаются на экране

#### Scenario: Очень низкий терминал
- **WHEN** высота терминала мала настолько, что полный результат команды не помещается
- **THEN** результат обрезается, transcript сохраняет не менее одной строки, а input остаётся доступен
