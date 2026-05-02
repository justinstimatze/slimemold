# Variance harness

Measure the noise floor of slimemold's extraction pipeline.

Runs the same fixture document through extraction `N` times into separate
temp project graphs, captures metrics from each run, and prints mean ±
stddev across runs. The output is the actual sampling-variance floor any
prompt-version comparison should be evaluated against.

## Why this exists

For four prompt versions (v4–v7) the README appendix carried the caveat
"n=1 per version cannot distinguish prompt-fix-effect from sampling
noise." Until a noise floor was on file, every prompt-change comparison
was vibes-tuning. This harness puts the floor on file so future changes
land with measured comparisons rather than hopes.

## Usage

```bash
ANTHROPIC_API_KEY=... go run benchmarks/variance/run.go [flags]
```

Flags:
- `-fixture PATH` — document to ingest (default `README.md`)
- `-runs N` — number of extraction runs (default `5`)
- `-model NAME` — extraction model (default `SLIMEMOLD_MODEL` env, then `claude-sonnet-4-6`)

## Cost / time tradeoffs

Per-run cost depends on fixture size and model. Rough numbers (May 2026):

| regime | runs | fixture | wall-clock | cost | use |
|---|---|---|---|---|---|
| gold standard | 5 | full README | 15-25 min | $2-3 | annual / pre-major-release |
| routine | 3 | full README | 9-15 min | $1.20-1.80 | per extraction-prompt edit |
| sanity | 1 | full README | 3-5 min | $0.40-0.60 | "did I break extraction" check |
| section | 3-5 | single README section | 2-6 min | $0.20-0.60 | targeted prompt iteration |

Routine mode (3 runs) catches anything dramatic; gold standard (5 runs)
gives a defensible noise floor for cross-version claims.

## Measured noise floor (May 2026, prompt v7, README fixture)

Five runs of the slimemold README ingest into fresh temp graphs.

### Counts

| metric | mean | stddev | stddev/mean | regime |
|---|---|---|---|---|
| claims | 275.0 | 9.78 | 3.6% | stable |
| edges | 483.8 | 18.78 | 3.9% | stable |
| max chain depth | 10.8 | 1.94 | 18% | noisy |
| orphans | 8.0 | 1.10 | 14% | noisy |
| critical findings | 247.4 | 16.38 | 6.6% | stable |
| warning findings | 15.2 | 1.94 | 13% | borderline |
| info findings | 50.6 | 8.59 | 17% | noisy |
| load_bearing_vibes | 87.8 | 10.91 | 12% | borderline |
| bottleneck | 5.0 | 0.00 | 0% | **degenerate (capped)** |
| unchallenged_chain | 1.0 | 0.00 | 0% | deterministic-after-extraction |

### Basis distribution

| basis | mean | stddev | stddev/mean |
|---|---|---|---|
| vibes | 192.4 | 10.69 | 5.5% |
| research | 28.2 | 2.04 | 7% |
| definition | 29.2 | 8.13 | 28% |
| analogy | 12.6 | 1.85 | 15% |
| deduction | 9.8 | 1.33 | 14% |
| assumption / empirical / convention | small absolute counts (<3) | — | high relative noise; ignore |

### Interpreting cross-version comparisons

A version-to-version difference counts as **signal** when it exceeds
~2× the noise stddev for that metric. For the headline metrics (claims,
edges, vibes count, research count, critical findings):

- Differences within ~5% of the previous run: noise
- Differences of 10-20%: marginal; need multiple runs both versions
- Differences >20%: real signal, even at n=1

For high-noise metrics (definition, deduction, analogy, info findings):
the floor is much wider; small differences are noise even at large
nominal magnitudes.

## Findings worth flagging

### Bottleneck stability: cap removed, churn confirmed

The original 5-run floor showed `bottleneck = 5.0 ± 0.0`. That was
not evidence of stable detection — it was the hardcoded cap
(`maxBottlenecks = 5`). Two follow-ups landed:

- The cap was removed (`findBottlenecks: remove hardcoded
  maxBottlenecks=5 cap`). Bottleneck count now varies meaningfully
  with graph topology (e.g. v9 README runs: 14, 8, 9, 13, 11).
- The harness was extended to record per-finding claim text and
  compute set intersection across runs (`variance: compare finding
  stability by claim text, not UUIDs`). The stability table is now
  in the harness output.

What that revealed: even after the cap is gone, the claim-identity
intersection across runs is small — bottleneck identities churn
within the same fixture. Count is misleading; identity is the
informative signal. Documented in the harness output's "stable?"
column, which currently reads `churns (count is misleading)` for
bottleneck, load_bearing_vibes, unchallenged_chain, and fluency_trap.

### Definition count is the noisiest basis

28% stddev/mean. The basis classifier is inconsistent on definition
specifically. Cross-version comparisons on definition share are unreliable;
the README appendix's hand-waving about definition swings (10 to 48
across v4-v7) was right that noise dominates for that metric.

**Update (v8 / v9):** two prompt-engineering attempts to reduce this.

| version | change | definition mean | stddev | stddev/mean |
|---|---|---|---|---|
| v7 baseline | — | 29.2 | 8.13 | 28% |
| v8 | added "definition declares meaning, convention declares practice" precision paragraph; added inline examples | 30.0 | 7.72 | 26% |
| v9 (reverted) | swapped convention before definition in decision tree; dropped "we use 'X' to refer to Y" example | 37.0 | 10.39 | 28% |

v8 was approximately neutral. v9 regressed — putting convention first
left the definition rule more permissive without redirecting anything
to convention (convention firings stayed at 1.0 ± 0.0 across all 5
runs, suggesting the README simply has very little stipulative-policy
content). v9 was reverted; the current state (`documentPromptVersion=10`)
is v8's prompt content.

Lesson: the ~28% definition floor is not budging within wording-tweak
range. Reducing it likely requires a structural change — different
model, ensemble extraction across N runs, or a non-prompt rule (e.g.
post-extraction reclassification of definition-vs-vibes claims using
a separate pass). Logged as a known limitation rather than an
open prompt-engineering task.

### Counts vs identities: the analysis layer is stable, extraction isn't

Once the graph exists, the topology analysis is deterministic — the
same graph produces the same finding counts. All variance lives in
the extraction step (basis classification, claim splitting, edge
inference). The graph-analysis layer is reliable; the LLM-extraction
layer is the noise source.

This is why per-finding claim-identity intersection is a more useful
stability signal than count. Two runs producing 11 and 13
bottlenecks tell you very little; two runs whose top bottlenecks
share zero claim-text overlap tell you the extractor isn't
converging on the same structural reading of the document.

## Pre-commit discipline

When editing extraction prompts (`internal/extract/prompt.go`,
`documentPromptVersion` constant in `internal/mcp/ingest.go`):

1. Run `go run benchmarks/variance/run.go -runs 3` (routine mode) before
   merging
2. Compare per-metric deltas against this README's measured floor
3. If a metric moved beyond ~2σ → real signal; document in commit
4. If within ~2σ → caveat as "within noise floor"
5. Never ship "we hope this helped"; the floor exists now

Updates to this floor when a future prompt version changes the floor's
shape: re-run with `-runs 5`, paste new numbers in the table above, note
the version transition.

## Reproduction

The exact run that produced the May 2026 floor:

```bash
go run benchmarks/variance/run.go -runs 5 -fixture README.md \
    -model claude-sonnet-4-6
```

Output is JSONL-able if needed; current implementation prints to stdout.
