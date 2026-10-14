#!/bin/sh
set -eu

VENV="${DAY26_VENV:-${HOME}/.local/share/ai-advent/day26-mlx}"
MODEL='mlx-community/Qwen3.5-4B-4bit'
REVISION='0e7ffd5c629ef7719d4cbc04069232580bfa9d9c'
KV_SIZE=${DAY26_KV_SIZE:-16384}

case "$KV_SIZE" in
    ''|*[!0-9]*)
        printf 'error: DAY26_KV_SIZE must be a positive integer, got %s\n' "$KV_SIZE" >&2
        exit 1
        ;;
    *[1-9]*) ;;
    *)
        printf 'error: DAY26_KV_SIZE must be a positive integer, got %s\n' "$KV_SIZE" >&2
        exit 1
        ;;
esac

if [ ! -f "$VENV/pyvenv.cfg" ] || [ ! -x "$VENV/bin/python" ] || [ ! -x "$VENV/bin/mlx_vlm.server" ]; then
    printf 'error: MLX-VLM environment is not installed at %s; run runtime/setup.sh first\n' "$VENV" >&2
    exit 1
fi

SNAPSHOT=$("$VENV/bin/python" - "$MODEL" "$REVISION" <<'PY'
import json
import sys
from pathlib import Path

repo_id, revision = sys.argv[1:]

try:
    from huggingface_hub import snapshot_download

    snapshot = Path(snapshot_download(
        repo_id=repo_id,
        revision=revision,
        local_files_only=True,
    )).resolve()
except Exception as exc:
    print(
        f"error: pinned model snapshot {repo_id}@{revision} is unavailable locally; "
        f"run runtime/setup.sh to download it ({exc})",
        file=sys.stderr,
    )
    raise SystemExit(1)


required_files = ("config.json", "tokenizer.json", "model.safetensors.index.json")
for name in required_files:
    path = snapshot / name
    if not path.is_file() or path.stat().st_size == 0:
        print(f"error: pinned model snapshot is incomplete: missing or empty {path}", file=sys.stderr)
        raise SystemExit(1)

try:
    index = json.loads((snapshot / "model.safetensors.index.json").read_text())
    weight_map = index["weight_map"]
    if not isinstance(weight_map, dict) or not weight_map:
        raise ValueError("weight_map is empty or invalid")
    shards = sorted(set(weight_map.values()))
    if not shards or any(not isinstance(shard, str) or not shard for shard in shards):
        raise ValueError("weight_map does not name any model shards")
    for shard in shards:
        shard_name = Path(shard)
        if shard_name.is_absolute() or ".." in shard_name.parts:
            raise ValueError(f"unsafe model shard path {shard}")
        shard_path = snapshot / shard_name
        if not shard_path.is_file() or shard_path.stat().st_size == 0:
            raise ValueError(f"missing or empty model shard {shard}")
except (KeyError, OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
    print(f"error: pinned model snapshot has an invalid or incomplete shard index: {exc}", file=sys.stderr)
    raise SystemExit(1)

print(snapshot)
PY
)

exec "$VENV/bin/mlx_vlm.server" \
    --model "$SNAPSHOT" \
    --host 127.0.0.1 \
    --port 8080 \
    --enable-thinking \
    --max-kv-size "$KV_SIZE"
