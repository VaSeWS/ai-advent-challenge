#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && git rev-parse --show-toplevel)
source "$ROOT/.claude/skills/record-task-demo/scripts/demolib.sh"
cd "$ROOT"

if [ ! -t 0 ] || [ ! -t 1 ]; then
    TERM=xterm
    export TERM
fi

TUI_SESSION=${TUI_SESSION:-day26-local-$$}
PROMPTS="$ROOT/week-06/day-26/prompts.json"
ARTIFACT_DIR=${DEMO_ARTIFACT_DIR:-}
DAY26_MODEL_ID='mlx-community/Qwen3.5-4B-4bit'
DAY26_MODEL_REVISION='0e7ffd5c629ef7719d4cbc04069232580bfa9d9c'

DAY26_ENDPOINT=${DAY26_ENDPOINT:-http://127.0.0.1:8080/v1/chat/completions}
printf -v quoted_chat_endpoint '%q' "$DAY26_ENDPOINT"

if [ -n "${DAY26_HISTORY_DB:-}" ]; then
    if [ -n "${DAY26_DB:-}" ]; then
        printf 'error: DAY26_DB cannot be combined with DAY26_HISTORY_DB; history must use its exclusive private clone\n' >&2
        exit 1
    fi
    db_dir=$(mktemp -d "${TMPDIR:-/tmp}/day26-history.XXXXXX")
    chmod 700 "$db_dir"
    db="$db_dir/day26.sqlite"
    history_stage="$db_dir/staging.sqlite"
    history_baseline="$db_dir/source-baseline.json"
    if ! python3 - "$DAY26_HISTORY_DB" "$history_stage" "$db" "$history_baseline" "$PROMPTS" "$ARTIFACT_DIR" <<'PY'
import hashlib
import json
import os
import sqlite3
import stat
import sys
from pathlib import Path

source_arg, stage_arg, clone_arg, baseline_arg, prompts_arg, artifacts_arg = sys.argv[1:]
source_input = Path(source_arg).expanduser().absolute()
if source_input.is_symlink():
    raise SystemExit(f"error: DAY26_HISTORY_DB must not be a symlink: {source_input}")
source = source_input.resolve(strict=True)
stage = Path(stage_arg).absolute()
clone = Path(clone_arg).absolute()
baseline_path = Path(baseline_arg).absolute()
artifacts = Path(artifacts_arg).expanduser().absolute() if artifacts_arg else None
if not stat.S_ISREG(source.lstat().st_mode):
    raise SystemExit(f"error: DAY26_HISTORY_DB must name an existing regular SQLite file: {source}")
if source == stage or source == clone or stage == clone:
    raise SystemExit("error: history source, staging snapshot and private clone must be distinct files")
clone_parent = clone.parent.resolve(strict=True)
stage_parent = stage.parent.resolve(strict=True)
if stage_parent != clone_parent or stat.S_IMODE(stage_parent.stat().st_mode) != 0o700:
    raise SystemExit("error: history staging and clone must be in the private mode-0700 directory")
if artifacts is not None:
    artifacts_real = artifacts.resolve(strict=False)
    for path in (source, Path(f"{source}-wal"), Path(f"{source}-shm")):
        try:
            path.resolve(strict=False).relative_to(artifacts_real)
        except ValueError:
            continue
        raise SystemExit(f"error: DEMO_ARTIFACT_DIR must not contain history source or sidecar: {path}")
    for private_path in (stage_parent,):
        try:
            private_path.relative_to(artifacts_real)
        except ValueError:
            pass
        else:
            raise SystemExit("error: private history staging must be outside DEMO_ARTIFACT_DIR")

def identity_from_stat(info):
    return {
        "present": True,
        "device": info.st_dev,
        "inode": info.st_ino,
        "mode": stat.S_IMODE(info.st_mode),
        "size": info.st_size,
        "mtime_ns": info.st_mtime_ns,
        "ctime_ns": info.st_ctime_ns,
    }

def fingerprint(path):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return {"present": False}
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise SystemExit(f"error: history source or sidecar must be a regular non-symlink file: {path}")
    return identity_from_stat(info)

def read_digest(path, expected):
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    fd = os.open(path, flags)
    try:
        opened = os.fstat(fd)
        if not stat.S_ISREG(opened.st_mode) or identity_from_stat(opened) != expected:
            raise SystemExit(f"error: history source or sidecar changed before hashing: {path}")
        value = hashlib.sha256()
        with os.fdopen(fd, "rb", closefd=False) as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                value.update(block)
        if identity_from_stat(os.fstat(fd)) != expected or fingerprint(path) != expected:
            raise SystemExit(f"error: history source or sidecar changed while hashing: {path}")
        return value.hexdigest()
    finally:
        os.close(fd)

def source_snapshot():
    paths = {
        "db": source,
        "wal": Path(f"{source}-wal"),
        "shm": Path(f"{source}-shm"),
    }
    identities = {label: fingerprint(path) for label, path in paths.items()}
    result = {}
    for label, path in paths.items():
        identity = identities[label]
        result[label] = {
            "path": str(path),
            "identity": identity,
            "sha256": read_digest(path, identity) if identity["present"] else None,
        }
    if {label: fingerprint(path) for label, path in paths.items()} != identities:
        raise SystemExit("error: DAY26_HISTORY_DB source identity or sidecar presence changed during snapshot")
    return result

if fingerprint(stage)["present"] or fingerprint(clone)["present"] or baseline_path.exists():
    raise SystemExit("error: history staging, clone and baseline paths must be new and exclusive")
before = source_snapshot()
for label in ("db", "wal", "shm"):
    entry = before[label]
    if not entry["identity"]["present"]:
        continue
    source_path = Path(entry["path"])
    target = stage if label == "db" else Path(f"{stage}-{label}")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    source_fd = os.open(source_path, flags)
    try:
        opened = os.fstat(source_fd)
        expected = entry["identity"]
        if not stat.S_ISREG(opened.st_mode) or identity_from_stat(opened) != expected:
            raise SystemExit(f"error: history source changed before copying: {source_path}")
        target_fd = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        value = hashlib.sha256()
        try:
            with os.fdopen(source_fd, "rb", closefd=False) as reader, os.fdopen(target_fd, "wb", closefd=False) as writer:
                for block in iter(lambda: reader.read(1024 * 1024), b""):
                    value.update(block)
                    writer.write(block)
                writer.flush()
                os.fsync(writer.fileno())
        finally:
            os.close(target_fd)
        if value.hexdigest() != entry["sha256"]:
            raise SystemExit(f"error: history source changed while copying: {source_path}")
        if identity_from_stat(os.fstat(source_fd)) != expected or fingerprint(source_path) != expected:
            raise SystemExit(f"error: history source identity changed while copying: {source_path}")
    finally:
        os.close(source_fd)
after_copy = source_snapshot()
if before != after_copy:
    raise SystemExit("error: DAY26_HISTORY_DB source identity, bytes or sidecar presence changed during staging")
if read_digest(stage, fingerprint(stage)) != before["db"]["sha256"]:
    raise SystemExit("error: staged history database digest does not match its source")
for label in ("wal", "shm"):
    staged_sidecar = Path(f"{stage}-{label}")
    expected = before[label]["sha256"]
    if expected is None:
        if staged_sidecar.exists():
            raise SystemExit(f"error: unexpected staged SQLite {label.upper()} sidecar")
    elif read_digest(staged_sidecar, fingerprint(staged_sidecar)) != expected:
        raise SystemExit(f"error: staged SQLite {label.upper()} digest does not match its source")

prompts = {item["id"]: item["text"] for item in json.loads(Path(prompts_arg).read_text(encoding="utf-8"))}
expected_titles = {
    1: "Chat 1",
    2: "Day 26 — independent prompt 2",
    3: "Day 26 — independent prompt 3",
}
with sqlite3.connect(stage.as_uri(), uri=True) as staged:
    staged.execute("PRAGMA query_only = ON")
    chats = staged.execute("SELECT id, title, active_branch_id FROM chats ORDER BY id").fetchall()
    branches = staged.execute("SELECT id, chat_id, head_message_id FROM branches ORDER BY id").fetchall()
    messages = staged.execute("SELECT chat_id, role, content FROM messages ORDER BY chat_id, id").fetchall()
    calls = staged.execute("SELECT chat_id, kind, provider, model FROM api_calls ORDER BY chat_id, id").fetchall()
    mcp_count = staged.execute("SELECT COUNT(*) FROM mcp_servers").fetchone()[0]
    expected_chats = [(chat_id, title, chat_id) for chat_id, title in expected_titles.items()]
    if chats != expected_chats:
        raise SystemExit("error: DAY26_HISTORY_DB must contain exactly the approved chats 1–3")
    if branches != [(1, 1, 2), (2, 2, 4), (3, 3, 6)]:
        raise SystemExit("error: DAY26_HISTORY_DB must contain the three complete original chat branches")
    def snapshot_identity(identifier):
        if not isinstance(identifier, str) or not identifier:
            raise SystemExit("error: API-call model identifier is missing or invalid")
        snapshot_path = Path(identifier)
        if not snapshot_path.is_absolute() or snapshot_path.parent.name != "snapshots":
            raise SystemExit(f"error: API-call model is not an absolute Hugging Face snapshot path: {identifier}")
        repository_dir = snapshot_path.parent.parent.name
        if not repository_dir.startswith("models--"):
            raise SystemExit(f"error: API-call model path has no Hugging Face repository directory: {identifier}")
        repository_parts = repository_dir[len("models--"):].split("--", 1)
        if len(repository_parts) != 2 or not all(repository_parts):
            raise SystemExit(f"error: API-call model path has an invalid repository directory: {identifier}")
        revision = snapshot_path.name
        if len(revision) != 40 or any(char not in "0123456789abcdef" for char in revision):
            raise SystemExit(f"error: API-call model path has an invalid pinned revision: {identifier}")
        return {
            "model_id": f"{repository_parts[0]}/{repository_parts[1]}",
            "revision": revision,
            "snapshot_path": str(snapshot_path),
        }

    if len(messages) != 6 or len(calls) != 6 or {chat_id for chat_id, _, _, _ in calls} != {1, 2, 3}:
        raise SystemExit("error: DAY26_HISTORY_DB must contain exactly 6 original messages and 6 API calls")
    historical_models = {model for _, _, _, model in calls}
    if len(historical_models) != 1:
        raise SystemExit("error: historical API calls must record one consistent model snapshot")
    historical_model = next(iter(historical_models))
    historical_identity = snapshot_identity(historical_model)
    if (
        historical_identity["model_id"] != "mlx-community/Qwen3.8-27B-4bit"
        or historical_identity["revision"] != "10c35caafbb80f7dc6a7a432cdd11af10a6d4818"
    ):
        raise SystemExit("error: historical API calls must retain the pinned Qwen3.8-27B snapshot")
    for chat_id in (1, 2, 3):
        chat_calls = [call for call in calls if call[0] == chat_id]
        if (
            sorted(kind for _, kind, _, _ in chat_calls) != ["main", "memory"]
            or any(provider != "local" or model != historical_model for _, _, provider, model in chat_calls)
        ):
            raise SystemExit("error: each historical chat must retain one local main and memory call on its recorded 27B snapshot")
    if mcp_count:
        raise SystemExit("error: DAY26_HISTORY_DB contains MCP servers; refusing to enable MCP from history")
    clone_fd = os.open(clone, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(clone_fd)
    with sqlite3.connect(clone) as private_copy:
        staged.backup(private_copy)
        clone_chats = private_copy.execute("SELECT id, title, active_branch_id FROM chats ORDER BY id").fetchall()
        clone_branches = private_copy.execute("SELECT id, chat_id, head_message_id FROM branches ORDER BY id").fetchall()
        clone_messages = private_copy.execute("SELECT chat_id, role, content FROM messages ORDER BY chat_id, id").fetchall()
        clone_calls = private_copy.execute("SELECT chat_id, kind, provider, model FROM api_calls ORDER BY chat_id, id").fetchall()
        clone_mcp = private_copy.execute("SELECT COUNT(*) FROM mcp_servers").fetchone()[0]
if (clone_chats, clone_branches, clone_messages, clone_calls, clone_mcp) != (chats, branches, messages, calls, 0):
    raise SystemExit("error: private clone logical contents do not match the validated staged source")
with open(baseline_path, "x", encoding="utf-8") as baseline:
    json.dump({"source": str(source), "snapshot": before}, baseline, indent=2)
    baseline.write("\n")
os.chmod(baseline_path, 0o600)
os.chmod(clone, 0o600)
print(f"Validated staged copy of three original chats, six messages and six API calls; source and sidecars unchanged: {before}")
PY
    then
        exit 1
    fi
    history_mode=1
else
    history_mode=0
    if [ -n "${DAY26_DB:-}" ]; then
        db=$DAY26_DB
        if [ -e "$db" ] && [ -s "$db" ]; then
            printf 'error: DAY26_DB must be a new path or an empty file; refusing to reuse or overwrite: %s\n' "$db" >&2
            exit 1
        fi
        if [ -e "$db-wal" ] || [ -e "$db-shm" ]; then
            printf 'error: DAY26_DB has SQLite sidecar files; refusing to reuse it: %s\n' "$db" >&2
            exit 1
        fi
        mkdir -p "$(dirname "$db")"
    else
        db_dir=$(mktemp -d "${TMPDIR:-/tmp}/day26-agent.XXXXXX")
        db="$db_dir/day26.sqlite"
    fi
fi
if [ -n "$ARTIFACT_DIR" ]; then
    if ! python3 - "$db" "$ARTIFACT_DIR" <<'PY'
import sys
from pathlib import Path

db = Path(sys.argv[1]).absolute()
artifact_dir = Path(sys.argv[2]).resolve()
db_paths = (db, db.resolve())
for db_path in db_paths:
    for source in (db_path, Path(f"{db_path}-wal"), Path(f"{db_path}-shm")):
        try:
            source.resolve().relative_to(artifact_dir)
        except ValueError:
            continue
        print(
            f"error: artifact directory would contain the SQLite source or sidecar: {source}",
            file=sys.stderr,
        )
        raise SystemExit(1)
PY
    then
        exit 1
    fi
    mkdir -p "$(dirname "$ARTIFACT_DIR")"
    if ! mkdir "$ARTIFACT_DIR"; then
        printf 'error: DEMO_ARTIFACT_DIR must be a new directory; refusing to overwrite existing artifacts: %s\n' "$ARTIFACT_DIR" >&2
        exit 1
    fi
    ARTIFACT_DIR=$(cd "$ARTIFACT_DIR" && pwd -P)
fi
if [ "$history_mode" -eq 1 ]; then
    demo_title "AI Advent Challenge — Дни 26–27" "Qwen3.5 4B LIVE; история Qwen3.8-27B"
    demo_note 'Ниже: три архивных ответа Qwen3.8-27B, затем отдельный новый LIVE-запрос на Qwen3.5-4B.'
else
    demo_title "AI Advent Challenge — День 26" "Три независимых LIVE-запроса Qwen3.5-4B"
    demo_note 'Все три независимых запроса в этой сессии отправляются текущей модели Qwen3.5-4B.'
fi
demo_run 'curl -fsS http://127.0.0.1:8080/v1/models'
printf 'Conversation database: %s\n' "$db"

status_file=$(mktemp "${TMPDIR:-/tmp}/day26-demo-status.XXXXXX")
cleanup() {
    local exit_status=$?
    demo_tui_stop
    rm -f "$status_file"
    if [ "$exit_status" -ne 0 ] && [ -n "${provenance_path:-}" ]; then
        rm -f "$provenance_path"
    fi
}
trap cleanup EXIT

wait_for() {
    local pattern=$1 timeout=${2:-90}
    demo_tui_wait_for "$pattern" "$timeout" || {
        printf 'error: TUI did not show expected state: %s\n' "$pattern" >&2
        return 1
    }
}

prompt_for() {
    python3 -c 'import json,sys; print(next(p["text"] for p in json.load(open(sys.argv[1], encoding="utf-8")) if p["id"] == sys.argv[2]))' "$PROMPTS" "$1"
}

capture_turn_pages() {
    local turn=$1 previous='' current='' page=0

    while [ "$page" -lt 64 ]; do
        current=$(demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null) || return 1
        [ "$current" = "$previous" ] && break
        page=$((page + 1))
        if [ -n "$ARTIFACT_DIR" ]; then
            printf '%s\n' "$current" > "$ARTIFACT_DIR/turn-${turn}-page-$(printf '%02d' "$page").txt"
        fi
        demo_pause "${DAY26_PAGE_PAUSE:-4}"
        previous=$current
        demo_tui_key PageUp
        /bin/sleep 0.2
    done
    if [ -n "$ARTIFACT_DIR" ]; then
        demo_tmux capture-pane -t "$TUI_SESSION" -p -S - > "$ARTIFACT_DIR/turn-${turn}-scrollback.txt" || return 1
    fi
    for ((i = 0; i < page + 1; i++)); do
        demo_tui_key PageDown
    done
    /bin/sleep 0.2
}

wait_turn() {
    local turn=$1 expected=$2 timeout=10920 started now pane completed
    started=$(date +%s)
    while true; do
        pane=$(demo_tmux capture-pane -t "$TUI_SESSION" -p 2>/dev/null) || return 1
        case "$pane" in
            *'Request failed.'*|*'turn persistence failed.'*|*'assistant turn incomplete.'*)
                printf 'error: Day26 turn %s failed; captured TUI follows:\n%s\n' "$turn" "$pane" >&2
                if [ -n "$ARTIFACT_DIR" ]; then
                    printf '%s\n' "$pane" > "$ARTIFACT_DIR/turn-${turn}-failure.txt"
                fi
                return 1
                ;;
            *'Response received.'*)
                completed=$(python3 -c 'import sqlite3,sys; db=sqlite3.connect(sys.argv[1]); print(db.execute("SELECT COUNT(*) FROM messages WHERE role = ?", ("assistant",)).fetchone()[0])' "$db") || return 1
                if [ "$completed" -lt "$expected" ]; then
                    now=$(date +%s)
                    if [ $((now - started)) -ge "$timeout" ]; then
                        printf 'error: timed out waiting for persisted turn %s after %s seconds\n' "$turn" "$timeout" >&2
                        return 1
                    fi
                    /bin/sleep 1
                    continue
                fi
                printf 'Turn %s completed: Response received.\n' "$turn"
                if [ -n "$ARTIFACT_DIR" ]; then
                    printf '%s\n' "$pane" > "$ARTIFACT_DIR/turn-${turn}-result.txt"
                fi
                capture_turn_pages "$turn" || return 1
                return 0
                ;;
        esac
        now=$(date +%s)
        if [ $((now - started)) -ge "$timeout" ]; then
            printf 'error: timed out waiting for turn %s after %s seconds; captured TUI follows:\n%s\n' "$turn" "$timeout" "$pane" >&2
            if [ -n "$ARTIFACT_DIR" ]; then
                printf '%s\n' "$pane" > "$ARTIFACT_DIR/turn-${turn}-timeout.txt"
            fi
            return 1
        fi
        /bin/sleep 1
    done
}

