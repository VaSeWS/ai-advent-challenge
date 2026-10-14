#!/usr/bin/env python3
"""Read immutable rag-experiment attempt JSONL and page two observations together."""
import argparse
import hashlib
import math
import json
import os
import re
import sqlite3
import statistics
import subprocess
import sys
import time
import urllib.request
from pathlib import Path
from contextlib import contextmanager
import unicodedata

import regex as grapheme_regex
from wcwidth import wcswidth


ANSI = re.compile(
    r"\x1b(?:\[[0-?]*[ -/]*[@-~]"
    r"|\](?:(?!\x07|\x1b\\)[\s\S])*(?:\x07|\x1b\\)"
    r"|[PX^_](?:(?!\x1b\\)[\s\S])*\x1b\\|[@-_])"
    r"|\x9b[0-?]*[ -/]*[@-~]"
    r"|\x9d(?:(?!\x07|\x9c)[\s\S])*(?:\x07|\x9c)"
    r"|\x90|\x98|\x9e|\x9f"
)

SELECTED_REVISION = "0e7ffd5c629ef7719d4cbc04069232580bfa9d9c"
SELECTED_REPOSITORY = "models--mlx-community--Qwen3.5-4B-4bit"
FROZEN_SHA256 = "099418d4659fa757d5ff4c762bc695a2edefd975797e883783037ea2daf80311"
DEEPSEEK_REVIEW_SHA256 = "4250633ebca9fc17a9a7e6dffe968df16c9a55c2f349fadf737e8d730add8a85"
DEEPSEEK_REPORT_SHA256 = "6316e178806961aa488ffdc796789b9c0065c46e4a73ce7b758468c805ae4279"
EXPECTED_KEYS = {(f"q{number:02d}", repeat) for number in range(1, 11) for repeat in (1, 2)}


def file_digest(path):
    with open(path, "rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def terminal_records(records, provider, profile):
    selected = [
        row for row in records
        if row.get("provider") == provider
        and (not profile or row.get("profile", {}).get("name") == profile)
        and row.get("status") != "planned"
    ]
    keyed = {(row.get("question_id"), row.get("repeat")): row for row in selected}
    if len(selected) != 20 or set(keyed) != EXPECTED_KEYS:
        raise ValueError(f"{provider}/{profile}: require exactly terminal q01-q10/repeats 1-2")
    if any(row.get("status") not in ("complete", "error") for row in selected):
        raise ValueError("unknown terminal lifecycle status")
    return keyed


def selected_model(model):
    return (
        isinstance(model, str)
        and os.path.isabs(model)
        and os.path.normpath(model) == model
        and model.endswith(f"/{SELECTED_REPOSITORY}/snapshots/{SELECTED_REVISION}")
    )


def effective_messages(messages):
    result = []
    for message in messages:
        if not isinstance(message, dict) or message.get("role") not in ("system", "user", "assistant"):
            raise ValueError("malformed captured message")
        role, content = message["role"], message.get("content")
        if not isinstance(content, str):
            raise ValueError("captured message content is not text")
        if role == "system" and result and result[-1]["role"] == "system":
            result[-1]["content"] += "\n\n" + content
        else:
            result.append({"role": role, "content": content})
    return result


def require_frozen(records, provider, profile, frozen):
    keyed = terminal_records(records, provider, profile)
    cases = {case["control_question"]["id"]: case for case in frozen["cases"]}
    instructions = []
    for (qid, repeat), row in keyed.items():
        case = cases[qid]
        arm = row.get("profile", {}).get("name")
        if arm not in ("A", "B"):
            raise ValueError("selected-model final reports support only A/B")
        if row.get("question") != case["control_question"]["question"]:
            raise ValueError(f"{qid}/{repeat}: question differs from approved frozen input")
        for field in ("context", "candidates", "context_sha256", "candidates_sha256", "retrieval_sha256"):
            if row.get(field) != case[field]:
                raise ValueError(f"{qid}/{repeat}: actual {field} differs from frozen input")
        documentary = case["documentary_messages"][arm]
        if row.get("messages") != documentary or row.get("prompt_sha256") != case["prompt_sha256"][arm]:
            raise ValueError(f"{qid}/{repeat}: documentary prompt differs from approved {arm}")
        request = request_json_only(row)
        if request is None or not row.get("request_dispatched"):
            raise ValueError(f"{qid}/{repeat}: missing actual dispatched request")
        messages = request.get("messages")
        if not isinstance(messages, list) or not messages or messages[0].get("role") != "system":
            raise ValueError(f"{qid}/{repeat}: missing captured system instruction")
        instruction = messages[0]["content"].split("\n\n", 1)[0]
        if not instruction.startswith('Return only valid JSON matching this JSON Schema named "rag_answer".'):
            raise ValueError(f"{qid}/{repeat}: unexpected grounding schema instruction")
        expected = effective_messages([{"role": "system", "content": instruction}] + documentary)
        if effective_messages(messages) != expected:
            raise ValueError(f"{qid}/{repeat}: effective captured instructions/evidence differ from frozen prompt")
        instructions.append(instruction)
        if provider == "local":
            if not selected_model(request.get("model")):
                raise ValueError(f"{qid}/{repeat}: actual request model is not the selected 4B pin")
            settings = {"A": (0, 2048, True, 16384, "grounded"),
                        "B": (0.2, 1536, False, 8192, "grounded-concise")}[arm]
            if (request.get("temperature"), request.get("max_tokens"), request.get("enable_thinking")) != settings[:3]:
                raise ValueError(f"{qid}/{repeat}: captured request differs from selected-model {arm} contract")
            declared = row.get("profile") or {}
            declared_settings = tuple(declared.get(field) for field in (
                "temperature", "max_tokens", "enable_thinking", "context_window", "prompt_template"
            ))
            if (
                declared_settings != settings
                or declared.get("server_kv_size") != settings[3]
                or declared.get("model_revision") != request["model"]
            ):
                raise ValueError(f"{qid}/{repeat}: declared profile differs from captured selected-model {arm}")
            generation = row.get("generation") or {}
            if (
                generation.get("context_window") != settings[3]
                or generation.get("prompt_template") != settings[4]
                or generation.get("main_model") != request["model"]
                or generation.get("aux_model") != request["model"]
            ):
                raise ValueError(f"{qid}/{repeat}: generation provenance differs from selected-model {arm}")
    if any(value != instructions[0] for value in instructions):
        raise ValueError("full schema instruction differs within arm")
    return instructions[0]


def valid_citations(row):
    answer = row.get("answer") or {}
    citations = answer.get("citations")
    if answer.get("status") == "insufficient_context":
        return bool(answer.get("answer")) and citations == []
    if answer.get("status") != "answer" or not isinstance(citations, list) or not citations:
        return False
    chunks = {(item.get("Chunk") or item.get("chunk") or {}).get("id"):
              (item.get("Chunk") or item.get("chunk") or {}).get("text", "")
              for item in row.get("context") or []}
    return all(
        isinstance(item, dict) and item.get("chunk_id") in chunks
        and isinstance(item.get("quote"), str) and bool(item["quote"].strip())
        and item["quote"] in chunks[item["chunk_id"]]
        for item in citations
    )


def semantic_review(path, report_path, keyed, provider):
    if provider == "deepseek" and file_digest(path) != DEEPSEEK_REVIEW_SHA256:
        raise ValueError("final DeepSeek arm requires the exact retained corrected semantic review")
    with open(path, encoding="utf-8") as source:
        review = json.load(source)
    if review.get("review_status") not in ("ready_for_review", "accepted"):
        raise ValueError(f"{path}: independent review is not ready")
    digest = file_digest(report_path)
    if not any(
        isinstance(item, dict) and item.get("sha256") == digest
        and os.path.basename(item.get("path", "")) == os.path.basename(report_path)
        for item in review.get("inputs", [])
    ):
        raise ValueError(f"{path}: review does not bind exact report bytes")
    legacy = provider == "deepseek"
    if not legacy and review.get("review_contract") != "rag-video-semantic-review-v1":
        raise ValueError(f"{path}: require independent rag-video-semantic-review-v1")
    observations = review.get("observations")
    if not isinstance(observations, list) or len(observations) != 20:
        raise ValueError(f"{path}: require 20 independent observations, including errors")
    assessed = {}
    for item in observations:
        key = (item.get("question_id"), item.get("repeat"))
        if key not in keyed or key in assessed:
            raise ValueError(f"{path}: duplicate/unexpected review question-repeat")
        row = keyed[key]
        if legacy:
            if item.get("status") != row["status"] or item.get("answer") != row.get("answer"):
                raise ValueError("legacy DeepSeek assessment does not match actual answer")
            facts = item.get("fact_statuses") or []
            if not facts or any(value not in ("present_correct", "missing", "contradicted") for value in facts):
                raise ValueError("legacy DeepSeek fact assessment is incomplete")
            verdict = "incomplete" if "missing" in facts else "incorrect" if "contradicted" in facts else (
                "correct_refusal" if row["answer"]["status"] == "insufficient_context" else "correct_answer")
            item = {"terminal_status": row["status"], "strict_valid": True,
                    "semantic_verdict": verdict, "citation_valid": valid_citations(row)}
        verdict = item.get("semantic_verdict")
        citation = item.get("citation_valid")
        if (
            item.get("terminal_status") != row["status"]
            or type(item.get("strict_valid")) is not bool
            or item["strict_valid"] != (row["status"] == "complete")
            or verdict not in ("correct_answer", "correct_refusal", "incomplete", "incorrect", "unscorable")
            or not (citation is None or type(citation) is bool)
        ):
            raise ValueError(f"{path}: malformed/inconsistent observation {key}")
        if row["status"] == "error" and verdict != "unscorable":
            raise ValueError(f"{path}: failed attempt must remain unscorable")
        if row["status"] == "complete" and verdict == "unscorable":
            raise ValueError(f"{path}: complete final answer lacks semantic assessment")
        if row["status"] == "complete" and citation != valid_citations(row):
            raise ValueError(f"{path}: citation assessment conflicts with actual chunks/quotes")
        answer_status = (row.get("answer") or {}).get("status")
        if verdict == "correct_answer" and (answer_status != "answer" or citation is not True):
            raise ValueError(f"{path}: correct answer lacks actual valid citations")
        if verdict == "correct_refusal" and (answer_status != "insufficient_context" or citation is not True):
            raise ValueError(f"{path}: correct refusal lacks actual empty-citation refusal")
        assessed[key] = item
    if not legacy:
        counts = review.get("outcomes") or {}
        expected = {"planned": 20, "terminal": 20,
                    "strict_valid": sum(item["strict_valid"] for item in assessed.values()),
                    "semantically_correct": sum(item["semantic_verdict"] in ("correct_answer", "correct_refusal") for item in assessed.values()),
                    "correct_refusals": sum(item["semantic_verdict"] == "correct_refusal" for item in assessed.values())}
        if any(counts.get(name) != value for name, value in expected.items()):
            raise ValueError(f"{path}: aggregates do not match all 20 assessments")
    return assessed


def measured_summary(values, label):
    values = [value for value in values if isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)]
    if not values:
        return f"{label}: unavailable (0/20 measured)"
    return (f"{label}: n={len(values)}/20; mean {statistics.mean(values):.3f}; "
            f"median {statistics.median(values):.3f}; range {min(values):.3f}–{max(values):.3f}")


