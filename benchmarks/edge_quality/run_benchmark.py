#!/usr/bin/env python3
"""
Edge quality benchmark using DialAM-2024 / QT30 gold annotations.

Converts annotated dialogue episodes into synthetic transcripts, runs slimemold
extraction, and measures:
  1. Claim recall: what fraction of gold I-nodes did slimemold extract?
  2. Edge recall: what fraction of gold RA/CA relations appear in extracted edges?
  3. Edge precision: what fraction of extracted edges correspond to gold relations?
  4. Relation type accuracy: when an edge is matched, is the type correct?

Matching uses word overlap (Jaccard similarity) between gold and extracted text.
"""

import json
import os
import re
import sqlite3
import subprocess
import sys
import tempfile
import time
from pathlib import Path

BENCHMARK_DIR = Path(__file__).parent
SAMPLES_FILE = BENCHMARK_DIR / "samples.json"
RESULTS_FILE = BENCHMARK_DIR / "results.json"

SLIMEMOLD_BIN = Path(__file__).parent.parent.parent / "slimemold"
PROJECT_NAME = "benchmark-edges"

# Match threshold: minimum word overlap to consider a match
CLAIM_MATCH_THRESHOLD = 0.25  # gold claims are paraphrased, need looser matching
EDGE_MATCH_THRESHOLD = 0.20


def word_set(text):
    """Normalize text to a set of lowercase words."""
    return set(re.findall(r'\w+', text.lower()))


def jaccard(a, b):
    """Jaccard similarity between two strings."""
    sa, sb = word_set(a), word_set(b)
    if not sa or not sb:
        return 0.0
    return len(sa & sb) / len(sa | sb)


def best_match(gold_text, candidates, threshold):
    """Find the best-matching candidate for a gold text."""
    best_score = 0
    best_candidate = None
    for cand in candidates:
        score = jaccard(gold_text, cand["text"])
        if score > best_score:
            best_score = score
            best_candidate = cand
    if best_score >= threshold:
        return best_candidate, best_score
    return None, best_score


def build_transcript_jsonl(messages):
    """Build a Claude Code–style JSONL transcript from messages."""
    lines = []
    for i, msg in enumerate(messages):
        entry = {
            "role": msg["role"],
            "content": msg["text"],
            "turn": i + 1,
        }
        lines.append(json.dumps(entry))
    return "\n".join(lines)


def run_extraction(transcript_jsonl, project):
    """Run slimemold extraction on a transcript and return (claims, edges) from DB."""
    # Write transcript to temp file
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        f.write(transcript_jsonl)
        transcript_path = f.name

    try:
        # Reset project
        subprocess.run(
            [str(SLIMEMOLD_BIN), "--project", project, "reset"],
            capture_output=True, timeout=30,
        )

        # Build MCP request for parse_transcript
        request = json.dumps({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/call",
            "params": {
                "name": "claims",
                "arguments": {
                    "action": "parse_transcript",
                    "project": project,
                    "transcript_path": transcript_path,
                },
            },
        })

        # We need to send initialize first, then the tool call
        init_request = json.dumps({
            "jsonrpc": "2.0",
            "id": 0,
            "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "benchmark", "version": "1.0"},
            },
        })

        full_input = init_request + "\n" + request + "\n"

        result = subprocess.run(
            [str(SLIMEMOLD_BIN), "--project", project, "mcp"],
            input=full_input,
            capture_output=True,
            text=True,
            timeout=180,
        )

        if result.returncode != 0 and result.stderr:
            print(f"  MCP stderr: {result.stderr[:200]}", file=sys.stderr)

        # Read results from SQLite
        data_dir = os.path.expanduser("~/.slimemold")
        # DB is opened by CWD name, not project override
        cwd_project = os.path.basename(os.getcwd())
        db_path = os.path.join(data_dir, cwd_project, "graph.sqlite")

        # Try project-named DB first, fall back to cwd-named
        project_db = os.path.join(data_dir, project, "graph.sqlite")
        if os.path.exists(project_db):
            db_path = project_db

        if not os.path.exists(db_path):
            print(f"  DB not found at {db_path}", file=sys.stderr)
            return [], []

        conn = sqlite3.connect(db_path)
        conn.row_factory = sqlite3.Row

        claims = [dict(r) for r in conn.execute(
            "SELECT * FROM claims WHERE project = ?", (project,)
        ).fetchall()]

        edges = [dict(r) for r in conn.execute(
            "SELECT e.* FROM edges e JOIN claims c ON e.from_id = c.id WHERE c.project = ?",
            (project,)
        ).fetchall()]

        conn.close()
        return claims, edges

    finally:
        os.unlink(transcript_path)


