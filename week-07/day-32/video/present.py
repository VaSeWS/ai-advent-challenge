#!/usr/bin/env python3
"""Fetch and present real Day 32 GitHub run, PR, log, and comment data."""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import time

MARKER = "<!-- iron-ai-review -->"
CATEGORIES = ("Ошибки", "Архитектура", "Рекомендации")


def gh(*args: str) -> str:
    result = subprocess.run(
        ["gh", *args], check=True, text=True, encoding="utf-8",
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    return result.stdout


def run_json(run_id: str) -> dict:
    return json.loads(gh("run", "view", run_id, "--repo", "VaSeWS/iRon_fork", "--json",
                         "databaseId,workflowName,displayTitle,event,status,conclusion,headBranch,headSha,url,createdAt,updatedAt"))


def index_view(run_id: str) -> str:
    run = run_json(run_id)
    if run["workflowName"] != "rag-index" or run["headBranch"] != "main":
        raise RuntimeError("Указанный запуск не является rag-index для main.")
    if run["event"] not in {"push", "workflow_dispatch"} or run.get("conclusion") != "success":
        raise RuntimeError("Индекс принимается только из успешного push/workflow_dispatch run.")
    log = gh("run", "view", run_id, "--repo", "VaSeWS/iRon_fork", "--log")
    downloads = []
    for candidate in re.finditer(r"\{[^{}\n]*\"found\"\s*:\s*true[^{}\n]*\}", log):
        try:
            data = json.loads(candidate.group())
        except json.JSONDecodeError:
            continue
        if all(isinstance(data.get(key), int) for key in ("id", "run_id", "size")):
            downloads.append(data)
    if not downloads:
        raise RuntimeError("Run log не содержит фактический JSON результата загрузки RAG-индекса.")
    origin = downloads[-1]
    origin_run = run_json(str(origin["run_id"]))
    artifacts = json.loads(gh(
        "api", f"repos/VaSeWS/iRon_fork/actions/runs/{origin['run_id']}/artifacts"
    ))
    artifact = next((item for item in artifacts.get("artifacts", [])
                     if item.get("id") == origin["id"] and not item.get("expired", False)), None)
    if artifact is None or not artifact["name"].startswith("rag-index-v1-"):
        raise RuntimeError("Download helper provenance does not resolve to a live versioned index artifact.")
    digest = artifact["name"].removeprefix("rag-index-v1-")
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise RuntimeError("Trusted artifact name has an invalid corpus hash.")
    jobs = json.loads(gh("api", f"repos/VaSeWS/iRon_fork/actions/runs/{run_id}/jobs"))
    steps = [step for job in jobs.get("jobs", []) for step in job.get("steps", [])]
    reuse = next((step["conclusion"] for step in steps
                  if step["name"] == "Reuse existing successful index"), "not reported")
    build = next((step["conclusion"] for step in steps
                  if step["name"] == "Build index artifact"), "not reported")
    upload = next((step["conclusion"] for step in steps
                   if step["name"] == "Upload versioned index"), "not reported")
    return (f"Индексирующий запуск #{run['databaseId']} — {run['workflowName']} "
            f"({run['status']}/{run['conclusion']})\n"
            f"Provenance run: VaSeWS/iRon_fork · main · {run['event']} · {run['headSha']}\n"
            f"Запуск: {run['url']}\n"
            f"Reuse: {reuse}; построение: {build}; upload нового артефакта: {upload}\n\n"
            f"Полученный из helper JSON источник: artifact #{origin['id']} "
            f"из успешного main run #{origin['run_id']} ({origin['size']} bytes)\n"
            f"Источник run: {origin_run['url']}\nАртефакт: {artifact['name']} "
            f"(GitHub compressed size {artifact['size_in_bytes']} bytes)\n"
            f"Corpus hash: {digest}\n")

def pr_view(number: str) -> str:
    data = json.loads(gh("pr", "view", number, "--repo", "VaSeWS/iRon_fork", "--json",
                         "number,title,url,files,state,baseRefName,headRefName"))
    files = data.get("files", [])
    lines = [f"PR #{data['number']}: {data['title']}", f"Статус: {data['state']}",
             f"Ветка: {data['headRefName']} → {data['baseRefName']}", f"URL: {data['url']}",
             f"Изменённые файлы ({len(files)}):"]
    lines.extend(f"  {item['path']} (+{item['additions']} −{item['deletions']})" for item in files)
    return "\n".join(lines) + "\n"


def review_sources(run_id: str) -> str:
    run = run_json(run_id)
    if run["workflowName"] != "ai-review" or run.get("conclusion") != "success":
        raise RuntimeError("Указанный run не является успешно завершённым ai-review.")
    log = gh("run", "view", run_id, "--repo", "VaSeWS/iRon_fork", "--log")
    matches = [line for line in log.splitlines() if "Источники обзора:" in line or re.search(
        r"источник\s+\d+|Источники обзора", line, re.I,
    )]
    if not matches:
        raise RuntimeError("Review log не содержит фактических строк выбранных источников.")
    return (f"Review run #{run['databaseId']} — {run['workflowName']}\n"
            f"Статус: {run['status']} / {run['conclusion']}\nURL: {run['url']}\n\n"
            "Выбранные источники из run log (фактический вывод CLI):\n" + "\n".join(matches) + "\n")


def review_comment(number: str) -> str:
    pages = json.loads(gh("api", "--paginate", "--slurp",
                          f"repos/VaSeWS/iRon_fork/issues/{number}/comments?per_page=100"))
    data = [item for page in pages for item in page]
    marked = [item for item in data if MARKER in item.get("body", "")]
    if not marked:
        raise RuntimeError("На PR не найден опубликованный комментарий с маркером iron-ai-review.")
    item = max(marked, key=lambda entry: (entry.get("updated_at") or entry["created_at"], entry["id"]))
    body = item["body"]
    if any(not re.search(rf"^#+\s*{re.escape(name)}\s*$", body, re.M) for name in CATEGORIES):
        raise RuntimeError("Фактический marked comment не содержит все три заголовка категорий.")
    return (f"Актуальный marked review comment #{item['id']} — {item['html_url']}\n"
            f"Автор: {item['user']['login']} · обновлён: {item.get('updated_at') or item['created_at']}\n\n{body}\n")


def present(text: str) -> None:
    if os.environ.get("DEMO_DRY_RUN") == "1" or not sys.stdout.isatty():
        sys.stdout.write(text)
        sys.stdout.flush()
        return
    width = max(1, shutil.get_terminal_size().columns)
    rows = max(2, shutil.get_terminal_size().lines - 5)
    wrapped = [line[i:i + width] for line in text.splitlines() for i in range(0, max(1, len(line)), width)]
    for offset in range(0, len(wrapped), rows - 1):
        page = wrapped[offset:offset + rows - 1]
        sys.stdout.write("\033[2J\033[H")
        sys.stdout.write(f"── Страница {offset // (rows - 1) + 1}/{(len(wrapped) + rows - 2) // (rows - 1)} ──\n")
        sys.stdout.write("\n".join(page) + "\n")
        sys.stdout.flush()
        time.sleep(min(12.0, max(6.0, sum(map(len, page)) / 25.0)))


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: present.py {index|pr|sources|comment} ID", file=sys.stderr)
        return 2
    mode, identifier = sys.argv[1:]
    try:
        render = {"index": index_view, "pr": pr_view, "sources": review_sources,
                  "comment": review_comment}[mode]
        present(render(identifier))
    except (KeyError, ValueError, subprocess.CalledProcessError, RuntimeError) as exc:
        detail = exc.stderr.strip() if isinstance(exc, subprocess.CalledProcessError) and exc.stderr else str(exc)
        print(f"GitHub demo data unavailable: {detail}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
