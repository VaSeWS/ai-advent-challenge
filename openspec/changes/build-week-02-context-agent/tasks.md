## 1. Основа программы

- [x] 1.1 Добавить пять pinned Go-зависимостей и создать `week-02` package layout; verify `go build ./week-02`.
- [x] 1.2 Реализовать provider registry, token counters, usage normalization и pricing tiers; verify focused profile, client и pricing tests.
- [x] 1.3 Реализовать `main` flags, безопасную проверку environment key и fail-fast lifecycle; verify missing-key и unknown-provider smoke commands.

## 2. Долговечное состояние

- [x] 2.1 Реализовать versioned SQLite schema, WAL/foreign keys и initial Chat 1/main state; verify temp database reopen test.
- [x] 2.2 Реализовать recursive lineage, chat/branch selection, checkpoints и forks; verify shared-prefix/no-suffix-leak test.
- [x] 2.3 Реализовать atomic turn persistence, summaries, facts и API-call aggregates; verify rollback and provider/model stats tests.

## 3. Агент и стратегии

- [x] 3.1 Реализовать OpenAI-compatible HTTP client и provider-neutral completion contracts; verify `httptest` usage normalization.
- [x] 3.2 Реализовать full, sliding и branching prompt builders и overflow guard; verify exact window boundaries and no-completer overflow test.
- [x] 3.3 Реализовать summary batching, facts rebuild/update validation и `Agent.Send`; verify scripted successful persisted turn and invalid facts tests.

## 4. Терминальный интерфейс

- [x] 4.1 Реализовать command parsing для chats, modes, branches, facts, stats, help и quit; verify command contract tests.
- [x] 4.2 Реализовать Bubble Tea alternate-screen TUI с responsive sidebar, transcript, textarea, busy spinner и key bindings; verify interactive local smoke.

## 5. Документация и проверка

- [x] 5.1 Документировать week-02 и обновить root README/.gitignore; verify documented commands match CLI and help.
- [x] 5.2 Добавить только контрактные focused tests, отформатировать код и выполнить `go test ./week-02`, `go build ./...`.
- [ ] 5.3 Выполнить Groq live TUI smoke с temporary DB при доступном exported key; verify chat restore and mode/branch interactions.
- [ ] 5.4 Выполнить DeepSeek live smoke при доступном key либо missing-key path; verify status/counter label/pricing evidence and remove temporary artifacts.