def quality_lines(keyed, assessed, provider, profile, resources_only=False):
    rows = list(keyed.values())
    verdicts = [item["semantic_verdict"] for item in assessed.values()]
    result = [
        f"{provider} / {profile or 'A'}: planned=20; terminal=20; failures never filtered",
        f"Complete {sum(row['status'] == 'complete' for row in rows)}/20; error {sum(row['status'] == 'error' for row in rows)}/20",
        f"Fully correct {sum(value in ('correct_answer', 'correct_refusal') for value in verdicts)}/20",
        f"Incomplete {verdicts.count('incomplete')}/20; incorrect {verdicts.count('incorrect')}/20; unscorable {verdicts.count('unscorable')}/20",
        f"Correct refusals {verdicts.count('correct_refusal')}/20; q09/q10 assessed, errors are not refusals",
        f"Citation-valid complete outputs {sum(item['strict_valid'] and item['citation_valid'] is True for item in assessed.values())}/20; exact quote checks are not semantic scoring",
    ]
    stable = sum(
        (assessed[(qid, 1)]["semantic_verdict"], assessed[(qid, 1)]["citation_valid"], keyed[(qid, 1)]["status"])
        == (assessed[(qid, 2)]["semantic_verdict"], assessed[(qid, 2)]["citation_valid"], keyed[(qid, 2)]["status"])
        for qid in (f"q{number:02d}" for number in range(1, 11))
    )
    result.append(f"Repeat verdict/citation/lifecycle agreement {stable}/10 pairs; not general reliability")
    for qid in (f"q{number:02d}" for number in range(1, 11)):
        result.append(f"{qid}: " + " / ".join(assessed[(qid, repeat)]["semantic_verdict"] for repeat in (1, 2)))
    result.append(measured_summary(
        [row.get("generation_path_duration_ms") / 1000 for row in rows
         if row.get("request_dispatched") and isinstance(row.get("generation_path_duration_ms"), (int, float))],
        "Generation-path wall seconds (includes dispatched errors)"))
    result.append("Not GPU compute/decode latency; retrieval is separate, missing times not zero")
    if not resources_only:
        result.append("Wire: " + ("local strict native JSON Schema" if provider == "local" else "remote json_object"))
        result.append("Shared frozen context/schema; task template follows A/B profile, raw wire may differ")
        result.append("Descriptive generation-path comparison; no isolated-knob speedup or broad reliability claim")
        return result
    result = result[:2] + result[-2:]
    result.append(measured_summary(
        [((row.get("runtime_metrics") or {}).get("matching_request") or {}).get("peak_memory_gb") for row in rows],
        "Observed MLX peak GB (not RSS)"))
    for position in ("before", "after"):
        result.append(measured_summary(
            [((row.get("resource_measurements") or {}).get(position) or {}).get("process_rss_bytes") for row in rows],
            f"Process RSS {position} bytes"))
    result.append("Pressure / swap snapshots (not GPU allocations):")
    for qid, repeat in sorted(keyed):
        row = keyed[(qid, repeat)]
        resources = row.get("resource_measurements") or {}
        before, after = resources.get("before") or {}, resources.get("after") or {}
        result.append(f"{qid}/{repeat}: pressure {before.get('system_memory_pressure', 'unavailable')} → {after.get('system_memory_pressure', 'unavailable')}; swap {before.get('swap_usage', 'unavailable')} → {after.get('swap_usage', 'unavailable')}")
    result.append("Remote resources/context unknown; requested thinking is not observed reasoning")
    return result


LIVE_QUESTIONS = (
    "What are the override/default/output priorities in an MV EnderIO central ore-processing line?",
    "How are clean dusts routed in that line?",
)


def write_new_json(path, value):
    with open(path, "x", encoding="utf-8") as output:
        json.dump(value, output, ensure_ascii=False, indent=2)
        output.write("\n")


@contextmanager
def db_connection(path):
    connection = sqlite3.connect(Path(path).resolve().as_uri() + "?mode=ro", uri=True)
    try:
        connection.execute("PRAGMA query_only=ON")
        yield connection
    finally:
        connection.close()


def json_cell(value):
    return json.loads(value)


def db_manifest(path, old_sessions=None):
    with db_connection(path) as connection:
        connection.execute("BEGIN")
        sessions = [row[0] for row in connection.execute("SELECT id FROM chat_sessions ORDER BY id")]
        old = set(sessions if old_sessions is None else old_sessions)
        history = {}
        for table, identity in (("chat_sessions", "id"), ("chat_turns", "session_id"),
                                ("chat_messages", "session_id"), ("chat_states", "session_id")):
            cursor = connection.execute(f'SELECT * FROM "{table}"')
            column = [description[0] for description in cursor.description].index(identity)
            rows = [list(row) for row in cursor if row[column] in old]
            serialized = sorted(json.dumps(row, default=lambda value: {"bytes_hex": value.hex()}, sort_keys=True) for row in rows)
            history[table] = {"count": len(rows), "sha256": hashlib.sha256("\n".join(serialized).encode()).hexdigest()}
        # Hash ordinary tables, including vec0 shadow tables, without loading
        # sqlite-vec or querying its virtual table through an unregistered module.
        index = {}
        for name, sql in connection.execute("SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name"):
            if name.startswith("chat_") or name.startswith("sqlite_"):
                continue
            if sql and sql.upper().startswith("CREATE VIRTUAL TABLE"):
                index[name] = {"schema": sql}
                continue
            quoted = '"' + name.replace('"', '""') + '"'
            rows = [json.dumps(list(row), default=lambda value: {"bytes_hex": value.hex()}, sort_keys=True)
                    for row in connection.execute(f"SELECT * FROM {quoted}")]
            index[name] = {"count": len(rows), "sha256": hashlib.sha256("\n".join(sorted(rows)).encode()).hexdigest()}
        return {"session_ids": sessions, "old_history": history, "index": index}


class NoMetricsRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        # None makes urllib raise the original HTTP error without contacting
        # the target; loopback evidence may not be substituted by a redirect.
        return None