MLX_VENV=${DAY26_VENV:-"$HOME/.local/share/ai-advent/day26-mlx"}
if [ ! -x "$MLX_VENV/bin/python" ]; then
    printf 'error: Day26 MLX Python environment not found at %s\n' "$MLX_VENV" >&2
    exit 1
fi
model_path=$("$MLX_VENV/bin/python" - "$DAY26_MODEL_ID" "$DAY26_MODEL_REVISION" <<'PY'
import sys
from pathlib import Path
from huggingface_hub import snapshot_download

repo_id, revision = sys.argv[1:]
print(Path(snapshot_download(repo_id=repo_id, revision=revision, local_files_only=True)).resolve())
PY
)
printf -v quoted_model '%q' "$model_path"
printf -v quoted_db '%q' "$db"
demo_tui_start "go run ./week-04/agent -provider local -endpoint $quoted_chat_endpoint -model $quoted_model -db $quoted_db -context-window 40960 -max-tokens 38912 -thinking-budget 32768 -aux-max-tokens 4096 -request-timeout 90m" 120 34

drive_demo() {
    local id caption prompt turn=0 chat_title
    phase='waiting for the local agent TUI'
    wait_for 'Write a message or slash command' 120 || return 1
    if [ "$history_mode" -eq 1 ]; then
        phase='showing the three validated historical chats'
        for id in 1 2 3; do
            case "$id" in
                1) caption='История реального прогона — простой запрос (Qwen3.8-27B)'; chat_title='Chat 1' ;;
                2) caption='История реального прогона — средний запрос (Qwen3.8-27B)'; chat_title='Day 26 — independent prompt 2' ;;
                3) caption='История реального прогона — сложный запрос (Qwen3.8-27B)'; chat_title='Day 26 — independent prompt 3' ;;
            esac
            demo_tmux display-message -d 3600000 "$caption"
            demo_tui_paste "/use $id"
            demo_tui_key Enter
            wait_for "using chat $id: $chat_title" 30 || return 1
            demo_tui_key Escape
            wait_for 'Write a message or slash command' 30 || return 1
            capture_turn_pages "history-$id" || return 1
        done
        demo_tui_paste '/new Day 26 — LIVE simple'
        demo_tui_key Enter
        wait_for 'created chat' 30 || return 1
        wait_for 'Day 26 — LIVE simple' 30 || return 1
        demo_tui_key Escape
        wait_for 'Write a message or slash command' 30 || return 1
        id=simple
        caption='Новый чат — простой запрос к Qwen3.5-4B (LIVE)'
        turn=4
    else
        for id in simple memory hard; do
            turn=$((turn + 1))
            case "$id" in
                simple) caption='LIVE Qwen3.5-4B — определение локальной языковой модели' ;;
                memory) caption='LIVE Qwen3.5-4B — расчёт минимального объёма весов' ;;
                hard) caption='LIVE Qwen3.5-4B — алгоритмическая задача LeetCode 42 на Go' ;;
            esac
            if [ "$turn" -gt 1 ]; then
                chat_title="Day 26 — independent prompt $turn"
                phase="creating a fresh chat before $id"
                demo_tui_paste "/new $chat_title"
                demo_tui_key Enter
                wait_for 'created chat' 30 || return 1
                wait_for "$chat_title" 30 || return 1
                demo_tui_key Escape
                wait_for 'Write a message or slash command' 30 || return 1
            fi
            phase="asking $id and waiting for successful response"
            demo_tmux display-message -d 4000 "$caption"
            prompt=$(prompt_for "$id") || return 1
            demo_tui_paste "$prompt"
            demo_tui_key Enter
            wait_turn "$id" "$turn" || return 1
        done
    fi
    if [ "$history_mode" -eq 1 ]; then
        phase='asking the new live simple prompt and waiting for its persisted response'
        demo_tmux display-message -d 3600000 "$caption"
        prompt=$(prompt_for "$id") || return 1
        demo_tui_paste "$prompt"
        demo_tui_key Enter
        wait_turn live "$turn" || return 1
    fi
    phase='closing the TUI after all successful responses'
    demo_tui_key C-c
    phase=complete
}

