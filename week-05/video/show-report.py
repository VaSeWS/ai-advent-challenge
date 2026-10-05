#!/usr/bin/env python3
"""Render exact Week 05 JSON reports as wrapped, timed terminal pages."""
import argparse
import hashlib
import json
import os
import shutil
import sys
import textwrap
import time
from pathlib import Path


def load_report(path):
    report_path = Path(path)
    with report_path.open(encoding="utf-8") as stream:
        return report_path, json.load(stream)


def wrapped_pages(lines):
    columns, rows = shutil.get_terminal_size((100, 30))
    width = max(40, columns - 2)
    height = max(8, rows - 3)
    output = []
    for line in lines:
        if not line:
            output.append("")
            continue
        output.extend(textwrap.wrap(str(line), width=width, break_long_words=True,
                                    break_on_hyphens=False, replace_whitespace=False,
                                    drop_whitespace=True) or [""])
    pages = [output[offset:offset + height] for offset in range(0, len(output), height)]
    return pages or [[]]


def display(lines):
    pages = wrapped_pages(lines)
    interactive = sys.stdout.isatty() and os.environ.get("DEMO_DRY_RUN") != "1"
    try:
        hold = max(0.0, float(os.environ.get("DEMO_PAGE_SECONDS", "15")))
    except ValueError:
        raise SystemExit("DEMO_PAGE_SECONDS must be a non-negative number")
    for number, page in enumerate(pages, 1):
        if number > 1:
            print("\n--- report page ---")
        print("\n".join(page))
        print(f"[page {number}/{len(pages)}]", flush=True)
        if interactive and hold:
            time.sleep(hold)


