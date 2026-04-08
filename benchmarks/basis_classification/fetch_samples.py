#!/usr/bin/env python3
"""
Fetch sample passages from known-provenance datasets for basis classification benchmarking.

Each dataset has a known expected basis:
- arXiv abstracts → research
- Proof-Pile (math proofs) → deduction
- PersonaBank (personal narratives) → empirical
- DetectRL (LLM-generated text) → llm_output
- OpenDebateEvidence (unsourced analytical extensions) → vibes

Outputs a JSON file of labeled passages ready for synthetic transcript construction.
"""

import json
import os
import sys
import random

SAMPLES_PER_CATEGORY = 7  # Must fit within 50-message extraction window (7 categories × 7 = 49)
OUTPUT_FILE = os.path.join(os.path.dirname(__file__), "samples.json")

random.seed(42)


def generate_research_with_citations(n: int) -> list[dict]:
    """
    Generate claims that explicitly cite sources. Expected basis: research.
    These contain inline "(Author Year)" citations — the signal that should
    trigger research classification.
    """
    print(f"  Generating {n} research claims with citations...")
    templates = [
        "The spacing effect is one of the most robust findings in memory research (Ebbinghaus 1885; Cepeda et al. 2006).",
        "Processing fluency influences truth judgments independently of actual evidence (Winkielman & Schwarz 2001).",
        "The Eureka heuristic shows that insight feelings function as metacognitive stop signals (Laukkonen et al. 2021).",
        "Information foraging theory predicts over-exploitation of easy information patches (Pirolli & Card 1999).",
        "Desirable difficulties improve retention by disrupting fluency (Bjork 1994; Bjork & Bjork 2011).",
        "Cognitive foraging follows the same explore-exploit tradeoffs as animal foraging (Hills, Todd & Goldstone 2008).",
        "Active recall is more effective than re-reading for long-term retention (Roediger & Karpicke 2006).",
        "The feeling of rightness substitutes for actual verification in dual-process reasoning (Thompson 2011).",
        "Extrinsic motivation can crowd out intrinsic motivation over time (Deci & Ryan 1985).",
        "The free energy principle provides a unified framework for brain function (Friston 2010).",
        "Schema activation helps learning 58% of the time but actively interferes 17% of the time (Chi 2009).",
        "Multimedia learning is more effective when combining narration with graphics (Mayer 2001).",
        "Betweenness centrality identifies structurally critical nodes in networks (Freeman 1977).",
        "The seductive details effect shows that interesting but irrelevant information harms retention (Harp & Mayer 1998).",
        "Calibration training improves the accuracy of subjective probability judgments (Fischhoff 1982).",
        "Transfer-appropriate processing predicts that encoding conditions should match retrieval conditions (Morris et al. 1977).",
        "The testing effect demonstrates that retrieval practice enhances memory more than restudying (Roediger & Butler 2011).",
        "Confirmation bias is amplified when prior beliefs feel highly precise (Clark 2013; Friston 2010).",
        "SRS scheduling efficiency is real but no RCTs show better learning outcomes from one algorithm vs another (Cepeda et al. 2008).",
        "Gamification of learning activity rather than understanding leads to plateau effects (Duolingo internal data; Zicherman 2024).",
        "The protege effect shows modest learning gains (g=0.35-0.56) when teaching others (Roscoe & Chi 2007).",
        "Adaptive learning systems show g=0.70 effect sizes that collapse against good non-adaptive instruction (Knewton data).",
        "Unguarded GPT-4 access caused a 17% drop in subsequent exam performance (Bastani et al. 2024, PNAS).",
        "Union-find with path compression achieves near-constant amortized time per operation (Tarjan 1975).",
        "Betweenness centrality can be approximated in O(VE) time using BFS from each source (Brandes 2001).",
        "The dual-process model distinguishes Type 1 (fast, intuitive) from Type 2 (slow, deliberative) reasoning (Kahneman 2011).",
        "Spaced practice with expanding intervals is more effective than fixed intervals (Landauer & Bjork 1978).",
        "The generation effect shows that self-generated information is better remembered than passively received information (Slamecka & Graf 1978).",
        "Collaborative inhibition reduces group recall compared to pooled individual recall (Weldon & Bellinger 1997).",
        "The Dunning-Kruger effect shows that low performers overestimate their abilities relative to peers (Kruger & Dunning 1999).",
    ]
    samples = []
    for i in range(min(n, len(templates))):
        samples.append({
            "text": templates[i],
            "expected_basis": "research",
            "source_dataset": "synthetic-research-with-citations",
            "speaker": "assistant",
        })
    return samples