(
    set +e
    drive_demo
    rc=$?
    if [ "$rc" -ne 0 ]; then
        printf 'error: Day26 demo failed during %s (status %s)\n' "$phase" "$rc" >&2
    fi
    if ! printf '%s\n' "$rc" > "$status_file"; then
        printf 'error: could not write demo driver status: %s\n' "$status_file" >&2
        rc=1
    fi
    demo_tui_detach || true
    exit "$rc"
) &
driver_pid=$!
watch_status=0
demo_tui_watch "$driver_pid" || watch_status=$?
if wait "$driver_pid"; then
    driver_wait_status=0
else
    driver_wait_status=$?
fi
if [ "$driver_wait_status" -ne 0 ]; then
    printf 'error: demo driver process exited with status %s (watch status %s)\n' "$driver_wait_status" "$watch_status" >&2
fi
if [ "$watch_status" -ne 0 ] && [ "$driver_wait_status" -eq 0 ]; then
    printf 'error: TUI watcher exited with status %s\n' "$watch_status" >&2
    driver_wait_status=$watch_status
fi
if [ ! -f "$status_file" ]; then
    printf 'error: missing demo driver status after joining driver (watch status %s, process status %s)\n' "$watch_status" "$driver_wait_status" >&2
    driver_status=1
elif ! IFS= read -r driver_status < "$status_file"; then
    printf 'error: could not read demo driver status: %s\n' "$status_file" >&2
    driver_status=1
