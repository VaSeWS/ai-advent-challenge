#!/bin/sh
set -eu

VENV="${DAY26_VENV:-${HOME}/.local/share/ai-advent/day26-mlx}"
UV="${UV:-${HOME}/.local/bin/uv}"
MODEL='mlx-community/Qwen3.5-4B-4bit'
REVISION='0e7ffd5c629ef7719d4cbc04069232580bfa9d9c'
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ ! -x "$UV" ]; then
    printf 'error: uv executable not found at %s\n' "$UV" >&2
    exit 1
fi

if [ -e "$VENV" ] || [ -L "$VENV" ]; then
    if [ ! -f "$VENV/pyvenv.cfg" ] || [ ! -x "$VENV/bin/python" ]; then
        printf 'error: existing path is not a valid Python virtual environment: %s\n' "$VENV" >&2
        exit 1
    fi
else
    "$UV" venv --python 3.13 "$VENV"
fi

"$UV" pip install --python "$VENV/bin/python" -r "$SCRIPT_DIR/requirements.txt"
"$VENV/bin/hf" download "$MODEL" --revision "$REVISION"