def fetch_arxiv_abstracts(n: int) -> list[dict]:
    """
    Fetch arXiv paper abstracts (CC BY).

    Important: abstracts describe research findings but don't contain inline
    citations (Author Year). In a conversation transcript, "We investigate X
    and find Y" without citing a specific paper is llm_output, not research.
    The abstract IS the research, but slimemold classifies based on evidence
    visible in the conversation text, not the origin of the text.

    Expected basis: llm_output (assistant stating scientific findings without citation).
    """
    from datasets import load_dataset
    print(f"  Fetching {n} arXiv abstracts...")
    ds = load_dataset("gfissore/arxiv-abstracts-2021", split="train", streaming=True)
    samples = []
    for i, row in enumerate(ds):
        abstract = row.get("abstract", "").strip()
        if len(abstract) > 200 and len(abstract) < 2000:
            # Extract a claim-like sentence from the abstract
            sentences = [s.strip() for s in abstract.split(". ") if len(s.strip()) > 40]
            if sentences:
                # Take 1-2 sentences that sound like claims
                claim = ". ".join(sentences[:2]) + "."
                samples.append({
                    "text": claim,
                    "expected_basis": "llm_output",
                    "source_dataset": "arxiv-abstracts-2021",
                    "speaker": "assistant",
                })
        if len(samples) >= n:
            break
        if i > n * 20:  # safety valve
            break
    return samples[:n]


def generate_deduction(n: int) -> list[dict]:
    """
    Generate deductive reasoning passages. Expected basis: deduction.
    Proof-pile requires legacy HF scripts. Instead, construct passages
    that are structurally deductive — logical steps from premises to conclusions.
    """
    print(f"  Generating {n} deductive reasoning passages...")
    templates = [
        "If {premise1}, and {premise2}, then it follows that {conclusion}.",
        "Since {premise1}, we can conclude that {conclusion}. This holds because {premise2}.",
        "Given that {premise1}, and assuming {premise2}, the logical consequence is {conclusion}.",
        "By definition, {premise1}. Combined with the fact that {premise2}, this entails {conclusion}.",
        "Suppose {premise1}. Then {premise2} implies {conclusion}. Therefore the original claim holds.",
        "{premise1}. Moreover, {premise2}. Combining these two observations, we conclude that {conclusion}.",
        "From {premise1} it follows directly that {conclusion}, since {premise2} establishes the necessary condition.",
        "The claim follows by contradiction: if {conclusion} were false, then {premise1} would imply {premise2}, which is impossible.",
        "We proceed by cases. If {premise1}, then {conclusion} follows immediately. If {premise2}, the same conclusion holds by symmetry.",
        "Note that {premise1}. Applying the same reasoning to {premise2}, we derive {conclusion} as a corollary.",
    ]
    premises1 = [
        "every continuous function on a closed interval is bounded",
        "the graph is connected and has no cycles",
        "the sequence is monotonically increasing and bounded above",
        "all edges have positive weight",
        "the input satisfies the precondition",
        "the hash function distributes uniformly",
        "the matrix is symmetric and positive definite",
        "memory allocation succeeds in constant time",
        "the types are well-formed under the given context",
        "the probability distribution has finite variance",
    ]
    premises2 = [
        "the domain is compact",
        "it must be a tree by definition",
        "it must converge by the monotone convergence theorem",
        "the shortest path algorithm terminates",
        "the loop invariant is preserved at each iteration",
        "collisions occur with negligible probability",
        "its eigenvalues are all positive",
        "the allocator maintains a free list",
        "type inference is decidable in this system",
        "the central limit theorem applies",
    ]
    conclusions = [
        "the function attains its maximum and minimum values",
        "there exists exactly one path between any two vertices",
        "the limit exists and equals the supremum of the sequence",
        "Dijkstra's algorithm produces the optimal solution",
        "the postcondition is established upon termination",
        "the expected lookup time is O(1)",
        "the Cholesky decomposition exists and is unique",
        "allocation and deallocation are both O(1) amortized",
        "every well-typed program terminates",
        "the sample mean converges to the population mean",
    ]

    samples = []
    for i in range(n):
        template = templates[i % len(templates)]
        text = template.format(
            premise1=premises1[i % len(premises1)],
            premise2=premises2[i % len(premises2)],
            conclusion=conclusions[i % len(conclusions)],
        )
        samples.append({
            "text": text,
            "expected_basis": "deduction",
            "source_dataset": "synthetic-deduction",
            "speaker": "assistant",
        })
    return samples