elif [ "$driver_status" = 0 ] && [ "$driver_wait_status" -ne 0 ]; then
    driver_status=$driver_wait_status
fi
if [ "$history_mode" -eq 1 ] && [ "$driver_status" = 0 ]; then
    provenance_path=''
    if [ -n "$ARTIFACT_DIR" ]; then
        provenance_path="$ARTIFACT_DIR/provenance.json"
    fi
    if ! python3 - "$db" "$PROMPTS" "$provenance_path" "$history_baseline" "$model_path" "$DAY26_MODEL_ID" "$DAY26_MODEL_REVISION" <<'PY'
import hashlib
import json
import os
import stat
import sqlite3
import sys
from pathlib import Path

db_path, prompts_path, provenance_path, baseline_path, live_model_path, live_model_id, live_revision = sys.argv[1:]
baseline = json.loads(Path(baseline_path).read_text(encoding="utf-8"))
def identity_from_stat(info):
    return {
        "present": True,
        "device": info.st_dev,
        "inode": info.st_ino,
        "mode": stat.S_IMODE(info.st_mode),
        "size": info.st_size,
        "mtime_ns": info.st_mtime_ns,
        "ctime_ns": info.st_ctime_ns,
    }

def fingerprint(path):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return {"present": False}
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise SystemExit(f"error: history source or sidecar became a symlink or non-regular file: {path}")
    return identity_from_stat(info)

