#!/usr/bin/env python3
"""
Fetch and prepare DialAM-2024 / QT30 samples for edge quality benchmarking.

Downloads argument-annotated dialogue episodes from the DialAM-2024 shared task,
extracts gold-standard propositional relations (support/conflict between claims),
and converts them into a format slimemold can process.

Data source: http://dialam.arg.tech/ (QT30 corpus — BBC Question Time debates)
"""

import json
import os
import random
import sys
import zipfile
from pathlib import Path

DATASET_URL = "http://dialam.arg.tech/res/files/dataset.zip"
DATASET_ZIP = "/tmp/dialam-dataset.zip"
DATASET_DIR = "/tmp/dialam-dataset/dataset"

BENCHMARK_DIR = Path(__file__).parent
SAMPLES_FILE = BENCHMARK_DIR / "samples.json"

# Selection criteria: medium episodes that fit in slimemold's 50-message window
MIN_I_NODES = 8
MAX_I_NODES = 35
MIN_RA = 2
MIN_CA = 1
MAX_LOCUTIONS = 50
NUM_EPISODES = 5
SEED = 42


def download_dataset():
    """Download and extract the DialAM-2024 training data."""
    if os.path.isdir(DATASET_DIR):
        return

    import urllib.request
    print(f"Downloading {DATASET_URL}...", file=sys.stderr)
    urllib.request.urlretrieve(DATASET_URL, DATASET_ZIP)

    print("Extracting...", file=sys.stderr)
    with zipfile.ZipFile(DATASET_ZIP, "r") as z:
        z.extractall("/tmp/dialam-dataset")


def parse_episode(path):
    """Parse a QT30 nodeset JSON into structured episode data."""
    with open(path) as f:
        data = json.load(f)

    nodes = {n["nodeID"]: n for n in data["nodes"]}

    # Build directed edge lookup
    from_edges = {}  # fromID -> [toID]
    to_edges = {}    # toID -> [fromID]
    for e in data["edges"]:
        from_edges.setdefault(e["fromID"], []).append(e["toID"])
        to_edges.setdefault(e["toID"], []).append(e["fromID"])

    # Extract I-nodes (propositions)
    i_nodes = {n["nodeID"]: n["text"] for n in data["nodes"] if n["type"] == "I"}

    # Extract L-nodes (locutions) in chronological order
    l_nodes = sorted(
        [n for n in data["nodes"] if n["type"] == "L"],
        key=lambda n: n["timestamp"]
    )

    # Extract propositional relations via S-nodes (RA/CA)
    # Pattern: I-node -> S-node -> I-node
    gold_relations = []
    for s_type, rel_type in [("RA", "supports"), ("CA", "contradicts")]:
        s_nodes = [n for n in data["nodes"] if n["type"] == s_type]
        for s in s_nodes:
            sid = s["nodeID"]
            sources = [nid for nid in to_edges.get(sid, []) if nodes.get(nid, {}).get("type") == "I"]
            targets = [nid for nid in from_edges.get(sid, []) if nodes.get(nid, {}).get("type") == "I"]
            for src in sources:
                for tgt in targets:
                    gold_relations.append({
                        "from_id": src,
                        "from_text": i_nodes[src],
                        "to_id": tgt,
                        "to_text": i_nodes[tgt],
                        "relation": rel_type,
                    })

    # Map L-nodes to speakers
    locution_speakers = {}
    for loc in data.get("locutions", []):
        if "personID" in loc:
            locution_speakers[loc["nodeID"]] = loc["personID"]

    return {
        "i_nodes": i_nodes,
        "l_nodes": l_nodes,
        "gold_relations": gold_relations,
        "locution_speakers": locution_speakers,
        "num_ra": len([n for n in data["nodes"] if n["type"] == "RA"]),
        "num_ca": len([n for n in data["nodes"] if n["type"] == "CA"]),
    }


def select_episodes():
    """Select diverse episodes matching size criteria."""
    download_dataset()

    candidates = []
    for fname in os.listdir(DATASET_DIR):
        if not fname.endswith(".json"):
            continue
        path = os.path.join(DATASET_DIR, fname)
        with open(path) as f:
            data = json.load(f)

        nodes = data["nodes"]
        i_count = sum(1 for n in nodes if n["type"] == "I")
        ra_count = sum(1 for n in nodes if n["type"] == "RA")
        ca_count = sum(1 for n in nodes if n["type"] == "CA")
        l_count = sum(1 for n in nodes if n["type"] == "L")

        if (MIN_I_NODES <= i_count <= MAX_I_NODES
                and ra_count >= MIN_RA
                and ca_count >= MIN_CA
                and l_count <= MAX_LOCUTIONS):
            candidates.append({
                "file": fname,
                "path": path,
                "i_nodes": i_count,
                "ra": ra_count,
                "ca": ca_count,
                "l_nodes": l_count,
            })

    random.seed(SEED)
    selected = random.sample(candidates, min(NUM_EPISODES, len(candidates)))
    return selected


def build_transcript(episode_data):
    """Convert QT30 L-nodes into a 2-speaker transcript for slimemold.

    Maps unique speakers to user/assistant alternating by first appearance.
    """
    l_nodes = episode_data["l_nodes"]
    speakers = episode_data["locution_speakers"]

    # Get unique speaker IDs in order of appearance
    seen_speakers = []
    for l in l_nodes:
        sid = speakers.get(l["nodeID"], "unknown")
        if sid not in seen_speakers:
            seen_speakers.append(sid)

    # Map to user/assistant: first speaker = user, rest alternate
    speaker_map = {}
    for i, s in enumerate(seen_speakers):
        speaker_map[s] = "user" if i % 2 == 0 else "assistant"

    messages = []
    for l in l_nodes:
        sid = speakers.get(l["nodeID"], "unknown")
        role = speaker_map.get(sid, "user")
        # Strip speaker prefix from L-node text (format: "Speaker : text" or "Host: Speaker : text")
        text = l["text"]
        if " : " in text:
            text = text.split(" : ", 1)[-1]
        messages.append({"role": role, "text": text.strip()})

    return messages


def main():
    episodes = select_episodes()
    print(f"Selected {len(episodes)} episodes", file=sys.stderr)

    samples = []
    for ep_info in episodes:
        ep = parse_episode(ep_info["path"])
        transcript = build_transcript(ep)

        samples.append({
            "source_file": ep_info["file"],
            "transcript": transcript,
            "gold_claims": ep["i_nodes"],  # {nodeID: text}
            "gold_relations": ep["gold_relations"],
            "stats": {
                "i_nodes": ep_info["i_nodes"],
                "ra": ep_info["ra"],
                "ca": ep_info["ca"],
                "l_nodes": ep_info["l_nodes"],
            },
        })

    with open(SAMPLES_FILE, "w") as f:
        json.dump(samples, f, indent=2)

    total_claims = sum(s["stats"]["i_nodes"] for s in samples)
    total_ra = sum(s["stats"]["ra"] for s in samples)
    total_ca = sum(s["stats"]["ca"] for s in samples)
    print(f"Wrote {SAMPLES_FILE}", file=sys.stderr)
    print(f"  {len(samples)} episodes, {total_claims} gold claims, {total_ra} RA + {total_ca} CA relations", file=sys.stderr)


if __name__ == "__main__":
    main()