def evaluation(report_path, selected_pairs, label, review_path=None):
    path, report = load_report(report_path)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    review = None
    if review_path:
        with Path(review_path).open(encoding="utf-8") as stream:
            review = json.load(stream)
        source = review.get("source_report", {})
        if source.get("path") != str(path) or source.get("sha256") != digest:
            raise SystemExit("semantic review is not bound to this exact report path and SHA-256")
        if source.get("status") != "complete" or report.get("status") != "complete":
            raise SystemExit("semantic review requires its exact complete source report")
        if not source.get("reviewer"):
            raise SystemExit("semantic review artifact has no reviewer attribution")
        review_map = review.get("manual_review_map", {})
        quality = review.get("source_and_quality_assessment", {})
    else:
        review_map, quality = {}, {}

    comparisons = report.get("comparisons", [])
    settings = report.get("settings", {})
    generation = settings.get("generation", {})
    retrieval = settings.get("retrieval", {})
    lines = [
        f"{label}: {path}",
        f"Status: {report.get('status')} | started: {report.get('started_at')} | finished: {report.get('finished_at')}",
        f"Provider: {settings.get('provider', generation.get('provider'))} | model: {generation.get('main_model')} | mode: {settings.get('mode')} | strategy: {settings.get('strategy')} | comparisons: {len(comparisons)}",
        f"Question set: {report.get('question_set')} | snapshot: {report.get('snapshot_id')} | build: {report.get('build_id')}",
        f"Retrieval: candidate_k={retrieval.get('candidate_k')} context_k={retrieval.get('context_k')} threshold={retrieval.get('threshold')} calibrated={retrieval.get('calibrated')} calibration={retrieval.get('calibration')}",
        f"Report SHA-256: {digest}",
    ]
    if review:
        lines.append(f"Agent-authored review: {review['source_report']['reviewer']} | source path and SHA-256 verified; not a human review.")
        lines.append(f"Review scope: {review.get('review_scope', 'full review artifact')}")
    if report.get("error"):
        lines.append(f"Run error (not suppressed): {report['error']}")

    questions = {}
    for row in comparisons:
        if selected_pairs and row.get("pair") not in selected_pairs:
            continue
        questions.setdefault(row.get("question_id", "?"), []).append(row)
    if not questions:
        lines.append(f"No comparisons matched selected pairs: {', '.join(sorted(selected_pairs))}")
    if review:
        for question_id, rows in questions.items():
            if question_id not in quality:
                raise SystemExit(f"semantic review is missing question assessment {question_id}")
            for row in rows:
                notes = review_map.get(f"{row.get('pair')}/{question_id}")
                observed = ((row.get("run") or {}).get("lanes") or row.get("observed_lanes") or [])
                if notes is None or len(notes) != len(observed):
                    raise SystemExit(f"semantic review is missing lane notes for {row.get('pair')}/{question_id}")
    for question_id, rows in questions.items():
        control = rows[0].get("control_question", {})
        lines.extend(["=" * 72, f"Question {question_id}: {control.get('question', '')}",
                      f"Corpus sufficient: {control.get('corpus_sufficient')}", "Expected facts:"])
        lines.extend(f"  - {fact}" for fact in (control.get("expected_facts") or []))
        lines.append("Expected sources:")
        lines.extend(f"  - document={source.get('document_id')} section={source.get('section_id')}"
                     for source in (control.get("expected_sources") or []))
        if review and question_id in quality:
            assessment = quality[question_id]
            lines.extend([f"Agent review — source presence: {assessment.get('source_presence')}",
                          f"Agent review — quote presence/exactness: {assessment.get('quote_presence_exactness')}",
                          f"Agent review — semantic assessment: {assessment.get('semantic_review')}"])
        for row in rows:
            pair_key = f"{row.get('pair')}/{question_id}"
            lane_reviews = review_map.get(pair_key, [])
            lines.extend([f"PAIR {row.get('pair')}",
                          f"Pair error: {row['error']}" if row.get("error") else ""])
            observed = ((row.get("run") or {}).get("lanes") or row.get("observed_lanes") or [])
            for index, lane in enumerate(observed, 1):
                if lane is None:
                    lines.append(f"Lane {index}: no completed result")
                    continue
                answer = lane.get("answer", {})
                lane_settings = lane.get("settings", {})
                lines.extend([f"Lane {index} settings: mode={lane_settings.get('mode')} strategy={lane_settings.get('strategy')} task_memory={lane_settings.get('task_memory')} rewrite={lane_settings.get('rewrite')}",
                              f"Original query: {lane.get('original_query')} | search query: {lane.get('search_query')}",
                              f"Answer status: {answer.get('status')}", f"Task state: {json.dumps(lane.get('state'), ensure_ascii=False, sort_keys=True)}",
                              "Full answer:", answer.get("answer", ""), "Retrieved context:"])
                for item in lane.get("context") or []:
                    chunk = item.get("chunk", item.get("Chunk", {}))
                    lines.append(f"Context chunk={chunk.get('id', chunk.get('ID'))} source={chunk.get('title', chunk.get('Title'))} sections={chunk.get('section_paths', chunk.get('SectionPaths'))} similarity={1 - item.get('distance', item.get('Distance', 1)):.6f}")
                lines.append("Citations:")
                for citation in answer.get("citations") or []:
                    lines.append(f"  chunk={citation.get('chunk_id')} quote={citation.get('quote')}")
                if review and index <= len(lane_reviews):
                    note = lane_reviews[index - 1]
                    lines.append(f"Agent review lane {index}: correctness={note.get('correctness')} semantic_support={note.get('semantic_support')} refusal_correctness={note.get('refusal_correctness')}")
            for index, metric in enumerate(row.get("lane_metrics") or [], 1):
                if metric is None:
                    continue
                lines.append(f"Lane {index} observed checks:")
                for fact in metric.get("expected_facts") or []:
                    lines.append(f"  fact exact_substring_present={fact.get('exact_substring_present')}: {fact.get('fact')}")
                for source in metric.get("expected_sources") or []:
                    lines.append(f"  source={source.get('document_id')} section={source.get('section_id')} source_present={source.get('source_present')} quote_present={source.get('quote_present')} quotes_exact={source.get('quotes_exact')} context_hit={source.get('context_hit')} cited_hit={source.get('cited_hit')}")
                    for chunk in source.get("chunks") or []:
                        lines.append(f"    role={chunk.get('role')} chunk={chunk.get('chunk_id')} quote_exact={chunk.get('quote_exact')} quote={chunk.get('quote')}")
                lines.append(f"Stored manual-review fields: {metric.get('manual_review')}")
    if review:
        lines.append(f"Agent review aggregate: {review.get('aggregate', {}).get('manual_assessment')}")
        lines.append(f"Review method/limits: {review.get('aggregate', {}).get('method_and_limit')}")
    if report.get("status") != "complete":
        lines.append("Report is incomplete; no completion or acceptance claim is made.")
    display(lines)


