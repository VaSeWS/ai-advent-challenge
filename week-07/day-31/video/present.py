#!/usr/bin/env python3
"""Present complete command output as readable terminal pages."""

from __future__ import annotations

import os
import shutil
import sys
import time
import unicodedata


def _char_width(char: str, column: int) -> int:
    """Return a character's terminal-cell width at the given column."""
    if char == "\t":
        return 8 - column % 8
    if unicodedata.combining(char):
        return 0
    if unicodedata.category(char).startswith("C"):
        return 0
    return 2 if unicodedata.east_asian_width(char) in {"W", "F"} else 1


def _wrap_line(line: str, width: int) -> list[str]:
    """Wrap without dropping, duplicating, or rewriting any input characters."""
    if not line:
        return [""]

    wrapped: list[str] = []
    segment: list[str] = []
    cells = 0
    for char in line:
        char_cells = _char_width(char, cells)
        if segment and cells + char_cells > width:
            wrapped.append("".join(segment))
            segment = []
            cells = 0
            char_cells = _char_width(char, cells)
        segment.append(char)
        cells += char_cells
    wrapped.append("".join(segment))
    return wrapped


def _wrapped_lines(text: str, width: int) -> list[str]:
    """Wrap LF-delimited source lines without discarding their contents."""
    return [part for line in text.split("\n") for part in _wrap_line(line, width)]


def _configured_dwell(page: list[str]) -> float:
    """Read configured dwell time, otherwise allow dense pages up to 12 seconds."""
    configured = os.environ.get("DAY31_PAGE_SECONDS")
    if configured is not None:
        return min(12.0, max(0.0, float(configured)))
    visible_characters = sum(len(line) for line in page)
    return min(12.0, max(6.0, visible_characters / 25.0))


def present(text: str) -> None:
    """Write all text directly, or page it when attached to a recording terminal."""
    if os.environ.get("DEMO_DRY_RUN") == "1" or not sys.stdout.isatty():
        sys.stdout.write(text)
        sys.stdout.flush()
        return

    terminal = shutil.get_terminal_size()
    width = max(1, terminal.columns)
    maximum_page_rows = max(2, terminal.lines - 5)
    content_rows = maximum_page_rows - 1
    lines = _wrapped_lines(text, width)
    pages = [lines[index : index + content_rows] for index in range(0, len(lines), content_rows)]
    if not pages:
        pages = [[]]

    for page_number, page in enumerate(pages, start=1):
        sys.stdout.write("\033[2J\033[H")
        sys.stdout.write(f"── Страница {page_number}/{len(pages)} ──\n")
        if page:
            sys.stdout.write("\n".join(page))
            if page_number < len(pages) or text.endswith(("\n", "\r")):
                sys.stdout.write("\n")
        sys.stdout.flush()
        time.sleep(_configured_dwell(page))


def main() -> None:
    """Read UTF-8 command output from stdin and present it completely."""
    text = sys.stdin.buffer.read().decode("utf-8")
    present(text)


if __name__ == "__main__":
    main()
