#!/usr/bin/env python3
"""
Basis Classification Benchmark for Slimemold.

Constructs a synthetic conversation transcript from known-provenance samples,
runs slimemold extraction, and scores basis classification accuracy.

Usage:
    python3 benchmarks/basis_classification/fetch_samples.py   # first time only
    python3 benchmarks/basis_classification/run_benchmark.py
"""

import json
import os
import subprocess
import sys
import tempfile
from collections import defaultdict

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.abspath(os.path.join(SCRIPT_DIR, "..", ".."))
SAMPLES_FILE = os.path.join(SCRIPT_DIR, "samples.json")
BINARY = os.path.join(REPO_ROOT, "slimemold")
PROJECT = "benchmark-basis"


def build_transcript(samples: list[dict]) -> str:
    """Build a JSONL transcript from samples in Claude Code format."""
    lines = []
    for i, sample in enumerate(samples):
        role = sample.get("speaker", "user")
        entry = {
            "role": role,
            "content": [{"type": "text", "text": sample["text"]}],
        }
        lines.append(json.dumps(entry))
    return "\n".join(lines)


def extract_claims(transcript_path: str) -> dict:
    """Run slimemold extraction and return raw MCP response."""
    payload = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "claims",
            "arguments": {
                "action": "parse_transcript",
                "project": PROJECT,
                "transcript_path": transcript_path,
                "since_turn": 0,
            }
        }
    })

    # Reset project first
    reset_payload = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "claims",
            "arguments": {
                "action": "parse_transcript",
                "project": PROJECT,
                "transcript_path": "/dev/null",
            }
        }
    })

    proc = subprocess.Popen(
        [BINARY, "mcp"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        cwd=REPO_ROOT,
    )
    try:
        stdout, stderr = proc.communicate(input=payload + "\n", timeout=180)
    except subprocess.TimeoutExpired:
        proc.kill()
        stdout, stderr = proc.communicate()
        print("Extraction timed out after 180s")
        return {}

    stdout = stdout.strip()
    if not stdout:
        print(f"Empty MCP response (exit code {proc.returncode})")
        print(f"stderr: {stderr[:500]}")
        return {}

    try:
        return json.loads(stdout)
    except json.JSONDecodeError:
        print(f"Failed to parse MCP response: {stdout[:500]}")
        return {}


def get_claims_from_db() -> list[dict]:
    """Read extracted claims directly from SQLite."""
    import sqlite3
    db_path = os.path.expanduser(f"~/.slimemold/{os.path.basename(REPO_ROOT)}/graph.sqlite")
    if not os.path.exists(db_path):
        print(f"Database not found at {db_path}")
        return []

    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.execute(
        "SELECT id, text, basis, confidence, speaker FROM claims WHERE project = ?",
        (PROJECT,)
    )
    claims = [dict(row) for row in cursor.fetchall()]
    conn.close()
    return claims


def match_claims_to_samples(claims: list[dict], samples: list[dict]) -> list[dict]:
    """Match extracted claims back to source samples using text similarity."""
    results = []
    used_claims = set()

    for sample in samples:
        sample_words = set(sample["text"].lower().split())
        best_match = None
        best_overlap = 0

        for claim in claims:
            if claim["id"] in used_claims:
                continue
            claim_words = set(claim["text"].lower().split())
            overlap = len(sample_words & claim_words) / max(len(sample_words | claim_words), 1)
            if overlap > best_overlap:
                best_overlap = overlap
                best_match = claim

        if best_match and best_overlap > 0.15:  # minimum 15% word overlap
            used_claims.add(best_match["id"])
            results.append({
                "sample_text": sample["text"][:80],
                "expected_basis": sample["expected_basis"],
                "extracted_basis": best_match["basis"],
                "extracted_text": best_match["text"][:80],
                "confidence": best_match.get("confidence", 0),
                "match_quality": round(best_overlap, 2),
                "correct": sample["expected_basis"] == best_match["basis"],
            })
        else:
            results.append({
                "sample_text": sample["text"][:80],
                "expected_basis": sample["expected_basis"],
                "extracted_basis": "NOT_EXTRACTED",
                "extracted_text": "",
                "confidence": 0,
                "match_quality": 0,
                "correct": False,
            })

    return results


def print_report(results: list[dict]):
    """Print benchmark results."""
    total = len(results)
    correct = sum(1 for r in results if r["correct"])
    extracted = sum(1 for r in results if r["extracted_basis"] != "NOT_EXTRACTED")
    not_extracted = total - extracted

    print("\n" + "=" * 70)
    print("SLIMEMOLD BASIS CLASSIFICATION BENCHMARK")
    print("=" * 70)

    print(f"\nOverall: {correct}/{total} correct ({correct/total*100:.1f}%)")
    print(f"Extracted: {extracted}/{total} ({extracted/total*100:.1f}%)")
    print(f"Not matched: {not_extracted}/{total}")

    # Per-basis breakdown
    by_expected = defaultdict(lambda: {"total": 0, "correct": 0, "extracted": 0, "got": defaultdict(int)})
    for r in results:
        b = r["expected_basis"]
        by_expected[b]["total"] += 1
        if r["extracted_basis"] != "NOT_EXTRACTED":
            by_expected[b]["extracted"] += 1
            by_expected[b]["got"][r["extracted_basis"]] += 1
        if r["correct"]:
            by_expected[b]["correct"] += 1

    print("\n--- Per-Basis Results ---\n")
    print(f"{'Expected':<15} {'Correct':<10} {'Extracted':<12} {'Accuracy':<10} {'Distribution of extracted'}")
    print("-" * 70)
    for basis in ["research", "deduction", "empirical", "llm_output", "vibes"]:
        info = by_expected[basis]
        acc = info["correct"] / info["total"] * 100 if info["total"] > 0 else 0
        ext = info["extracted"]
        dist = ", ".join(f"{k}={v}" for k, v in sorted(info["got"].items(), key=lambda x: -x[1]))
        print(f"{basis:<15} {info['correct']}/{info['total']:<7} {ext}/{info['total']:<9} {acc:>5.1f}%     {dist}")

    # Confusion-like summary
    print("\n--- Misclassifications ---\n")
    misses = [r for r in results if not r["correct"] and r["extracted_basis"] != "NOT_EXTRACTED"]
    if not misses:
        print("None! Perfect classification on extracted claims.")
    else:
        for r in misses[:20]:  # cap at 20
            print(f"  expected={r['expected_basis']:<12} got={r['extracted_basis']:<12} text={r['sample_text'][:60]}...")

    # Save detailed results
    results_path = os.path.join(SCRIPT_DIR, "results.json")
    with open(results_path, "w") as f:
        json.dump(results, f, indent=2)
    print(f"\nDetailed results saved to {results_path}")


def main():
    if not os.path.exists(SAMPLES_FILE):
        print(f"Samples file not found. Run fetch_samples.py first.")
        sys.exit(1)

    if not os.path.exists(BINARY):
        print(f"Slimemold binary not found at {BINARY}. Run: go build -o slimemold .")
        sys.exit(1)

    with open(SAMPLES_FILE) as f:
        samples = json.load(f)

    print(f"Loaded {len(samples)} samples")

    # Build synthetic transcript
    transcript = build_transcript(samples)
    with tempfile.NamedTemporaryFile(mode="w", suffix=".jsonl", delete=False) as f:
        f.write(transcript)
        transcript_path = f.name
    print(f"Built transcript at {transcript_path} ({len(transcript)} bytes)")

    # Reset benchmark project
    print("Resetting benchmark project...")
    subprocess.run(
        [BINARY, "-p", PROJECT, "reset"],
        capture_output=True, cwd=REPO_ROOT,
    )

    # Run extraction (with retries for overloaded API)
    import time
    response = {}
    for attempt in range(3):
        print(f"Running extraction (attempt {attempt + 1}/3, may take 1-2 minutes)...")
        response = extract_claims(transcript_path)
        if response:
            text = response.get("result", {}).get("content", [{}])[0].get("text", "")
            is_error = response.get("result", {}).get("isError", False)
            if is_error and "overloaded" in text.lower():
                print("API overloaded, waiting 30s before retry...")
                time.sleep(30)
                continue
        break

    if response:
        text = response.get("result", {}).get("content", [{}])[0].get("text", "")
        is_error = response.get("result", {}).get("isError", False)
        if is_error:
            print(f"Extraction error: {text}")
            sys.exit(1)
        print(f"Extraction response: {text[:200]}...")
    else:
        print("No response from extraction")

    # Read claims from DB
    claims = get_claims_from_db()
    print(f"Found {len(claims)} claims in database")

    if not claims:
        print("No claims extracted. Check API key and model availability.")
        sys.exit(1)

    # Match and score
    results = match_claims_to_samples(claims, samples)
    print_report(results)

    # Cleanup
    os.unlink(transcript_path)


if __name__ == "__main__":
    main()