paths = {
    label: Path(baseline["snapshot"][label]["path"])
    for label in ("db", "wal", "shm")
}
identities = {label: fingerprint(path) for label, path in paths.items()}
for label, identity in identities.items():
    if identity != baseline["snapshot"][label]["identity"]:
        raise SystemExit(f"error: history source identity or sidecar presence changed during TUI run: {paths[label]}")

snapshot = {}
for label, path in paths.items():
    identity = identities[label]
    expected_digest = baseline["snapshot"][label]["sha256"]
    if identity["present"]:
        value = hashlib.sha256()
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        fd = os.open(path, flags)
        try:
            opened = os.fstat(fd)
            if not stat.S_ISREG(opened.st_mode) or identity_from_stat(opened) != identity:
                raise SystemExit(f"error: history source changed before final hashing: {path}")
            with os.fdopen(fd, "rb", closefd=False) as stream:
                for block in iter(lambda: stream.read(1024 * 1024), b""):
                    value.update(block)
            if identity_from_stat(os.fstat(fd)) != identity or fingerprint(path) != identity:
                raise SystemExit(f"error: history source changed during final hashing: {path}")
            digest = value.hexdigest()
        finally:
            os.close(fd)
    else:
        digest = None
    if digest != expected_digest:
        raise SystemExit(f"error: history source bytes changed during TUI run: {path}")
    snapshot[label] = {"path": str(path), "identity": identity, "sha256": digest}

