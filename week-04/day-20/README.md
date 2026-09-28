# Day 20 — Telegram MCP server

This standalone assignment exposes a stdio MCP server with one tool, `send_message`. It sends real messages through Telegram's HTTPS Bot API using credentials inherited from the TUI process. Telegram messages longer than its 4096 UTF-16-character limit are sent as consecutive Unicode-safe chunks. If a later chunk fails, the tool reports how many chunks were already delivered; delivery stops at the first failure.

## Configure credentials

Set credentials in the shell that will launch the agent. Enter each value silently when prompted; values are not included in command history or printed:

```sh
printf 'Groq API key: '; read -s GROQ_API_KEY; printf '\n'; export GROQ_API_KEY
printf 'Telegram bot token: '; read -s TELEGRAM_BOT_TOKEN; printf '\n'; export TELEGRAM_BOT_TOKEN
printf 'Telegram chat ID: '; read -s TELEGRAM_CHAT_ID; printf '\n'; export TELEGRAM_CHAT_ID
```

The Telegram bot must already be able to send messages to the configured chat. The agent reads `GROQ_API_KEY`; the server reads `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`. Do not include credentials in MCP command arguments or server configuration. The programs do not load `.env` files, and never print credentials.

Day 19 also reads `OBSIDIAN_VAULT` when set; otherwise it uses `~/Documents/Obsidian Vault/Interviews prep/Questions`. The vault is read-only. Plans are saved to `week-04/day-19/plan.md` in the repository, not to the vault.

## Connect both servers in the Week 04 TUI

Start the Week 04 agent from the repository root with a persistent database and a Groq provider, for example:

```sh
go run ./week-04/agent -db /path/to/week-04-agent.db -provider groq
```

Then enter these commands in the TUI:

```text
/mcp add day19 --cwd . -- go run ./week-04/day-19 server
/mcp connect day19
/mcp add telegram --cwd . -- go run ./week-04/day-20 telegram-server
/mcp connect telegram
/mcp list
```

`/mcp list` shows each connection and its discovered tools. Configuration is saved in the agent database, so enabled servers reconnect when the TUI restarts with the same database. Use `/mcp disconnect <name>` and `/mcp connect <name>` to stop and reconnect a server.

Now ask naturally in Russian, without naming tools, for example:

> Составь план повторения темы Go на 2026-09-27, сохрани его вне хранилища и отправь полный план в Telegram.

The agent can select the tools from both connected servers. A no-questions lookup does not save a plan or send a Telegram message. The host preserves results produced by earlier tool calls, saves only the returned plan, and sends that same plan rather than relying on model-supplied replacement content. A failed save or delivery is reported; Telegram's earlier successful chunks cannot be recalled.

## Run the server independently

From the repository root, `go run ./week-04/day-20 telegram-server` starts the stdio MCP endpoint. The `send_message` tool accepts `{ "text": string }` and confirms only after Telegram confirms delivery of all chunks.