# Day 19 — MCP review-plan pipeline

This standalone assignment provides a stdio MCP server and a CLI client that connects to that server and calls its tools in order. The vault is read-only: the only write is the generated plan at `week-04/day-19/plan.md`.

## Run

```sh
# One hashtag topic (exact match, case-insensitive); # is optional.
go run ./week-04/day-19 plan Go 2026-09-27

# All topics (empty topic in MCP; "all" is the CLI spelling).
go run ./week-04/day-19 plan all 2026-09-27

# Expose the MCP server over stdio for another MCP client.
go run ./week-04/day-19 server
```

The CLI date must be a valid `YYYY-MM-DD`. The server reads the vault from `OBSIDIAN_VAULT`; if unset it uses `~/Documents/Obsidian Vault/Interviews prep/Questions`. It recursively considers Markdown files with a `sr-due: YYYY-MM-DD` YAML frontmatter field, includes dates on or before the requested date, excludes missing/invalid dates, and sorts oldest due date first. Topic filtering matches a complete hashtag such as `#Go`, not a substring such as `#Golang`.

The `plan` command connects to its own server via stdio and prints every structured tool result, then the saved path and saved content. It passes `find_due_questions`' returned question objects into `build_review_plan`, and passes the returned plan text to `save_plan`.

## MCP tools

- `find_due_questions` input `{ "topic": string, "date": string }`; an empty topic selects every topic. Structured output is `{ "questions": [{ "title": string, "topic": string, "due": string, "path": string }] }`. Paths are relative to the vault.
- `build_review_plan` input `{ "questions": [...], "topic": string, "date": string }`; structured output is `{ "plan": string, "count": number }`. The plan contains titles, topics, and due dates only, never note bodies or source paths.
- `save_plan` input `{ "plan": string }`; structured output is `{ "path": string }`. The destination is fixed to `week-04/day-19/plan.md`, not selected by tool input.