if {label: fingerprint(path) for label, path in paths.items()} != identities:
    raise SystemExit("error: history source identity or sidecar presence changed during final snapshot")
if snapshot != baseline["snapshot"]:
    raise SystemExit("error: history source or sidecar changed during TUI run")
source_snapshot = snapshot
prompts = {item["id"]: item["text"] for item in json.load(open(prompts_path, encoding="utf-8"))}
with sqlite3.connect(db_path) as db:
    chats = db.execute("SELECT id, title FROM chats ORDER BY id").fetchall()
    messages = db.execute("SELECT chat_id, role, content FROM messages ORDER BY chat_id, id").fetchall()
    calls = db.execute("SELECT chat_id, kind, provider, model FROM api_calls ORDER BY chat_id, id").fetchall()
    mcp_count = db.execute("SELECT COUNT(*) FROM mcp_servers").fetchone()[0]
live_chat = [chat for chat in chats if chat[1] == "Day 26 — LIVE simple"]
live_messages = [message for message in messages if live_chat and message[0] == live_chat[0][0]]
live_calls = [(kind, provider, model) for chat_id, kind, provider, model in calls if live_chat and chat_id == live_chat[0][0]]
history_calls = [(kind, provider, model) for chat_id, kind, provider, model in calls if not live_chat or chat_id != live_chat[0][0]]
def snapshot_identity(identifier):
    if not isinstance(identifier, str) or not identifier:
        raise SystemExit("error: API-call model identifier is missing or invalid")
    snapshot_path = Path(identifier)
    if not snapshot_path.is_absolute() or snapshot_path.parent.name != "snapshots":
        raise SystemExit(f"error: API-call model is not an absolute Hugging Face snapshot path: {identifier}")
    repository_dir = snapshot_path.parent.parent.name
    if not repository_dir.startswith("models--"):
        raise SystemExit(f"error: API-call model path has no Hugging Face repository directory: {identifier}")
    repository_parts = repository_dir[len("models--"):].split("--", 1)
    if len(repository_parts) != 2 or not all(repository_parts):
        raise SystemExit(f"error: API-call model path has an invalid repository directory: {identifier}")
    revision = snapshot_path.name
    if len(revision) != 40 or any(char not in "0123456789abcdef" for char in revision):
        raise SystemExit(f"error: API-call model path has an invalid pinned revision: {identifier}")
    return {
        "model_id": f"{repository_parts[0]}/{repository_parts[1]}",
        "revision": revision,
        "snapshot_path": str(snapshot_path),
    }