def evaluate_episode(sample, extracted_claims, extracted_edges):
    """Compare extracted claims/edges against gold annotations."""
    gold_claims = sample["gold_claims"]  # {nodeID: text}
    gold_relations = sample["gold_relations"]

    # --- Claim matching ---
    # For each gold claim, find best extracted match
    claim_matches = {}  # gold_nodeID -> extracted_claim
    matched_extracted = set()

    for gold_id, gold_text in gold_claims.items():
        match, score = best_match(gold_text, extracted_claims, CLAIM_MATCH_THRESHOLD)
        if match and match["id"] not in matched_extracted:
            claim_matches[gold_id] = {
                "extracted_id": match["id"],
                "extracted_text": match["text"],
                "gold_text": gold_text,
                "similarity": score,
            }
            matched_extracted.add(match["id"])

    claim_recall = len(claim_matches) / len(gold_claims) if gold_claims else 0

    # --- Edge matching ---
    # Map: slimemold relation types to gold types
    # slimemold: supports, depends_on, contradicts
    # gold: supports (RA), contradicts (CA)
    # depends_on is the inverse of supports

    # Build extracted edge set with text
    extracted_edge_set = []
    claim_by_id = {c["id"]: c for c in extracted_claims}
    for e in extracted_edges:
        from_claim = claim_by_id.get(e["from_id"])
        to_claim = claim_by_id.get(e["to_id"])
        if from_claim and to_claim:
            extracted_edge_set.append({
                "from_text": from_claim["text"],
                "to_text": to_claim["text"],
                "from_id": e["from_id"],
                "to_id": e["to_id"],
                "relation": e["relation"],
            })

    # For each gold relation, check if we found it
    gold_edge_matches = []
    matched_extracted_edges = set()

    for gr in gold_relations:
        # Find the extracted claims that match the gold source and target
        gold_from_matched = claim_matches.get(gr["from_id"])
        gold_to_matched = claim_matches.get(gr["to_id"])

        if not gold_from_matched or not gold_to_matched:
            gold_edge_matches.append({
                "gold": gr,
                "found": False,
                "reason": "endpoint_claims_not_matched",
            })
            continue

        ext_from_id = gold_from_matched["extracted_id"]
        ext_to_id = gold_to_matched["extracted_id"]

        # Look for an edge between these extracted claims (in either direction)
        found_edge = None
        for i, ee in enumerate(extracted_edge_set):
            if i in matched_extracted_edges:
                continue
            # Direct match
            if ee["from_id"] == ext_from_id and ee["to_id"] == ext_to_id:
                found_edge = ee
                matched_extracted_edges.add(i)
                break
            # Inverse match (depends_on is inverse of supports)
            if ee["from_id"] == ext_to_id and ee["to_id"] == ext_from_id:
                found_edge = ee
                matched_extracted_edges.add(i)
                break

        if found_edge:
            # Check relation type
            gold_rel = gr["relation"]  # supports or contradicts
            ext_rel = found_edge["relation"]

            # Normalize: depends_on and supports are both "support" relations
            gold_norm = "support" if gold_rel == "supports" else "conflict"
            ext_norm = "support" if ext_rel in ("supports", "depends_on") else "conflict"

            gold_edge_matches.append({
                "gold": gr,
                "found": True,
                "extracted_relation": ext_rel,
                "type_correct": gold_norm == ext_norm,
            })
        else:
            gold_edge_matches.append({
                "gold": gr,
                "found": False,
                "reason": "no_edge_between_matched_claims",
            })

    edge_found = sum(1 for m in gold_edge_matches if m["found"])
    edge_recall = edge_found / len(gold_relations) if gold_relations else 0

    type_correct = sum(1 for m in gold_edge_matches if m.get("type_correct"))
    type_accuracy = type_correct / edge_found if edge_found else 0

    # Edge precision: matched extracted edges / total extracted edges
    edge_precision = len(matched_extracted_edges) / len(extracted_edge_set) if extracted_edge_set else 0

    return {
        "source_file": sample["source_file"],
        "gold_claims": len(gold_claims),
        "extracted_claims": len(extracted_claims),
        "matched_claims": len(claim_matches),
        "claim_recall": claim_recall,
        "gold_relations": len(gold_relations),
        "extracted_edges": len(extracted_edge_set),
        "matched_edges": edge_found,
        "edge_recall": edge_recall,
        "edge_precision": edge_precision,
        "type_accuracy": type_accuracy,
        "claim_matches": claim_matches,
        "edge_matches": gold_edge_matches,
    }


