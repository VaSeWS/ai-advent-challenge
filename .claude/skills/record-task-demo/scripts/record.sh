#!/usr/bin/env bash
# Native-TTY callers stay direct; non-TTY callers get a separately verified Terminal window.
set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec python3 "$SCRIPT_DIR/record-supervisor.py" "$@"