historical_models = {model for _, _, model in history_calls}
live_models = {model for _, _, model in live_calls}
if len(historical_models) != 1 or len(live_models) != 1:
    raise SystemExit("error: historical and LIVE API calls must each record one consistent model snapshot")
historical_model = next(iter(historical_models))
live_model = next(iter(live_models))
historical_identity = snapshot_identity(historical_model)
live_identity = snapshot_identity(live_model)
if (
    historical_identity["model_id"] != "mlx-community/Qwen3.8-27B-4bit"
    or historical_identity["revision"] != "10c35caafbb80f7dc6a7a432cdd11af10a6d4818"
):
    raise SystemExit("error: historical API calls must retain the pinned Qwen3.8-27B snapshot")
if (
    live_identity["model_id"] != live_model_id
    or live_identity["revision"] != live_revision
    or live_identity["snapshot_path"] != live_model_path
):
    raise SystemExit("error: LIVE API calls must record the selected pinned Qwen3.5-4B snapshot path")
if (
    len(history_calls) != 6
    or sorted(kind for kind, _, _ in history_calls) != ["main", "main", "main", "memory", "memory", "memory"]
    or any(provider != "local" or model != historical_model for _, provider, model in history_calls)
    or len(live_calls) != 2
    or sorted(kind for kind, _, _ in live_calls) != ["main", "memory"]
    or any(provider != "local" or model != live_model for _, provider, model in live_calls)
):
    raise SystemExit("error: API calls must contain three historical local main+memory pairs and one LIVE local main+memory pair")