def main():
    if not SAMPLES_FILE.exists():
        print("Run fetch_samples.py first", file=sys.stderr)
        sys.exit(1)

    if not SLIMEMOLD_BIN.exists():
        print(f"Build slimemold first: {SLIMEMOLD_BIN}", file=sys.stderr)
        sys.exit(1)

    with open(SAMPLES_FILE) as f:
        samples = json.load(f)

    print(f"Running edge quality benchmark on {len(samples)} episodes", file=sys.stderr)
    print(f"Model: {os.environ.get('SLIMEMOLD_MODEL', 'claude-sonnet-4-6 (default)')}", file=sys.stderr)
    print(file=sys.stderr)

    all_results = []

    for i, sample in enumerate(samples):
        ep_name = f"{PROJECT_NAME}-{i}"
        print(f"Episode {i+1}/{len(samples)}: {sample['source_file']} "
              f"({sample['stats']['i_nodes']} claims, "
              f"{sample['stats']['ra']} RA, {sample['stats']['ca']} CA)",
              file=sys.stderr)

        # Build transcript
        transcript = build_transcript_jsonl(sample["transcript"])

        # Run extraction
        claims, edges = run_extraction(transcript, ep_name)
        print(f"  Extracted: {len(claims)} claims, {len(edges)} edges", file=sys.stderr)

        # Evaluate
        result = evaluate_episode(sample, claims, edges)
        all_results.append(result)

        print(f"  Claim recall: {result['claim_recall']:.1%} "
              f"({result['matched_claims']}/{result['gold_claims']})", file=sys.stderr)
        print(f"  Edge recall:  {result['edge_recall']:.1%} "
              f"({result['matched_edges']}/{result['gold_relations']})", file=sys.stderr)
        print(f"  Edge precision: {result['edge_precision']:.1%}", file=sys.stderr)
        if result['matched_edges'] > 0:
            print(f"  Type accuracy:  {result['type_accuracy']:.1%}", file=sys.stderr)
        print(file=sys.stderr)

    # --- Aggregate ---
    print("=" * 60, file=sys.stderr)
    print("AGGREGATE RESULTS", file=sys.stderr)
    print("=" * 60, file=sys.stderr)

    total_gold_claims = sum(r["gold_claims"] for r in all_results)
    total_matched_claims = sum(r["matched_claims"] for r in all_results)
    total_gold_rels = sum(r["gold_relations"] for r in all_results)
    total_matched_edges = sum(r["matched_edges"] for r in all_results)
    total_extracted_edges = sum(r["extracted_edges"] for r in all_results)
    total_type_correct = sum(
        sum(1 for m in r["edge_matches"] if m.get("type_correct"))
        for r in all_results
    )

    agg_claim_recall = total_matched_claims / total_gold_claims if total_gold_claims else 0
    agg_edge_recall = total_matched_edges / total_gold_rels if total_gold_rels else 0
    agg_edge_precision = total_matched_edges / total_extracted_edges if total_extracted_edges else 0
    agg_type_accuracy = total_type_correct / total_matched_edges if total_matched_edges else 0

    print(f"Claim recall:    {agg_claim_recall:.1%} ({total_matched_claims}/{total_gold_claims})", file=sys.stderr)
    print(f"Edge recall:     {agg_edge_recall:.1%} ({total_matched_edges}/{total_gold_rels})", file=sys.stderr)
    print(f"Edge precision:  {agg_edge_precision:.1%} ({total_matched_edges}/{total_extracted_edges})", file=sys.stderr)
    print(f"Type accuracy:   {agg_type_accuracy:.1%} ({total_type_correct}/{total_matched_edges})", file=sys.stderr)

    # Unmatched analysis
    unmatched_reasons = {}
    for r in all_results:
        for m in r["edge_matches"]:
            if not m["found"]:
                reason = m.get("reason", "unknown")
                unmatched_reasons[reason] = unmatched_reasons.get(reason, 0) + 1

    if unmatched_reasons:
        print(f"\nUnmatched edge reasons:", file=sys.stderr)
        for reason, count in sorted(unmatched_reasons.items(), key=lambda x: -x[1]):
            print(f"  {reason}: {count}", file=sys.stderr)

    # Save detailed results
    # Strip non-serializable claim_matches for JSON output
    output = []
    for r in all_results:
        out = {k: v for k, v in r.items() if k not in ("claim_matches", "edge_matches")}
        # Include match details in a serializable form
        out["claim_match_details"] = [
            {"gold_id": gid, **info}
            for gid, info in r["claim_matches"].items()
        ]
        out["edge_match_details"] = r["edge_matches"]
        output.append(out)

    with open(RESULTS_FILE, "w") as f:
        json.dump(output, f, indent=2)

    print(f"\nDetailed results: {RESULTS_FILE}", file=sys.stderr)


if __name__ == "__main__":
    main()