def native_snapshot():
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoMetricsRedirect())
    with opener.open("http://127.0.0.1:8080/metrics", timeout=10) as response:
        metrics = json.load(response)
    summary, server = metrics.get("summary") or {}, metrics.get("server") or {}
    for field in ("requests_started", "requests_completed", "requests_failed", "in_flight"):
        if type(summary.get(field)) is not int:
            raise ValueError("native metrics lack actual request counters")
    if not selected_model(server.get("loaded_model")):
        raise ValueError("native loaded model is not selected 4B pin")
    if server.get("configured_context_limit") != 16384 or server.get("effective_context_limit") != 16384:
        raise ValueError("LIVE native configured/effective context is not 16384")
    if summary["in_flight"] != 0 or server.get("request_queue_depth") != 0:
        raise ValueError("native backend is not idle; no parallel generator allowed")
    if not isinstance(metrics.get("recent"), list):
        raise ValueError("native metrics lack recent per-request evidence")
    return metrics


def verify_live_generation(generation, model):
    expected = {
        "provider": "local", "endpoint": "http://127.0.0.1:8080/v1/chat/completions",
        "main_model": model, "aux_model": model, "temperature": 0.2,
        "max_tokens": 4096, "context_window": 16384, "enable_thinking": False,
        "prompt_template": "grounded-concise",
    }
    if any(generation.get(field) != value for field, value in expected.items()):
        raise ValueError("persisted LIVE generation differs from explicit selected4B main/aux settings")


def live_baseline(args):
    if not selected_model(args.live_model) or not Path(args.live_model).is_dir():
        raise ValueError("LIVE snapshot must be the existing verified absolute 4B pin")
    directory = Path(args.live_baseline_dir)
    directory.mkdir(mode=0o700)  # Exclusive-create; never replace a prior take.
    before = {
        "contract": "day28-fresh4b-live-proof-v1", "db": str(Path(args.live_db).resolve()),
        "model": args.live_model, "captured_at_unix": time.time(),
        "manifest": db_manifest(args.live_db), "metrics": native_snapshot(),
        "reports": {"left": file_digest(args.left_jsonl), "right": file_digest(args.right_jsonl)},
    }
    write_new_json(directory / "before.json", before)
    print("LIVE baseline saved; old sessions/index preserved; selected4B backend idle. No inference requested.")


def live_proof(args):
    directory = Path(args.live_proof_dir)
    with open(directory / "before.json", encoding="utf-8") as source:
        before = json.load(source)
    if (
        before.get("contract") != "day28-fresh4b-live-proof-v1"
        or before.get("db") != str(Path(args.live_db).resolve())
        or before.get("model") != args.live_model
        or before.get("reports") != {"left": file_digest(args.left_jsonl), "right": file_digest(args.right_jsonl)}
        or not args.tmux_socket or not args.tmux_session
        or not Path(args.tmux_socket).is_socket()
    ):
        raise ValueError("fresh LIVE baseline/report/model/private-socket identity mismatch")
    if (directory / "result.json").exists():
        raise ValueError("LIVE take already has a terminal result; no replay/overwrite")
    result = {"contract": before["contract"], "status": "error", "turns": [],
              "main_aux_evidence": "Persisted generation plus every native recent request/counter delta; ordinary chat does not capture raw request_json."}

    def tmux(*command):
        return subprocess.run(["tmux", "-N", "-S", args.tmux_socket, *command],
                              check=True, text=True, capture_output=True).stdout

    def capture():
        dead = tmux("display-message", "-p", "-t", args.tmux_session, "#{pane_dead}").strip()
        if dead == "1":
            result["failed_pane_exit_status"] = tmux("display-message", "-p", "-t", args.tmux_session, "#{pane_dead_status}").strip()
            result["failed_pane_output"] = safe_text(tmux("capture-pane", "-p", "-S", "-", "-t", args.tmux_session))
            raise ValueError("ordinary LIVE TUI exited before completion:\n" + result["failed_pane_output"])
        return tmux("capture-pane", "-p", "-t", args.tmux_session)

    def key(value):
        tmux("send-keys", "-t", args.tmux_session, value)
        time.sleep(0.2)

    def wait_until(operation, seconds, label):
        deadline = time.monotonic() + seconds
        while True:
            value = operation()
            if value:
                return value
            if time.monotonic() >= deadline:
                raise ValueError(f"{label}: timeout; no retry and no success from a quiet screen")
            time.sleep(0.3)

    def fresh_session():
        with db_connection(args.live_db) as connection:
            rows = connection.execute("SELECT id, generation_json, raw_turns FROM chat_sessions").fetchall()
        fresh = [row for row in rows if row[0] not in before["manifest"]["session_ids"]]
        if len(fresh) > 1:
            raise ValueError("more than one fresh session; exclusive ordinary TUI ownership lost")
        if not fresh:
            return None
        verify_live_generation(json_cell(fresh[0][1]), args.live_model)
        if fresh[0][2] < 2:
            raise ValueError("fresh ordinary session does not retain two turns of history")
        return fresh[0][0]

    def page_all(label, both=True):
        # Paging a persisted completed answer is not a generation completion gate.
        # Detect actual top/bottom viewport boundaries rather than guess page count.
        def scroll(direction):
            key(direction)
            if both:
                key("Tab")
                key(direction)
                key("Tab")
            return capture()
        current = capture()
        for _ in range(500):
            previous, current = current, scroll("PageUp")
            if current == previous:
                break
        else:
            raise ValueError("LIVE viewport top not reached within bounded paging")
        pages = []
        for _ in range(500):
            pages.append(current)
            time.sleep(max(args.pause, len(safe_text(current).split()) / 3.0))
            following = scroll("PageDown")
            if following == current:
                write_new_json(directory / f"{label}-pages.json", pages)
                return
            current = following
        raise ValueError("LIVE viewport bottom not reached within bounded paging")

    try:
        wait_until(lambda: "Ask a question about GT:NH" in capture(), 120, "TUI ready")
        sid = wait_until(fresh_session, 120, "one fresh session")
        result["session_id"] = sid
        if args.require_attached_tui:
            wait_until(lambda: int(tmux("display-message", "-p", "-t", args.tmux_session, "#{session_attached}").strip()) > 0,
                       30, "recording client attached")
        metrics_before = before["metrics"]
        # Startup must not have caused a model call.
        ready_metrics = native_snapshot()
        if ready_metrics["summary"]["requests_started"] != metrics_before["summary"]["requests_started"]:
            raise ValueError("model request before fresh turn; exclusive runtime proof invalid")
        for ordinal, question in enumerate(LIVE_QUESTIONS, 1):
            tmux("display-message", "-t", args.tmux_session, "-d", "2500", f"LIVE selected4B · turn {ordinal}/2")
            started = time.monotonic()
            for char in question:
                tmux("send-keys", "-t", args.tmux_session, "-l", "--", char)
                time.sleep(0.03)
            key("Enter")

            def saved_turn():
                pane = capture()
                if re.search(r"(?:Lane [12].*error|Turn failed|save failed)", pane, re.IGNORECASE):
                    raise ValueError("LIVE application shows an error; no automatic rerun")
                with db_connection(args.live_db) as connection:
                    rows = connection.execute(
                        "SELECT question, lanes_json FROM chat_turns WHERE session_id=? AND ordinal=?",
                        (sid, ordinal)).fetchall()
                if not rows or f"Turn {ordinal} saved" not in pane:
                    return None
                if len(rows) != 1 or rows[0][0] != question:
                    raise ValueError("saved LIVE turn/question mismatch")
                lanes = json_cell(rows[0][1])
                if not isinstance(lanes, list) or len(lanes) != 2:
                    raise ValueError("saved LIVE turn lacks both actual lanes")
                for lane in lanes:
                    verify_live_generation(lane.get("generation") or {}, args.live_model)
                    if lane.get("original_query") != question or not valid_citations(lane):
                        raise ValueError("saved LIVE answer/refusal/citations fail grounded structural validation")
                return lanes

            lanes = wait_until(saved_turn, args.live_turn_timeout, f"Turn {ordinal} saved with both lanes")
            after = native_snapshot()
            old_summary, new_summary = metrics_before["summary"], after["summary"]
            count = new_summary["requests_started"] - old_summary["requests_started"]
            requests = [row for row in after["recent"]
                        if isinstance(row.get("timestamp_unix"), (int, float))
                        and row["timestamp_unix"] > (old_summary.get("last_request_at") or 0)]
            if (
                count <= 0 or new_summary["requests_completed"] - old_summary["requests_completed"] != count
                or new_summary["requests_failed"] != old_summary["requests_failed"] or len(requests) != count
                or any(row.get("model") != args.live_model for row in requests)
                or len({row["timestamp_unix"] for row in requests}) != count
            ):
                raise ValueError("native per-request evidence does not cover every actual selected4B main/aux call")
            result["turns"].append({"ordinal": ordinal, "question": question, "lanes": lanes,
                                    "wall_seconds": time.monotonic() - started, "metrics_after": after,
                                    "actual_request_count": count, "actual_requests": requests})
            page_all(f"turn-{ordinal}")
            metrics_before = after
        key("Tab")
        key("C-s")
        wait_until(lambda: "Inspector · state" in capture(), 10, "task-state inspector")
        page_all("memory", both=False)
        key("Escape")
        key("Tab")
        with db_connection(args.live_db) as connection:
            turns = connection.execute("SELECT ordinal, question FROM chat_turns WHERE session_id=? ORDER BY ordinal", (sid,)).fetchall()
            messages = connection.execute("SELECT lane, role, content FROM chat_messages WHERE session_id=? ORDER BY lane,ordinal", (sid,)).fetchall()
            states = connection.execute("SELECT lane,state_json FROM chat_states WHERE session_id=? ORDER BY lane", (sid,)).fetchall()
        if turns != [(1, LIVE_QUESTIONS[0]), (2, LIVE_QUESTIONS[1])] or len(messages) != 8 or len(states) != 2:
            raise ValueError("persisted fresh session does not contain exactly two turns/eight lane messages/two states")
        for lane in (0, 1):
            history = [row for row in messages if row[0] == lane]
            if [row[1] for row in history] != ["user", "assistant", "user", "assistant"] or [history[0][2], history[2][2]] != list(LIVE_QUESTIONS):
                raise ValueError("persisted raw-history roles/questions differ from actual two turns")
            if json_cell(states[lane][1]) != result["turns"][-1]["lanes"][lane]["state"]:
                raise ValueError("persisted task state differs from saved actual lane")
        memory = json_cell(states[1][1])
        if not memory.get("goal") or not memory.get("goal_quote") or memory["goal_quote"] not in "\n".join(LIVE_QUESTIONS):
            raise ValueError("memory lane lacks actual user-grounded goal")
        preserved = db_manifest(args.live_db, before["manifest"]["session_ids"])
        if preserved["index"] != before["manifest"]["index"] or preserved["old_history"] != before["manifest"]["old_history"]:
            raise ValueError("existing index or historical sessions changed during fresh LIVE take")
        if set(preserved["session_ids"]) != set(before["manifest"]["session_ids"]) | {sid}:
            raise ValueError("unexpected session creation/deletion during LIVE take")
        final_metrics = native_snapshot()
        if final_metrics["summary"]["requests_started"] != metrics_before["summary"]["requests_started"]:
            raise ValueError("unexpected request during presentation; exclusive ownership lost")
        result.update({"status": "source_evidence_complete", "preserved_manifest": preserved,
                       "metrics_final": final_metrics, "semantic_visual_acceptance": "pending independent parent review"})
        key("C-c")
        wait_until(lambda: tmux("display-message", "-p", "-t", args.tmux_session, "#{pane_dead}").strip() == "1",
                   20, "ordinary TUI shutdown")
        if tmux("display-message", "-p", "-t", args.tmux_session, "#{pane_dead_status}").strip() != "0":
            raise ValueError("ordinary TUI did not exit successfully")
    except Exception as error:
        result["status"] = "error"
        result["error"] = str(error)
        raise
    finally:
        write_new_json(directory / "result.json", result)
        # Only clients of this owned private socket/session; never a default server.
        subprocess.run(["tmux", "-N", "-S", args.tmux_socket, "detach-client", "-s", args.tmux_session],
                       text=True, capture_output=True)


