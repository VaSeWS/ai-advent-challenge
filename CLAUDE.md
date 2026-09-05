# AI Advent Challenge #9

Homework repo for a multi-week AI course. 5 tasks per week, one folder per
week/day: `week-NN/day-NN/main.go`, each a standalone `package main`.

## Stack

Go, stdlib only (`net/http` + `encoding/json`) — no third-party deps unless a
task genuinely needs one. LLM: Groq, OpenAI-compatible Chat Completions API
(`https://api.groq.com/openai/v1/chat/completions`). Fallback if Groq limits
run out: DeepSeek, same OpenAI-compatible shape.

## Rules

- Each day's task is minimal, standalone code. No shared framework/library
  across days unless a task explicitly asks for one — don't build for future
  tasks preemptively.
- `GROQ_API_KEY` read only via `os.Getenv` — no config file parsing, no
  hardcoded keys. `.env` holds the key locally for reference but is **not**
  auto-loaded by the Go code; export it in the shell before running
  (`export $(grep -v '^#' .env | xargs)` or just `export GROQ_API_KEY=...`).
- Never print, log, or commit the API key. `.env` and `.env.local` are
  gitignored — keep it that way.
- No retry/backoff/elaborate error handling — this is homework, fail fast
  with a clear stderr message and exit 1.
- `go build ./...` must pass before committing.
- Solo homework repo, no PR review flow: commit and push straight to `main`,
  no feature branches.