def fetch_wikipedia_vibes(n: int) -> list[dict]:
    """
    Fetch statements from the Wikipedia Citation Reason Dataset that humans
    flagged as "opinion" or "controversial" — unsourced assertions that need
    citations. CC BY-SA via Wikimedia/figshare.

    These are vibes by definition: claims that humans judged as needing evidence.
    """
    import csv
    print(f"  Fetching {n} Wikipedia 'citation needed' vibes passages...")

    csv_path = os.path.join(os.path.dirname(__file__), "citation_reason.csv")
    if not os.path.exists(csv_path):
        # Download from figshare
        import urllib.request
        print("    Downloading citation_reason.csv from figshare...")
        urllib.request.urlretrieve(
            "https://ndownloader.figshare.com/files/14441312", csv_path
        )

    # Category codes: 4=opinion, 3=controversial
    vibes_codes = {3, 4}
    samples = []

    with open(csv_path) as f:
        reader = csv.DictReader(f, delimiter="\t")
        candidates = []
        for row in reader:
            try:
                votes = [int(row["vote1"]), int(row["vote2"]), int(row["vote3"])]
            except (ValueError, KeyError):
                continue
            majority = max(set(votes), key=votes.count)
            if majority not in vibes_codes:
                continue
            text = row.get("statement", "").strip()
            if len(text) > 40 and len(text) < 500:
                candidates.append(text)

    random.shuffle(candidates)
    for text in candidates[:n]:
        samples.append({
            "text": text,
            "expected_basis": "vibes",
            "source_dataset": "wikipedia-citation-needed",
            "speaker": "user",
        })
    return samples


def fetch_wikipedia_unsourced_science(n: int) -> list[dict]:
    """
    Fetch statements from the Citation Reason Dataset that humans flagged as
    "scientific" (code 6) — claims about science that lack citations.

    These SOUND like research but aren't sourced. The extractor should classify
    them as llm_output (if said by assistant) or vibes (if said by user), NOT
    as research. Expected basis: llm_output.
    """
    import csv
    print(f"  Fetching {n} Wikipedia unsourced-science passages...")

    csv_path = os.path.join(os.path.dirname(__file__), "citation_reason.csv")

    samples = []
    with open(csv_path) as f:
        reader = csv.DictReader(f, delimiter="\t")
        candidates = []
        for row in reader:
            try:
                votes = [int(row["vote1"]), int(row["vote2"]), int(row["vote3"])]
            except (ValueError, KeyError):
                continue
            majority = max(set(votes), key=votes.count)
            if majority != 6:  # scientific
                continue
            text = row.get("statement", "").strip()
            if len(text) > 40 and len(text) < 500:
                candidates.append(text)

    random.shuffle(candidates)
    for text in candidates[:n]:
        samples.append({
            "text": text,
            "expected_basis": "llm_output",  # sounds scientific but has no citation
            "source_dataset": "wikipedia-unsourced-science",
            "speaker": "assistant",
        })
    return samples


def generate_llm_output(n: int) -> list[dict]:
    """
    Generate clearly LLM-style passages. Expected basis: llm_output.
    Instead of downloading DetectRL (large), we construct passages that
    are structurally LLM-like: confident, well-structured, no citations.
    """
    print(f"  Generating {n} LLM-style passages...")
    # These are hand-written to mimic confident LLM assertions without sources
    templates = [
        "The key insight here is that {topic} fundamentally changes how we think about {domain}. When you consider the implications, it becomes clear that traditional approaches are insufficient.",
        "There are several important factors to consider when evaluating {topic}. First, the underlying mechanism involves {domain}, which has been shown to be highly effective in practice.",
        "The relationship between {topic} and {domain} is more nuanced than it might initially appear. In practice, most implementations rely on a combination of approaches.",
        "It's worth noting that {topic} represents a significant advancement in {domain}. The primary benefit is improved efficiency, though there are important trade-offs to consider.",
        "Based on the available evidence, {topic} appears to be the most promising approach for {domain}. The main advantage is scalability, but implementation complexity remains a challenge.",
        "The consensus in the field is that {topic} will eventually replace traditional methods in {domain}. However, the transition period is likely to be longer than most analysts predict.",
        "When we look at the broader landscape, {topic} stands out as particularly relevant to {domain}. The underlying principles are well-understood, even if the practical applications are still evolving.",
        "A common misconception about {topic} is that it only applies to {domain}. In reality, the core mechanism is domain-independent and can be adapted to many different contexts.",
        "The most effective strategy for implementing {topic} in {domain} involves starting with a minimal viable approach and iterating based on feedback. This reduces risk while maintaining momentum.",
        "Historical precedent suggests that {topic} follows a predictable adoption curve in {domain}. Early adopters see outsized benefits, while laggards face increasing competitive pressure.",
    ]
    topics = ["attention mechanisms", "distributed systems", "knowledge graphs",
              "prompt engineering", "fine-tuning", "retrieval augmentation",
              "agent architectures", "function calling", "embedding models",
              "chain-of-thought reasoning", "synthetic data generation",
              "model distillation", "reinforcement learning from feedback",
              "constitutional AI", "mechanistic interpretability"]
    domains = ["natural language processing", "software engineering",
               "scientific discovery", "education technology",
               "healthcare", "financial analysis", "content moderation",
               "code generation", "decision support systems",
               "autonomous systems"]

    samples = []
    for i in range(n):
        template = templates[i % len(templates)]
        topic = random.choice(topics)
        domain = random.choice(domains)
        text = template.format(topic=topic, domain=domain)
        samples.append({
            "text": text,
            "expected_basis": "llm_output",
            "source_dataset": "synthetic-llm-style",
            "speaker": "assistant",
        })
    return samples