def scenarios(report_paths, review_path=None, label="SCENARIO REPORTS"):
    review = None
    if review_path:
        with Path(review_path).open(encoding="utf-8") as stream:
            review = json.load(stream)
    if not review_path:
        if len(report_paths) != 4:
            raise SystemExit(f"expected exactly four current-run scenario reports, got {len(report_paths)}")
        grouped = {}
        for report_path in report_paths:
            _, item = load_report(report_path)
            grouped.setdefault(item.get("scenario_id"), []).append(item)
        if set(grouped) != {"progression", "ore-processing"}:
            raise SystemExit("current run must contain progression and ore-processing reports")
        sessions = set()
        for scenario_id, pair in grouped.items():
            pair.sort(key=lambda item: item.get("started_at", ""))
            if len(pair) != 2 or pair[0].get("status") != "checkpoint" or len(pair[0].get("turns", [])) != 10:
                raise SystemExit(f"{scenario_id} must show one turn-10 checkpoint and one resume report")
            if pair[1].get("status") != "complete" or len(pair[1].get("turns", [])) != 12:
                raise SystemExit(f"{scenario_id} resume report must be complete through turn 12")
            if pair[0].get("session_id") != pair[1].get("session_id"):
                raise SystemExit(f"{scenario_id} checkpoint and resume reports must share one session")
            sessions.add(pair[0].get("session_id"))
        if len(sessions) != 2:
            raise SystemExit("progression and ore-processing must use independent sessions")
    for report_path in report_paths:
        path, report = load_report(report_path)
        scenario_id = report.get("scenario_id")
        scenario_key = "ore_processing" if scenario_id == "ore-processing" else scenario_id
        report_digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if review:
            provenance = review.get("provenance", {}).get(scenario_key, {})
            if provenance.get("report") != str(path) or provenance.get("sha256") != report_digest:
                raise SystemExit(f"scenario review is not bound to exact report path and SHA-256: {path}")
        turns = report.get("turns", [])
        lines = ["=" * 72, f"{label}: {path}",
                 f"Scenario={scenario_id} status={report.get('status')} session={report.get('session_id')} turns={len(turns)} started={report.get('started_at')} finished={report.get('finished_at')}",
                 f"Report SHA-256: {report_digest}"]
        settings = report.get("settings", {})
        generation = settings.get("generation", {})
        retrieval = settings.get("retrieval", {})
        lines.append(f"Provider={settings.get('provider', generation.get('provider'))} model={generation.get('main_model')} strategy={settings.get('strategy')} endpoint={generation.get('endpoint')}")
        lines.append(f"Snapshot={report.get('snapshot_id')} build={report.get('build_id')} retrieval candidate_k={retrieval.get('candidate_k')} context_k={retrieval.get('context_k')} threshold={retrieval.get('threshold')} calibration={retrieval.get('calibration')}")
        if review:
            lines.append("Agent-authored semantic review: bound to this exact historical report; not a human review.")
        for turn in turns:
            ordinal = turn.get("ordinal")
            turn_key = f"turn-{ordinal:02d}" if isinstance(ordinal, int) else f"turn-{ordinal}"
            lines.extend([f"Turn {ordinal}: {turn.get('question')}",
                          f"Expected goal: {turn.get('expected_goal')}",
                          f"Expected constraints: {turn.get('expected_constraints')}",
                          f"Corpus sufficient: {turn.get('corpus_sufficient')}"])
            lane_reviews = (review or {}).get("scenario_manual_review_maps", {}).get(scenario_id, {}).get(turn_key, [])
            for index, lane in enumerate(turn.get("lanes", []), 1):
                result = lane.get("result", {})
                answer = result.get("answer", {})
                lane_settings = result.get("settings", {})
                lines.extend([f"Lane {index}: mode={lane_settings.get('mode')} strategy={lane_settings.get('strategy')} task_memory={lane_settings.get('task_memory')}",
                              f"Answer status: {answer.get('status')}", "Full answer:", answer.get("answer", ""),
                              "Expected-source checks:"])
                for source in lane.get("expected_sources") or []:
                    lines.append(f"  document={source.get('document_id')} section={source.get('section_id')} source_present={source.get('source_present')} quote_present={source.get('quote_present')} quotes_exact={source.get('quotes_exact')}")
                lines.append("Citations:")
                for citation in answer.get("citations") or []:
                    lines.append(f"  chunk={citation.get('chunk_id')} quote={citation.get('quote')}")
                lines.append(f"Task state: {json.dumps(result.get('state'), ensure_ascii=False, sort_keys=True)}")
                lines.append(f"Stored manual-review fields: {lane.get('manual_review')}")
                if review and index <= len(lane_reviews):
                    note = lane_reviews[index - 1]
                    lines.append(f"Agent review lane {index}: goal_correctness={note.get('goal_correctness')} constraint_compliance={note.get('constraint_compliance')} semantic_support={note.get('semantic_support')} refusal_correctness={note.get('refusal_correctness')}")
        notes = (review or {}).get("factual_notes", {}).get(scenario_key, {})
        for key, value in notes.items():
            lines.append(f"Agent review {key}: {value}")
        if report.get("status") not in ("checkpoint", "complete"):
            lines.append("This report is incomplete; no completion or acceptance claim is made.")
        display(lines)