def safe_text(text):
    text = ANSI.sub("", str(text))
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    return "".join(char for char in text if char in "\n\t" or (unicodedata.category(char) not in ("Cc", "Cs")))


def clusters(text):
    return grapheme_regex.findall(r"\X", text)


def cluster_width(cluster):
    return max(0, wcswidth(cluster))


def pad(text, cells):
    return text + " " * max(0, cells - width(text))


def width(text):
    text = safe_text(text).expandtabs(8)
    return sum(cluster_width(cluster) for cluster in clusters(text))


def wrap(text, limit):
    text = safe_text(text).expandtabs(8)
    if not text:
        return [""]
    result = []
    for paragraph in text.splitlines() or [""]:
        if not paragraph:
            result.append("")
            continue
        tokens = re.findall(r"\s+|\S+", paragraph)
        line = ""
        for token in tokens:
            if token.isspace():
                if line and width(line + token) > limit:
                    result.append(line)
                    line = ""
                for cluster in clusters(token):
                    if line and width(line + cluster) > limit:
                        result.append(line)
                        line = ""
                    line += cluster
                continue
            if line and width(line + token) > limit:
                result.append(line)
                line = ""
            piece = ""
            for cluster in clusters(token):
                if piece and width(piece + cluster) > limit:
                    if line:
                        result.append(line)
                    line = piece
                    piece = ""
                piece += cluster
            if line and width(line + piece) > limit:
                result.append(line)
                line = ""
            line += piece
        result.append(line)
    return result


def load(path):
    records = []
    with open(path, encoding="utf-8") as source:
        for number, line in enumerate(source, 1):
            if not line.strip():
                continue
            record = json.loads(line)
            if record.get("record_type") == "attempt":
                record["_source_line"] = number
                records.append(record)
    return records


def observation(records, provider, qid, repeat, profile):
    matches = [r for r in records if r.get("provider") == provider and r.get("question_id") == qid and int(r.get("repeat", 0)) == repeat and (not profile or r.get("profile", {}).get("name") == profile)]
    if not matches:
        return None
    return matches[-1]


def short(value):
    if isinstance(value, dict):
        return ", ".join(f"{k}={v}" for k, v in value.items() if v not in (None, "", [], {}))
    return str(value or "—")


def captured_request(record):
    usage = record.get("usage")
    sources = [record.get("request_json")]
    if isinstance(usage, dict):
        sources.append(usage.get("request_json"))
    sources = [source for source in sources if source not in (None, "")]
    if not sources:
        return None
    requests = []
    for source in sources:
        try:
            request = json.loads(source) if isinstance(source, str) else source
        except (TypeError, json.JSONDecodeError):
            return None
        if not isinstance(request, dict):
            return None
        requests.append(request)
    fields = ("model", "temperature", "max_tokens", "thinking", "enable_thinking")
    first = {key: requests[0].get(key) for key in fields if key in requests[0]}
    for request in requests[1:]:
        current = {key: request.get(key) for key in fields if key in request}
        if current != first:
            return None
    return requests[0]



def captured_schema_contract(record, request=None):
    """Return the single named schema embedded in a captured system message."""
    request = request if request is not None else captured_request(record)
    messages = request.get("messages") if isinstance(request, dict) else None
    if not isinstance(messages, list):
        return None
    found = []
    for message in messages:
        if not isinstance(message, dict) or message.get("role") != "system":
            continue
        content = message.get("content")
        if not isinstance(content, str):
            continue
        marker = re.search(r'Schema named ["\']([^"\']+)["\'].*?Schema:\s*', content, re.DOTALL)
        if not marker:
            continue
        try:
            schema, end = json.JSONDecoder().raw_decode(content[marker.end():])
        except json.JSONDecodeError:
            continue
        if isinstance(schema, dict):
            found.append((marker.group(1), schema, content[marker.start():marker.end() + end]))
    return found[0] if len(found) == 1 else None


def captured_schema_instruction(record):
    contract = captured_schema_contract(record)
    return contract[2] if contract else None


def request_json_only(record):
    """Parse only the request_json field; usage metadata is not request evidence."""
    source = record.get("request_json")
    try:
        request = json.loads(source) if isinstance(source, str) else source
    except (TypeError, json.JSONDecodeError):
        return None
    return request if isinstance(request, dict) else None



def same_json_value(left, right):
    return json.dumps(left, sort_keys=True, separators=(",", ":")) == json.dumps(
        right, sort_keys=True, separators=(",", ":")
    )

def native_schema_status(record):
    request = request_json_only(record)
    response = request.get("response_format") if request else None
    if not isinstance(response, dict):
        return "unavailable"
    if response.get("type") != "json_schema":
        return str(response.get("type") or "unavailable")
    supplied = response.get("json_schema")
    instruction = captured_schema_contract(record, request)
    if not isinstance(supplied, dict) or instruction is None:
        return "json_schema (schema/name/strict unavailable)"
    name, schema, _ = instruction
    if (
        supplied.get("name") != name
        or supplied.get("strict") is not True
        or not same_json_value(supplied.get("schema"), schema)
    ):
        return "json_schema (does not match supplied schema instruction)"
    return f"json_schema(name={name}, strict=true)"