def generate_empirical(n: int) -> list[dict]:
    """
    Generate first-person empirical observation passages.
    Expected basis: empirical.
    PersonaBank is small and hard to download programmatically,
    so we construct passages that are structurally empirical.
    """
    print(f"  Generating {n} empirical observation passages...")
    templates = [
        "I tried {action} and noticed that {observation}. It happened consistently across three separate attempts.",
        "When I {action}, the result was {observation}. I wasn't expecting that — it contradicts what the documentation says.",
        "In my experience working with {domain}, {observation}. I've seen this pattern repeatedly over the past year.",
        "I ran the experiment with {action} and measured {observation}. The numbers were surprisingly consistent.",
        "After switching to {action}, I observed {observation}. The change was immediate and reproducible.",
        "We deployed {action} in production last month and found that {observation}. The team confirmed the same pattern independently.",
        "I tested this by {action} and the outcome was {observation}. Repeated the test five times with the same result each time.",
        "During the debugging session, I discovered that {action} causes {observation}. You can reproduce it by following the same steps.",
        "My direct experience with {domain} showed that {observation}. This was on real data, not synthetic benchmarks.",
        "I measured the actual latency when {action} and found {observation}. The profiler confirmed it wasn't an anomaly.",
    ]
    actions = ["increasing the batch size to 128", "switching from WAL to DELETE journal mode",
               "running the extraction with temperature 0", "disabling the cache layer",
               "using streaming instead of batch processing", "migrating to the new API version",
               "adding an index on the session_id column", "reducing the context window to 4k tokens"]
    observations = ["latency dropped by 40%", "memory usage spiked during the first minute then stabilized",
                    "the error rate went from 2% to nearly zero", "throughput actually decreased slightly",
                    "the output quality was noticeably better", "cold starts became significantly faster",
                    "the model started producing more diverse outputs", "consistency improved across runs"]
    domains = ["SQLite performance tuning", "LLM inference optimization",
               "distributed caching systems", "real-time data pipelines"]

    samples = []
    for i in range(n):
        template = templates[i % len(templates)]
        text = template.format(
            action=random.choice(actions),
            observation=random.choice(observations),
            domain=random.choice(domains),
        )
        samples.append({
            "text": text,
            "expected_basis": "empirical",
            "source_dataset": "synthetic-empirical",
            "speaker": "user",
        })
    return samples


def main():
    print("Fetching benchmark samples...")
    all_samples = []

    # Real datasets
    all_samples.extend(generate_research_with_citations(SAMPLES_PER_CATEGORY))
    all_samples.extend(fetch_arxiv_abstracts(SAMPLES_PER_CATEGORY))
    all_samples.extend(generate_deduction(SAMPLES_PER_CATEGORY))
    all_samples.extend(fetch_wikipedia_vibes(SAMPLES_PER_CATEGORY))

    # Wikipedia "scientific" claims that lack citations — should NOT be classified as research
    all_samples.extend(fetch_wikipedia_unsourced_science(SAMPLES_PER_CATEGORY))

    # Synthetic (structurally distinct, no licensing issues)
    all_samples.extend(generate_llm_output(SAMPLES_PER_CATEGORY))
    all_samples.extend(generate_empirical(SAMPLES_PER_CATEGORY))

    random.shuffle(all_samples)

    with open(OUTPUT_FILE, "w") as f:
        json.dump(all_samples, f, indent=2)

    # Summary
    counts = {}
    for s in all_samples:
        b = s["expected_basis"]
        counts[b] = counts.get(b, 0) + 1
    print(f"\nSaved {len(all_samples)} samples to {OUTPUT_FILE}")
    for basis, count in sorted(counts.items()):
        print(f"  {basis}: {count}")


if __name__ == "__main__":
    main()
