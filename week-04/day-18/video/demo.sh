#!/usr/bin/env bash
# Demo for week-04/day-18: periodic scheduler and persisted state.
#
#   ./week-04/day-18/video/demo.sh

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel)
source "$REPO_ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$REPO_ROOT"

DEMO_TMP=$(mktemp -d "${TMPDIR:-/tmp}/day18-demo.XXXXXX")
SERVER_PID=
cleanup() {
    stop_server
    rm -rf "$DEMO_TMP"
}
stop_server() {
    if [[ -n "$SERVER_PID" ]]; then
        # go run owns a child binary; stop both so neither is left behind.
        pkill -TERM -P "$SERVER_PID" 2>/dev/null || true
        kill -TERM "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
        SERVER_PID=
    fi
}
trap cleanup EXIT INT TERM


wait_for_server() {
    # Avoid sleep: dryrun.sh stubs it. Poll the real TCP listener instead.
    python3 -c 'import socket,time; deadline=time.monotonic()+45
while time.monotonic()<deadline:
 try:
  with socket.create_connection(("127.0.0.1",18818),timeout=.2): break
 except OSError: time.sleep(.2)
else: raise SystemExit("MCP server did not start")'
}

start_server_command='go run ./week-04/day-18 -listen 127.0.0.1:18818 -state "$DEMO_TMP/state.json" >"$DEMO_TMP/server.log" 2>&1 & SERVER_PID=$!'
start_server() {
    demo_run "$start_server_command"
    wait_for_server
}

wait_for_ticks() {
    # The scheduler ticks once per second; use real Python sleep, not demo sleep.
    python3 -c 'import time; time.sleep(5)'
}

demo_title "AI Advent Challenge — День 18" "Хотел сделать применимое к своему проекту, но так не успевал в сроки =("

demo_note 'Запускаю локальный MCP-сервер с отдельным временным состоянием; заметки читает из подключённого Obsidian Vault.'
start_server

demo_note 'Создаю расписание для темы Floats с интервалом две секунды.'
demo_run 'go run ./week-04/day-18 -mode schedule -listen 127.0.0.1:18818 -topic Floats -interval 2s'

demo_note 'Сервер работает: даю планировщику выполнить несколько тиков и смотрю реальный результат.'
wait_for_ticks
demo_run 'go run ./week-04/day-18 -mode summary -listen 127.0.0.1:18818'
demo_pause 2

demo_note 'Останавливаю сервер, не удаляя JSON, затем поднимаю его с тем же состоянием.'
stop_server
start_server

demo_note 'После перезапуска проверяю, что расписание и результаты восстановились из JSON.'
demo_run 'go run ./week-04/day-18 -mode summary -listen 127.0.0.1:18818'
demo_outro 'week-04/day-18'