def require_schema_parity(records, provider, profile, expected=None):
    selected = [
        record for record in records
        if record.get("provider") == provider
        and (not profile or record.get("profile", {}).get("name") == profile)
        and record.get("status") in ("complete", "error")
    ]
    if len(selected) != 20:
        raise ValueError(
            f"{provider}/{profile or 'all profiles'} must have exactly 20 selected terminal attempts"
        )
    schemas = []
    for record in selected:
        if not record.get("request_dispatched"):
            raise ValueError(
                f"{provider}/{profile or 'all profiles'} {record.get('question_id')} "
                f"repeat {record.get('repeat')}: terminal attempt has no dispatched request"
            )
        request = request_json_only(record)
        if request is None:
            raise ValueError(
                f"{provider}/{profile or 'all profiles'} {record.get('question_id')} "
                f"repeat {record.get('repeat')}: actual request_json is missing or unparseable"
            )
        contract = captured_schema_contract(record, request)
        if contract is None:
            raise ValueError(
                f"{provider}/{profile or 'all profiles'} {record.get('question_id')} "
                f"repeat {record.get('repeat')}: captured system schema instruction is missing or ambiguous"
            )
        name, schema, instruction = contract
        schemas.append(instruction)
        if provider == "local":
            response = request.get("response_format")
            supplied = response.get("json_schema") if isinstance(response, dict) else None
            if (
                not isinstance(response, dict)
                or response.get("type") != "json_schema"
                or not isinstance(supplied, dict)
                or supplied.get("name") != name
                or supplied.get("strict") is not True
                or not same_json_value(supplied.get("schema"), schema)
            ):
                raise ValueError(
                    f"local/{profile or 'all profiles'} {record.get('question_id')} "
                    f"repeat {record.get('repeat')}: actual request_json must use json_schema "
                    "with the exact instructed name/schema and strict=true"
                )
    if any(schema != schemas[0] for schema in schemas[1:]):
        raise ValueError(f"captured system schema instruction differs within {provider}/{profile or 'all profiles'}")
    if expected is not None and schemas[0] != expected:
        raise ValueError("captured system schema instructions differ across comparison providers")
    return schemas[0]


def historical_protocol_label(left_data, right_data, left_provider, right_provider, left_profile, right_profile):
    selected = []
    for records, provider, profile in (
        (left_data, left_provider, left_profile),
        (right_data, right_provider, right_profile),
    ):
        selected.extend(
            record for record in records
            if record.get("provider") == provider
            and (not profile or record.get("profile", {}).get("name") == profile)
            and record.get("status") != "planned"
        )
    if any(request_json_only(record) is None for record in selected):
        return "captured request_json unobserved; NON-RANKING"
    instructions = [captured_schema_instruction(record) for record in selected]
    if any(instruction is None for instruction in instructions):
        return "captured schema instruction unobserved; NON-RANKING"
    if any(instruction != instructions[0] for instruction in instructions[1:]):
        return "pre-exact-instruction: captured system instructions differ; NON-RANKING"
    local_records = [
        record for record in selected
        if record.get("provider") == "local"
    ]
    if not local_records or any(request_json_only(record) is None for record in local_records):
        return "local request wire unobserved; NON-RANKING"
    local_types = {
        (request_json_only(record).get("response_format") or {}).get("type")
        for record in local_records
        if isinstance(request_json_only(record).get("response_format"), dict)
    }
    if not local_types:
        return "local response format unobserved; NON-RANKING"
    if local_types == {"json_object"}:
        return "pre-native-schema: matching instructions, local json_object; NON-RANKING"
    if "json_object" in local_types or "json_schema" in local_types:
        return "mixed local wire mechanisms; NON-RANKING"
    return "historical wire contract; NON-RANKING"
 


def wire_settings(request):
    if request is None:
        return "unavailable (captured request missing, unparseable, or conflicting)"
    temperature = request.get("temperature")
    max_tokens = request.get("max_tokens")
    thinking = request.get("thinking")
    thinking_type = thinking.get("type") if isinstance(thinking, dict) else None
    enabled = request.get("enable_thinking")
    if thinking_type is not None:
        type_enabled = thinking_type != "disabled"
        if enabled is not None and enabled != type_enabled:
            return "unavailable (conflicting captured thinking fields)"
    elif enabled is not None:
        thinking_type = "enabled" if enabled else "disabled"
    values = (
        f"temperature={temperature if temperature is not None else 'unavailable'}",
        f"max_tokens={max_tokens if max_tokens is not None else 'unavailable'}",
        f"thinking={thinking_type if thinking_type is not None else 'unavailable'}",
    )
    return ", ".join(values)


def profile_declaration(record, profile):
    provider = record.get("provider")
    name = profile.get("name")
    fields = {
        "temperature": profile.get("temperature"),
        "max_tokens": profile.get("max_tokens"),
        "client context": profile.get("context_window"),
        "target KV": profile.get("server_kv_size"),
        "enable_thinking": profile.get("enable_thinking"),
    }
    declared = [f"{key}={value}" for key, value in fields.items() if value is not None]
    model_revision = profile.get("model_revision")
    if isinstance(model_revision, str) and model_revision:
        revision = os.path.basename(model_revision.rstrip("/"))
        if "snapshots/" in model_revision:
            model = model_revision.split("models--")[-1].split("/snapshots/")[0].replace("--", "/")
            declared.append(f"pinned model={model}@{revision[:8]}")
        else:
            declared.append(f"pinned revision={revision[:8]}")
    applicability = "local-only" if provider == "local" else "local-only; not applicable to remote provider"
    return f"{name or 'unnamed'} ({applicability}): " + (", ".join(declared) or "settings unavailable")


def capability_caption(record):
    if record.get("provider") == "local":
        metrics = record.get("runtime_metrics") or {}
        snapshot = metrics.get("health_after") or metrics.get("health_before") or {}
        fields = ("loaded_context_size", "configured_context_limit", "effective_context_limit")
        return "Captured native context: " + ", ".join(f"{field}={snapshot.get(field, 'unobserved')}" for field in fields)
    return "Remote KV, native context, effective context: unobserved"


def short_model(model):
    if not isinstance(model, str) or not model:
        return "unavailable"
    if "models--" in model and "/snapshots/" in model:
        repo = model.split("models--")[-1].split("/snapshots/")[0].replace("--", "/")
        revision = model.rsplit("/snapshots/", 1)[-1].split("/", 1)[0]
        return f"{repo}@{revision[:8]}"
    if "/snapshots/" in model:
        model_name = model.split("/snapshots/", 1)[0].rstrip("/").split("/")[-1]
        revision = model.split("/snapshots/", 1)[1].split("/", 1)[0]
        return f"{model_name}@{revision[:8]}"
    if model.startswith("/"):
        model = os.path.basename(model.rstrip("/"))
    if len(model) > 32:
        model = model[:20] + "…" + model[-8:]
    return model


def actual_model_caption(request):
    model = request.get("model") if isinstance(request, dict) else None
    return "Actual request model: " + short_model(model)




def timing_label(record):
    retrieval = record.get("retrieval_duration_ms")
    retrieval_text = f"{retrieval} ms retrieval" if retrieval is not None else "retrieval time unavailable"
    generation = record.get("generation_path_duration_ms")
    if not record.get("request_dispatched"):
        generation_text = "not requested"
    elif generation is not None:
        generation_text = f"{generation} ms generation path"
    else:
        generation_text = "generation-path time unavailable"
    return retrieval_text + " · " + generation_text + " · not summed"


def final_content(record):
    usage = record.get("usage")
    final = usage.get("final_content") if isinstance(usage, dict) else None
    if isinstance(final, str):
        try:
            return json.loads(final)
        except json.JSONDecodeError:
            return final
    return final


def has_final_content(record):
    usage = record.get("usage")
    return isinstance(usage, dict) and "final_content" in usage


def diagnostic_lines(value, panel_width, fields):
    """Page final-answer/citation values, never arbitrary object fields."""
    if isinstance(value, dict):
        lines = []
        for name in fields:
            if name in value:
                lines += wrap(name + ":", panel_width)
                lines += diagnostic_lines(value[name], panel_width, fields)
        if any(name not in fields for name in value):
            lines += wrap("[Unrelated fields omitted]", panel_width)
        return lines or wrap("[Empty object]", panel_width)
    if isinstance(value, list):
        lines = []
        for index, item in enumerate(value, 1):
            lines += wrap(f"Item {index}:", panel_width)
            lines += diagnostic_lines(item, panel_width, fields)
        return lines or wrap("[Empty array]", panel_width)
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return wrap(text or "[Empty string]", panel_width)


def model_status(record):
    answer = record.get("answer")
    status = answer.get("status") if isinstance(answer, dict) else None
    if not isinstance(status, str) or not status:
        final = final_content(record)
        status = final.get("status") if isinstance(final, dict) else None
    return status if isinstance(status, str) and status else "not recorded"




def frozen_source(record, chunk_id):
    for candidate in record.get("context") or []:
        chunk = candidate.get("chunk") or candidate.get("Chunk") or {}
        if chunk.get("id") == chunk_id:
            title = chunk.get("title") or "Untitled source"
            sections = " › ".join(chunk.get("section_paths") or [])
            return title + (f" — {sections}" if sections else "")
    return "Source unavailable in frozen context"


