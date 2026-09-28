# Day 19 — MCP review-plan server

This standalone assignment exposes a stdio MCP server with three tools. The Obsidian vault is read-only; a generated plan is saved only to `week-04/day-19/plan.md` in the repository, never into the vault.

## Connect from the Week 04 TUI

Run these commands from the repository root, inside the TUI input:

```text
/mcp add day19 --cwd . -- go run ./week-04/day-19 server
/mcp connect day19
/mcp list
```

`/mcp list` shows the connected server and its discovered tools. To make it available after restarting the TUI, start the agent again with the same database; the enabled server reconnects automatically. `/mcp disconnect day19` disconnects it, and `/mcp connect day19` reconnects it.

Ask in Russian using a normal message, for example:

> Составь план повторения темы Go на 2026-09-27 и сохрани его.

The date must be a real `YYYY-MM-DD` date. The agent chooses the discovered tools; the user does not need to name them. If there are no matching due questions, no plan is saved. The server recursively considers Markdown files containing a `sr-due: YYYY-MM-DD` YAML frontmatter field, includes dates on or before the requested date, excludes missing or invalid dates, and sorts by oldest due date first. Topic filtering matches a complete hashtag such as `#Go` case-insensitively, not a substring such as `#Golang`.

## Environment

The server reads `OBSIDIAN_VAULT` from its inherited environment. If unset, it uses `~/Documents/Obsidian Vault/Interviews prep/Questions`. Set the variable in the shell before starting the TUI if you use a different vault; do not put secrets in MCP configuration. The server and agent do not print credentials.

For a different vault, set `OBSIDIAN_VAULT` in the shell before launching the TUI, for example `export OBSIDIAN_VAULT=/path/to/Questions`.

## MCP tools

- `find_due_questions` accepts `{ "topic": string, "date": string }`; an empty topic selects every topic. Structured output is `{ "questions": [{ "title": string, "topic": string, "due": string, "path": string }] }`. Paths are relative to the vault.
- `build_review_plan` accepts `{ "questions": [...], "topic": string, "date": string }`; output contains plan text and count. The plan contains titles, topics, and due dates only, never note bodies or source paths.
- `save_plan` accepts `{ "plan": string }`; output contains the saved repository-relative path. The destination is fixed to `week-04/day-19/plan.md` and is not selected by tool input.

The server is also independently runnable from the repository root with `go run ./week-04/day-19 server`.