valid = (
    len(chats) == 4
    and len(messages) == 8
    and sum(role == "assistant" for _, role, _ in messages) == 4
    and len(calls) == 8
    and live_chat[0][0] > 3
    and [role for _, role, _ in live_messages] == ["user", "assistant"]
    and live_messages[0][2] == prompts["simple"]
    and bool(live_messages[1][2].strip())
    and sorted(kind for kind, _, _ in live_calls) == ["main", "memory"]
    and all(provider == "local" and model == live_model for _, provider, model in live_calls)
    and sorted(kind for kind, _, _ in history_calls) == ["main", "main", "main", "memory", "memory", "memory"]
    and all(provider == "local" and model == historical_model for _, provider, model in history_calls)
    and mcp_count == 0
)
if not valid:
    raise SystemExit("error: combined history/LIVE run must retain 3 historical 27B chats and use the pinned 4B model for its two LIVE calls")
result = {
    "history": {
        "chats": 3, "messages": 6, "api_calls": 6, "source_unchanged": True,
        "model_id": historical_identity["model_id"],
        "revision": historical_identity["revision"],
        "snapshot_path": historical_identity["snapshot_path"],
        "source_identity_and_sidecars": source_snapshot,
    },
    "live": {
        "chat_title": "Day 26 — LIVE simple", "chats": 1, "messages": 2,
        "assistant_messages": 1, "api_calls": 2, "api_call_kinds": ["main", "memory"],
        "model_id": live_identity["model_id"],
        "revision": live_identity["revision"],
        "snapshot_path": live_identity["snapshot_path"],
    },
    "clone_totals": {"chats": 4, "messages": 8, "assistant_messages": 4, "api_calls": 8},
    "mcp_servers": 0,
}
if provenance_path:
    with open(provenance_path, "x", encoding="utf-8") as output:
        json.dump(result, output, ensure_ascii=False, indent=2)
        output.write("\n")
print("Validated private clone: 3 historical chats + 1 new LIVE chat; 4 assistant messages and 8 API calls; original source and sidecars unchanged through the TUI run.")
PY
    then
        driver_status=1
    fi
fi
if [ -n "$ARTIFACT_DIR" ]; then
    if [ "$driver_status" != 0 ]; then
        if ! demo_tmux capture-pane -t "$TUI_SESSION" -p > "$ARTIFACT_DIR/failure-pane.txt"; then
            printf 'error: could not save current TUI pane to %s/failure-pane.txt\n' "$ARTIFACT_DIR" >&2
        fi
    fi
    python3 - "$db" "$ARTIFACT_DIR/day26.sqlite" "$ARTIFACT_DIR/transcript.json" <<'PY'
import json
import sqlite3
import sys

with sqlite3.connect(sys.argv[1]) as db:
    rows = db.execute("SELECT role, content FROM messages ORDER BY id").fetchall()
    with sqlite3.connect(sys.argv[2]) as copy:
        db.backup(copy)
with open(sys.argv[3], "w", encoding="utf-8") as output:
    json.dump([{"role": role, "content": content} for role, content in rows], output, ensure_ascii=False, indent=2)
    output.write("\n")
PY
    printf 'Saved TUI captures and SQLite transcript artifacts to %s\n' "$ARTIFACT_DIR"
fi
case "$driver_status" in
    0) ;;
    ''|*[!0-9]*)
        printf 'error: invalid demo driver status: %s\n' "$driver_status" >&2
        exit 1
        ;;
    *)
        printf 'error: Day26 demo driver failed with status %s\n' "$driver_status" >&2
        exit "$driver_status"
        ;;
esac
printf 'SQLite conversation retained at %s\n' "$db"
demo_outro 'week-06/day-26 + day-27'