def panel_title(record, label, provider, profile_name):
    provider = (record or {}).get("provider") or provider
    profile = (record or {}).get("profile") or {}
    profile_name = profile.get("name") or profile_name
    identity = " · ".join(part for part in (label, provider, profile_name) if part)
    return identity or "Observation"


def profile_card_lines(record, panel_width):
    if record is None:
        return ["Profile / request: unavailable"]
    profile = record.get("profile") or {}
    request = captured_request(record)
    lines = wrap(actual_model_caption(request), panel_width)
    lines += wrap("Captured wire: " + wire_settings(request), panel_width)
    lines += wrap("Captured response format: " + native_schema_status(record), panel_width)
    lines += wrap("Declared profile: " + profile_declaration(record, profile), panel_width)
    lines += wrap(capability_caption(record), panel_width)
    return lines


def status_strip_lines(record, panel_width):
    if record is None:
        return ["No matching saved observation."]
    status = record.get("status", "unknown")
    no_model_result = not record.get("request_dispatched") and record.get("finish_reason") == "not_requested_insufficient_context"
    result_status = "no model result (insufficient context)" if no_model_result else model_status(record)
    content_unvalidated = status != "complete" and (record.get("answer") is not None or has_final_content(record))
    if content_unvalidated:
        result_status += " · content UNVALIDATED"
    lines = wrap(
        f"Lifecycle {status} · {'dispatched' if record.get('request_dispatched') else 'not dispatched'} · {result_status}",
        panel_width,
    )
    lines += wrap("Timing: " + timing_label(record) + f" · finish {record.get('finish_reason') or '—'}", panel_width)
    metrics = record.get("runtime_metrics") or {}
    latest = metrics.get("matching_request") or {}
    resources = record.get("resource_measurements") or {}
    before, after = resources.get("before") or {}, resources.get("after") or {}
    peak = latest.get("peak_memory_gb")
    peak_text = f"{peak} GB" if peak is not None else "unavailable"
    rss = lambda snapshot: (f"{snapshot['process_rss_bytes'] / 1e9:.2f} GB" if snapshot.get("process_rss_bytes") is not None else "unavailable")
    resource_status = record.get("resource_status") or "status unavailable"
    lines += wrap(f"Resources: MLX peak {peak_text} · process RSS before {rss(before)} / after {rss(after)} · {resource_status}", panel_width)
    return lines


def panel_lines(record, panel_width):
    if record is None:
        return ["No matching saved observation."]
    no_model_result = not record.get("request_dispatched") and record.get("finish_reason") == "not_requested_insufficient_context"
    answer_record = record.get("answer")
    answer_record = answer_record if isinstance(answer_record, dict) else {}
    final = final_content(record)
    structured = final if isinstance(final, dict) else {}
    answer = answer_record.get("answer")
    answer_present = "answer" in answer_record
    fallback_answer = not isinstance(answer, str) or not answer
    if fallback_answer and "answer" in structured:
        answer = structured["answer"]
        answer_present = True
    citation_sources = record.get("citation_sources")
    if citation_sources is not None and (not isinstance(citation_sources, list) or citation_sources):
        citations = citation_sources
        fallback_citations = False
        citations_present = True
    else:
        source = structured if "citations" in structured else answer_record
        citations = source.get("citations", [])
        fallback_citations = True
        citations_present = "citations" in source
    failed = record.get("status") != "complete"
    final_fields = ("status", "answer", "citations", "chunk_id", "quote")
    citation_fields = ("chunk_id", "quote") if fallback_citations else ("number", "title", "sections", "quote")
    lines = []
    if has_final_content(record) and not isinstance(final, dict):
        lines += wrap("UNVALIDATED captured final content (not an answer/refusal):", panel_width)
        lines += diagnostic_lines(final, panel_width, final_fields)
    else:
        if (fallback_answer and answer_present) or failed:
            lines += wrap("Captured answer content: UNVALIDATED", panel_width)
        lines.append("Answer:")
        if isinstance(answer, str) and answer:
            lines += wrap(answer, panel_width)
        elif answer_present:
            lines += wrap("UNVALIDATED answer value (not an answer/refusal):", panel_width)
            lines += diagnostic_lines(answer, panel_width, final_fields)
        elif no_model_result:
            lines += wrap("No model result: insufficient context; request not dispatched.", panel_width)
        elif has_final_content(record):
            lines += wrap("UNVALIDATED final object has no answer field; unrelated fields omitted.", panel_width)
        else:
            lines.append("No final answer was recorded.")
    if citations_present and not isinstance(citations, list):
        lines += wrap("UNVALIDATED citations value (not valid citations):", panel_width)
        lines += diagnostic_lines(citations, panel_width, citation_fields)
    elif citations:
        lines.append("Citations:")
        for index, citation in enumerate(citations, 1):
            if not isinstance(citation, dict):
                lines += wrap(f"UNVALIDATED citation item {index} (not a valid citation):", panel_width)
                lines += diagnostic_lines(citation, panel_width, citation_fields)
                continue
            quote = citation.get("quote")
            number = index if fallback_citations else citation.get("number", index)
            if fallback_citations:
                chunk_id = citation.get("chunk_id")
                valid_shape = isinstance(chunk_id, str) and bool(chunk_id) and isinstance(quote, str) and bool(quote)
                title = frozen_source(record, chunk_id) if valid_shape else ""
                sections = []
            else:
                title = citation.get("title", "Untitled source")
                sections = citation.get("sections", [])
                valid_shape = isinstance(title, str) and isinstance(sections, list) and all(
                    isinstance(section, str) for section in sections) and isinstance(quote, str) and bool(quote)
                valid_shape = valid_shape and type(number) is int and number > 0
            if not valid_shape:
                lines += wrap(f"UNVALIDATED citation item {index} (not a valid citation):", panel_width)
                lines += diagnostic_lines(citation, panel_width, citation_fields)
                continue
            unvalidated = failed or fallback_citations or citation.get("unvalidated")
            prefix = "UNVALIDATED · " if unvalidated else ""
            lines += wrap(f"[{number}] {prefix}{title}" + (f" — {' › '.join(sections)}" if sections else ""), panel_width)
            quote_prefix = "Unvalidated quote: " if unvalidated else "Quote: "
            lines += wrap(quote_prefix + "“" + quote + "”", panel_width)
    metrics = record.get("runtime_metrics") or {}
    latest = metrics.get("matching_request") or {}
    resources = record.get("resource_measurements") or {}
    before, after = resources.get("before") or {}, resources.get("after") or {}
    lines.append("Details (after answer and citations):")
    lines += wrap("Resource details: " + short({
        "MLX peak GB": latest.get("peak_memory_gb"),
        "RSS before bytes": before.get("process_rss_bytes"),
        "RSS after bytes": after.get("process_rss_bytes"),
        "pressure before/after": f"{before.get('system_memory_pressure', '—')} → {after.get('system_memory_pressure', '—')}",
        "swap before/after": f"{before.get('swap_usage', '—')} → {after.get('swap_usage', '—')}",
        "method": resources.get("method"),
        "resource status": record.get("resource_status"),
    }), panel_width)
    lines += wrap("Runtime metrics: " + short({
        "status": record.get("metrics_status"),
        "decode tok/s": latest.get("decode_tok_s"),
        "matching request seconds": latest.get("request_elapsed_s"),
    }), panel_width)
    if record.get("error"):
        lines += wrap("Failure: " + record["error"], panel_width)
    return lines


def summary_lines(records, provider, profile_name, keys, panel_width):
    groups = []
    inner = panel_width
    selected = [r for r in records if r.get("provider") == provider and (not profile_name or r.get("profile", {}).get("name") == profile_name)]
    groups.append(wrap(f"{provider} · {profile_name or 'all profiles'}: {len(selected)} lifecycle rows (planned and terminal rows retained)", inner))
    for qid, repeat in keys:
        record = observation(records, provider, qid, repeat, profile_name)
        if record is None:
            groups.append(wrap(f"{qid} · repeat {repeat}: no observation", inner))
            continue
        group = []
        context_identity = (record.get("context_sha256") or "")[:12] or "unavailable"
        build_identity = (record.get("retrieval_sha256") or "")[:12] or "unavailable"
        group += wrap(f"Frozen context {context_identity} · retrieval/build {build_identity}", inner)
        answer_present = bool(record.get("answer") or (record.get("usage") or {}).get("final_content"))
        request = "request dispatched" if record.get("request_dispatched") else "request not dispatched"
        no_model_result = not record.get("request_dispatched") and record.get("finish_reason") == "not_requested_insufficient_context"
        result_label = "no model result (insufficient context)" if no_model_result else f"model status {model_status(record)}"
        group += wrap(f"Question: {record.get('question', '')}", inner)
        group += wrap(f"{qid} · repeat {repeat}: lifecycle {record.get('status')} · {request} · {timing_label(record)} · {result_label} · final content {'present' if answer_present else 'absent'} · finish {record.get('finish_reason') or '—'}", inner)
        group += wrap("Captured response format: " + native_schema_status(record), inner)
        metrics = record.get("runtime_metrics") or {}
        matching = metrics.get("matching_request") or {}
        measurements = record.get("resource_measurements") or {}
        before, after = measurements.get("before") or {}, measurements.get("after") or {}
        group += wrap("Resources: " + short({
            "MLX peak GB": matching.get("peak_memory_gb"),
            "RSS before bytes": before.get("process_rss_bytes"),
            "RSS after bytes": after.get("process_rss_bytes"),
            "pressure before/after": f"{before.get('system_memory_pressure', '—')} → {after.get('system_memory_pressure', '—')}",
            "swap before/after": f"{before.get('swap_usage', '—')} → {after.get('swap_usage', '—')}",
        }), inner)
        if record.get("error"):
            group += wrap("Failure: " + record["error"], inner)
        groups.append(group)
    return groups


