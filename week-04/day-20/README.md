# Day 20: MCP review-plan agent and Telegram notifications

This standalone Go CLI connects to two MCP servers over stdio:

1. Day 19's `server` mode reads the configured Obsidian vault, finds due questions, builds a review plan, and saves that plan in day 19's output directory.
2. This assignment's `telegram-server` mode exposes `send_message` and sends the plan through Telegram's HTTPS Bot API.

The Groq-backed agent discovers both servers' tools and JSON input schemas, offers the full discovered tool list on every turn, and asks the model to select one tool per turn. The host enforces the sequence `find_due_questions` → `build_review_plan` → `save_plan` → `send_message`, rejects a selection that is out of order, and routes each valid selection to the server that owns it. The model must select the requested topic and date for the lookup; for subsequent calls it can use compact `questions:[]`, `plan:""`, and `text:""` placeholders, which the host replaces with the exact discovered questions or generated plan. No plan is saved or notification sent when there are no due questions. A failed tool stops the flow before any later side effect.

Пользовательская просьба формулирует только результат: «Составь план повторения темы, сохрани его и отправь мне в Telegram». Она не называет инструменты; модель видит все доступные схемы и может выбрать их сама (`tool_choice: auto`). Хост проверяет порядок действий и передаваемые данные перед вызовом MCP.

## Configure

From the repository root, export the credentials in the current shell (the program does not load `.env`):

```sh
export GROQ_API_KEY='your-groq-key'
export TELEGRAM_BOT_TOKEN='your-telegram-bot-token'
export TELEGRAM_CHAT_ID='your-chat-id'
```

The day 19 server uses `OBSIDIAN_VAULT` if set; otherwise it defaults to `~/Documents/Obsidian Vault/Interviews prep/Questions`. The vault is read-only; saved plans are written to day 19's output folder, not the vault.

## Run

```sh
go run ./week-04/day-20 agent Go 2026-09-27
```

The topic can be any vault topic, and the date must be `YYYY-MM-DD`. The CLI starts day 19 with `go run ./week-04/day-19 server` from the repository root and starts its own Telegram MCP subprocess. The Telegram bot must already be able to send messages to the configured chat. Telegram messages are limited to 4096 characters per `sendMessage`; longer plans are sent as consecutive Unicode-safe chunks, preserving the complete plan text and order. Delivery stops at the first rejected chunk and reports how many chunks succeeded, since earlier chunks cannot be recalled. The CLI prints MCP server/tool routing and confirms delivery only when Telegram's API confirms every chunk; it never prints credentials.

## Demo

For a live demo, first obtain user approval for the shot plan and ensure test credentials are available. A demo can then show the exported environment-variable names without revealing their values, the command and date/topic input, MCP tool-routing sequence, saved-plan path, and Telegram receipt in the target chat. Do not record or upload a demo without that approval.