def corpus(snapshot_path, cli_output):
    path, snapshot = load_report(snapshot_path)
    summary = []
    prefixes = ("Snapshot ID:", "Captured:", "Documents:",
                "Normalized source words", "Page-equivalent estimate",
                "Limits:", "Corpus size gate:")
    for line in Path(cli_output).read_text(encoding="utf-8").splitlines():
        if line.startswith(prefixes):
            summary.append(line)
    if not summary:
        raise SystemExit(f"no corpus report summary found in {cli_output}")
    documents = snapshot.get("documents", [])
    lines = [f"Corpus snapshot summary: {path}", *summary, "Pinned page provenance:"]
    for document in documents:
        lines.append(f"{document.get('title')} | page={document.get('page_id')} revision={document.get('revision_id')} ({document.get('revision_timestamp')}) | sections={len(document.get('sections', []))} | license={document.get('license')} | {document.get('permalink')}")
    lines.append("Normalized text/recorded sections only; images, NEI, Quest Book, absent game evidence, and modpack-version certainty are outside this snapshot.")
    display(lines)


def index_summary(input_path):
    lines = []
    keep = ("Active/index build:", "Snapshot:", "Embedding model:", "Embedding digest:",
            "Preparation:", "Dimensions:", "Chunk size/overlap:", "Full source text coverage")
    for line in Path(input_path).read_text(encoding="utf-8").splitlines():
        if line.startswith(keep) or " persisted chunks:" in line:
            lines.append(line)
    if not lines:
        raise SystemExit(f"no index summary fields found in {input_path}")
    display(["Read-only index summary (full inspect ran once):", *lines])

def index_search(input_path):
    lines = Path(input_path).read_text(encoding="utf-8").splitlines()
    start = next((index for index, line in enumerate(lines) if line.startswith("Search:")), None)
    if start is None:
        raise SystemExit(f"no search result found in {input_path}")
    display(["Selected full search hit (the complete chunk text and provenance):", *lines[start:]])


def checkpoint(log_path):
    import re
    text = Path(log_path).read_text(encoding="utf-8")
    matches = re.findall(r"SCENARIO_CHECKPOINT scenario=\S+ session=([0-9a-fA-F-]{36}) committed=10 next=11", text)
    if len(matches) != 1:
        raise SystemExit(f"expected exactly one turn-10 SCENARIO_CHECKPOINT in {log_path}, found {len(matches)}")
    print(matches[0])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    evaluation_parser = subparsers.add_parser("evaluation")
    evaluation_parser.add_argument("--report", required=True, help="exact evaluation JSON report to inspect")
    evaluation_parser.add_argument("--pair", action="append", default=[], help="include this comparison pair; repeat as needed")
    evaluation_parser.add_argument("--review-json", help="agent-authored review bound to the exact report digest")
    evaluation_parser.add_argument("--label", default="REPORT REVIEW")
    scenario_parser = subparsers.add_parser("scenarios")
    scenario_parser.add_argument("reports", nargs="+", help="exact scenario JSON report paths, in checkpoint/resume order")
    scenario_parser.add_argument("--review-json", help="semantic review bound to exact historical scenario reports")
    scenario_parser.add_argument("--label", default="SCENARIO REPORTS")
    corpus_parser = subparsers.add_parser("corpus")
    corpus_parser.add_argument("--cli-output", required=True)
    corpus_parser.add_argument("--snapshot", required=True)
    index_parser = subparsers.add_parser("index-summary")
    index_parser.add_argument("--input", required=True)
    search_parser = subparsers.add_parser("index-search")
    search_parser.add_argument("--input", required=True)
    checkpoint_parser = subparsers.add_parser("checkpoint")
    checkpoint_parser.add_argument("log")
    args = parser.parse_args()
    if args.command == "checkpoint":
        checkpoint(args.log)
    elif args.command == "scenarios":
        scenarios(args.reports, args.review_json, args.label)
    elif args.command == "corpus":
        corpus(args.snapshot, args.cli_output)
    elif args.command == "index-summary":
        index_summary(args.input)
    elif args.command == "index-search":
        index_search(args.input)
    else:
        evaluation(args.report, set(args.pair), args.label, args.review_json)


if __name__ == "__main__":
    main()