def border(left, right, left_width, right_width):
    return "│ " + pad(left, left_width) + " │ " + pad(right, right_width) + " │"




def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("left_jsonl")
    parser.add_argument("right_jsonl")
    parser.add_argument("--left-provider", default="local")
    parser.add_argument("--right-provider", default="deepseek")
    parser.add_argument("--left-profile", default="")
    parser.add_argument("--right-profile", default="")
    parser.add_argument("--left-label", default="Local")
    parser.add_argument("--right-label", default="DeepSeek")
    parser.add_argument("--question", default="", help="question_id; defaults to first shared ID")
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--pause", type=float, default=0, help="minimum seconds per page; 0 waits for Enter")
    parser.add_argument("--page-hold", type=float, default=0, help="fixed timed-playback hold per full page; 0 keeps the content-based hold")
    parser.add_argument("--summary", action="store_true", help="show every question/repeat and lifecycle outcome as paired status/timing rows")
    parser.add_argument("--require-schema-parity", action="store_true", help="require exact captured instructions and local native json_schema wire parity")
    parser.add_argument("--historical-confound", action="store_true", help="label historical protocol stage from captured evidence; non-ranking")
    parser.add_argument("--check-schema-parity-only", action="store_true", help="validate 20 captured terminal requests, schema instructions, and local native schema wire, then exit")
    parser.add_argument("--require-complete-pair", action="store_true", help="require terminal q01-q10/repeat-1-2 coverage and matching question/context pairs")
    parser.add_argument("--require-selected-model", action="store_true", help="gate selected4B A/B pin, exact approved frozen bytes, all20 effective captured prompts and native schema")
    parser.add_argument("--frozen", default="", help="approved immutable day28-frozen-real-v1.json")
    parser.add_argument("--left-review", default="", help="digest-bound independent all20 semantic review")
    parser.add_argument("--right-review", default="", help="digest-bound independent all20 semantic review")
    parser.add_argument("--quality-summary", action="store_true", help="paired independently assessed quality/repeat/timing card, always denominator20")
    parser.add_argument("--resource-summary", action="store_true", help="paired actual resource snapshots with explicit missingness")
    parser.add_argument("--check-final-only", action="store_true", help="validate final selected4B/frozen/schema/reviews offline, then exit")
    parser.add_argument("--live-baseline-dir", default="", help="exclusive-create fresh ordinary TUI take directory; snapshot read-only DB/metrics, no inference")
    parser.add_argument("--live-proof-dir", default="", help="send exactly two real questions to the owned fresh TUI; retain actual session/request/page proof")
    parser.add_argument("--live-db", default="week-05/rag.db")
    parser.add_argument("--live-model", default="")
    parser.add_argument("--tmux-socket", default="")
    parser.add_argument("--tmux-session", default="")
    parser.add_argument("--live-turn-timeout", type=float, default=1800, help="bounded real wall wait per saved turn; never a retry")
    parser.add_argument("--require-attached-tui", action="store_true")
    args = parser.parse_args()
    left_data, right_data = load(args.left_jsonl), load(args.right_jsonl)
    if args.check_final_only or args.quality_summary or args.resource_summary or args.live_baseline_dir or args.live_proof_dir:
        args.require_selected_model = True
    if args.require_selected_model:
        providers = (args.left_provider, args.right_provider)
        if "local" not in providers or any(provider not in ("local", "deepseek") for provider in providers):
            parser.error("final selected-model comparison requires local vs existing DeepSeek or local A/B")
        if args.historical_confound:
            parser.error("historical playback cannot bypass selected-model final gate")
        args.require_schema_parity = args.require_complete_pair = True
    if not math.isfinite(args.pause) or args.pause < 0 or not math.isfinite(args.page_hold) or args.page_hold < 0 or not math.isfinite(args.live_turn_timeout) or args.live_turn_timeout <= 0:
        parser.error("pause and page-hold must be finite/nonnegative; live-turn-timeout must be finite/positive")
    if args.live_baseline_dir and args.live_proof_dir:
        parser.error("choose one LIVE phase; never create a second baseline during a take")
    if args.check_schema_parity_only:
        args.require_schema_parity = True
    if args.require_schema_parity:
        try:
            expected_schema = require_schema_parity(left_data, args.left_provider, args.left_profile)
            require_schema_parity(right_data, args.right_provider, args.right_profile, expected_schema)
        except ValueError as error:
            parser.error(str(error))
    if args.require_complete_pair:
        expected_keys = {(f"q{number:02d}", repeat) for number in range(1, 11) for repeat in (1, 2)}
        selected = []
        for dataset, provider, profile in (
            (left_data, args.left_provider, args.left_profile),
            (right_data, args.right_provider, args.right_profile),
        ):
            terminal = [
                record for record in dataset
                if record.get("provider") == provider
                and (not profile or record.get("profile", {}).get("name") == profile)
                and record.get("status") != "planned"
            ]
            keyed = {(record.get("question_id"), int(record.get("repeat", 0))): record for record in terminal}
            if len(terminal) != 20 or set(keyed) != expected_keys:
                parser.error(f"{provider}/{profile or 'all profiles'} must have exactly one terminal attempt for each q01-q10/repeat 1-2")
            selected.append(keyed)
        for key in sorted(expected_keys):
            left_row, right_row = (selected[0][key], selected[1][key])
            if (
                not left_row.get("question")
                or left_row.get("question") != right_row.get("question")
                or not left_row.get("context_sha256")
                or left_row.get("context_sha256") != right_row.get("context_sha256")
            ):
                parser.error(f"{key[0]} repeat {key[1]} lacks a matched original question and nonempty frozen-context digest")
    reviews = None
    if args.require_selected_model:
        if not args.frozen or not args.left_review or not args.right_review:
            parser.error("selected-model final gate requires --frozen, --left-review, --right-review")
        try:
            if file_digest(args.frozen) != FROZEN_SHA256:
                raise ValueError("frozen bytes differ from approved immutable SHA256")
            for path, provider in ((args.left_jsonl, args.left_provider), (args.right_jsonl, args.right_provider)):
                if provider == "deepseek" and file_digest(path) != DEEPSEEK_REPORT_SHA256:
                    raise ValueError("final comparison must reuse the existing DeepSeek20 bytes; no new cloud run")
            with open(args.frozen, encoding="utf-8") as source:
                frozen = json.load(source)
            left_instruction = require_frozen(left_data, args.left_provider, args.left_profile, frozen)
            right_instruction = require_frozen(right_data, args.right_provider, args.right_profile, frozen)
            if left_instruction != right_instruction:
                raise ValueError("full schema instructions differ across arms")
            reviews = [
                semantic_review(args.left_review, args.left_jsonl, selected[0], args.left_provider),
                semantic_review(args.right_review, args.right_jsonl, selected[1], args.right_provider),
            ]
            if args.live_baseline_dir:
                live_baseline(args)
                return 0
            if args.live_proof_dir:
                live_proof(args)
                return 0
        except (ValueError, OSError, KeyError, TypeError, sqlite3.Error, subprocess.SubprocessError) as error:
            parser.error(str(error))
    if args.check_final_only:
        print("Final gates: selected4B pin; exact approved frozen input; all20 captured native schema/effective prompts; both exact-byte independent reviews. No model call.")
        return 0
    if args.check_schema_parity_only:
        print("Twenty terminal captured requests have matching schema instructions; local requests use the exact strict native JSON Schema.")
        return 0
    left = right = None
    table_view = args.summary or args.quality_summary or args.resource_summary
    if table_view:
        if not left_data or not right_data:
            parser.error("both JSONL reports must contain lifecycle observations")
        left_keys = {(r.get("question_id"), int(r.get("repeat", 0))) for r in left_data if r.get("provider") == args.left_provider and (not args.left_profile or r.get("profile", {}).get("name") == args.left_profile)}
        right_keys = {(r.get("question_id"), int(r.get("repeat", 0))) for r in right_data if r.get("provider") == args.right_provider and (not args.right_profile or r.get("profile", {}).get("name") == args.right_profile)}
        keys = sorted((left_keys | right_keys) - {(None, 0)})
        if not keys:
            parser.error("no lifecycle observations for the selected providers/profiles")
        for question_id, repeat in keys:
            left_row = observation(left_data, args.left_provider, question_id, repeat, args.left_profile)
            right_row = observation(right_data, args.right_provider, question_id, repeat, args.right_profile)
            if left_row and right_row and left_row.get("context_sha256") != right_row.get("context_sha256"):
                parser.error(f"{question_id} repeat {repeat} has different frozen context digests")
            if left_row and right_row and left_row.get("question") != right_row.get("question"):
                parser.error(f"{question_id} repeat {repeat} does not contain the same original question")
    else:
        left_ids = {r.get("question_id") for r in left_data if r.get("provider") == args.left_provider}
        right_ids = {r.get("question_id") for r in right_data if r.get("provider") == args.right_provider}
        shared = sorted((left_ids & right_ids) - {None})
        qid = args.question or (shared[0] if shared else "")
        left = observation(left_data, args.left_provider, qid, args.repeat, args.left_profile)
        right = observation(right_data, args.right_provider, qid, args.repeat, args.right_profile)
        if left is not None and right is not None and left.get("context_sha256") != right.get("context_sha256"):
            parser.error("paired observations have different frozen context digests; refusing a misleading comparison")
        if left is not None and right is not None and left.get("question") != right.get("question"):
            parser.error("paired observations do not contain the same original question")
        if not qid or (left is None and right is None):
            parser.error("no matching observations; choose a shared question_id and matching repeat/profile")
        question = (left or right).get("question", "")
    try:
        cols, rows = os.get_terminal_size()
    except OSError:
        cols, rows = int(os.environ.get("COLUMNS", "120")), int(os.environ.get("LINES", "34"))
    if cols < 82 or rows < 14:
        print("Terminal is too small: comparison requires at least 82 columns × 14 rows.", file=sys.stderr)
        return 2
    gutter = 7
    left_width = (cols - gutter) // 2
    right_width = cols - gutter - left_width
    if table_view:
        title = ("Independent quality / repeat / timing review · denominator20" if args.quality_summary
                 else "Actual resource snapshots · missingness explicit" if args.resource_summary
                 else "All questions/repeats · lifecycle/timing table · not a semantic quality review")
        header = wrap(title, cols - 2)
        question_lines = []
        identity_lines = wrap(
            "Reports SHA256 " + file_digest(args.left_jsonl)[:12] + " / " + file_digest(args.right_jsonl)[:12]
            if args.require_selected_model else "Frozen context/build identity shown per observation below", cols - 2)
    else:
        header = wrap(f"Saved report playback · {qid} · repeat {args.repeat} · frozen retrieval context", cols - 2)
        question_lines = wrap("Question: " + question, cols - 2)
        context_digest = (left or right).get("context_sha256") or ""
        left_build = (left or {}).get("retrieval_sha256") or "unavailable"
        right_build = (right or {}).get("retrieval_sha256") or "unavailable"
        identity_lines = wrap(f"Frozen context {context_digest[:12] or 'unavailable'}", cols - 2)
        if left_build == right_build and left_build != "unavailable":
            identity_lines += wrap(f"Shared retrieval/build {left_build[:12]}", cols - 2)
        else:
            identity_lines += wrap(f"Left retrieval/build {left_build[:12] if left_build != 'unavailable' else left_build}", cols - 2)
            identity_lines += wrap(f"Right retrieval/build {right_build[:12] if right_build != 'unavailable' else right_build}", cols - 2)
    header += identity_lines
    banner = wrap("SAVED REPORT PLAYBACK — no model call is made", cols)
    if args.historical_confound:
        protocol = historical_protocol_label(
            left_data, right_data, args.left_provider, args.right_provider,
            args.left_profile, args.right_profile,
        )
        banner = wrap("HISTORICAL PLAYBACK — " + protocol + "; do not compare quality", cols) + banner
    left_title = wrap(panel_title(left, args.left_label, args.left_provider, args.left_profile), left_width)
    right_title = wrap(panel_title(right, args.right_label, args.right_provider, args.right_profile), right_width)
    title_height = max(len(left_title), len(right_title))
    footer_height = len(wrap("Page 999999999999/999999999999 · Enter next · q quit", cols))
    if table_view:
        left_card = right_card = []
        profile_card_height = status_height = 0
        left_status = right_status = []
        body_height = rows - len(banner) - len(header) - len(question_lines) - title_height - footer_height - 3
        if args.quality_summary or args.resource_summary:
            left_groups = [wrap(line, left_width) for line in quality_lines(selected[0], reviews[0], args.left_provider, args.left_profile, args.resource_summary)]
            right_groups = [wrap(line, right_width) for line in quality_lines(selected[1], reviews[1], args.right_provider, args.right_profile, args.resource_summary)]
        else:
            left_groups = summary_lines(left_data, args.left_provider, args.left_profile, keys, left_width)
            right_groups = summary_lines(right_data, args.right_provider, args.right_profile, keys, right_width)
        left_lines, right_lines = [], []
        for left_group, right_group in zip(left_groups, right_groups):
            row_height = max(len(left_group), len(right_group))
            left_lines.extend(left_group + [""] * (row_height - len(left_group)))
            right_lines.extend(right_group + [""] * (row_height - len(right_group)))
    else:
        full_left_card = profile_card_lines(left, left_width)
        full_right_card = profile_card_lines(right, right_width)
        left_card = wrap(actual_model_caption(captured_request(left)), left_width) if args.require_selected_model else full_left_card
        right_card = wrap(actual_model_caption(captured_request(right)), right_width) if args.require_selected_model else full_right_card
        profile_card_height = max(len(left_card), len(right_card)) + 3
        left_status = status_strip_lines(left, left_width)
        right_status = status_strip_lines(right, right_width)
        status_height = max(len(left_status), len(right_status))
        body_height = rows - len(banner) - len(header) - len(question_lines) - profile_card_height - title_height - status_height - footer_height - 3
        left_lines = panel_lines(left, left_width)
        right_lines = panel_lines(right, right_width)
        if args.require_selected_model:
            # Full provenance is paged, not hidden; it cannot consume the entire
            # small viewport before any actual answer/citation can be displayed.
            left_lines = full_left_card + ["", "ACTUAL ANSWER / CITATIONS:"] + left_lines
            right_lines = full_right_card + ["", "ACTUAL ANSWER / CITATIONS:"] + right_lines
    if body_height < 1:
        print("Terminal is too short for the selected view; enlarge the terminal.", file=sys.stderr)
        return 2
    total_pages = max((max(len(left_lines), len(right_lines)) + body_height - 1) // body_height, 1)
    for page in range(total_pages):
        start = page * body_height
        print("\033[2J\033[H", end="")
        for line in banner:
            print(line)
        for line in header:
            print(" " + line)
        for line in question_lines:
            print(" " + line)
        if not table_view:
            print("┌" + "─" * (left_width + 2) + "┬" + "─" * (right_width + 2) + "┐")
            print(border("Profile / captured request", "Profile / captured request", left_width, right_width))
            for index in range(profile_card_height - 3):
                l = left_card[index] if index < len(left_card) else ""
                r = right_card[index] if index < len(right_card) else ""
                print(border(l, r, left_width, right_width))
            print("└" + "─" * (left_width + 2) + "┴" + "─" * (right_width + 2) + "┘")
        print("┌" + "─" * (left_width + 2) + "┬" + "─" * (right_width + 2) + "┐")
        for index in range(title_height):
            l = left_title[index] if index < len(left_title) else ""
            r = right_title[index] if index < len(right_title) else ""
            print(border(l, r, left_width, right_width))
        left_page = left_lines[start:start + body_height]
        right_page = right_lines[start:start + body_height]
        for index in range(status_height):
            l = left_status[index] if index < len(left_status) else ""
            r = right_status[index] if index < len(right_status) else ""
            print(border(l, r, left_width, right_width))
        for index in range(body_height):
            l = left_page[index] if index < len(left_page) else "— end —"
            r = right_page[index] if index < len(right_page) else "— end —"
            print(border(l, r, left_width, right_width))
        print("└" + "─" * (left_width + 2) + "┴" + "─" * (right_width + 2) + "┘")
        footer = wrap(f"Page {page + 1}/{total_pages} · Enter next · q quit", cols)
        for line in footer:
            print(line)
        if args.page_hold > 0:
            import time
            time.sleep(max(args.pause, args.page_hold))
        elif args.pause > 0:
            import time
            page_words = sum(len(line.split()) for line in left_page + right_page)
            time.sleep(max(args.pause, page_words / 3.0))
        elif sys.stdin.isatty() or page + 1 < total_pages:
            if input()[:1].lower() == "q":
                break
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
