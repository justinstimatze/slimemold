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

---

# Quality harness (`cmd/quality`)

Stability ≠ quality. `run.go` answers "are the extracted COUNTS reproducible
across runs"; it can't tell you whether those counts are mostly substantive
claims or mostly filler. `cmd/quality` measures the latter via a separate
grader model (Haiku) that scores each extracted claim as SUBSTANTIVE or
FILLER, gated by positive/negative control fixtures so a broken grader
can't silently produce a confidently-wrong number.

Adapted from the buddy re-injection eval harness
(`~/Documents/buddy/scripts/reinject-harness.mjs`), which used the same
validity-gate discipline to catch two real bugs in its own pipeline.

## Why this exists

Slimemold's extractor uses a deliberately aggressive-recall prompt: missing
a real claim is worse than emitting a filler claim (because the downstream
graph analysis is blind to absent nodes, while ignoring filler is just
cost). That trade-off is correct *up to a content-dependent threshold* —
beyond which filler dilutes the load-bearing signal and the analysis
layer pays full DFS cost for zero detection value. Without a quality
number you can't tell when the threshold's been crossed; you can only
see the variance floor stay calm while the precision quietly tanks.

The buddy eval measured this same pattern at the host layer and saw it
move ~25pp on one fixture-model cell (sonnet @ 150k tokens, slimemold
transcript). It's content-dependent and not uniform — which means a
single quality number per fixture is the right granularity.

## Architecture

Grading logic lives in `internal/qualityharness/` so `go build ./...`,
`go vet ./...`, and the pre-commit gate catch signature drift against
`internal/extract`, `internal/mcp`, and `internal/store`. The CLI shim
at `cmd/quality/main.go` is a real package (not `//go:build ignore`)
so shim-vs-library drift is ALSO caught. Pure logic (rate computation,
validity gate, rune-safe truncation, schema construction, prompt
assembly) has unit-test coverage in `internal/qualityharness/qualityharness_test.go`.

The grader prompt itself is versioned via `GraderPromptVersion` in the
internal package; the version is stamped into every emitted result so
cross-run comparisons can detect that the grader (not the extractor)
changed between runs. Note: result persistence + cross-run diff tooling
do not yet exist — the version stamp is documented intent, not a
delivered capability. Future work.

## Usage

```bash
ANTHROPIC_API_KEY=... go run ./cmd/quality [flags]
# or
go install ./cmd/quality && ANTHROPIC_API_KEY=... quality [flags]
```

Flags:
- `-fixture PATH` — main fixture to grade (default `README.md`)
- `-pos-fixture PATH` — positive control (default `benchmarks/variance/fixtures/positive_control.md`)
- `-neg-fixture PATH` — negative control (default `benchmarks/variance/fixtures/negative_control.md`)
- `-grader-model NAME` — model for per-claim grading (default `claude-haiku-4-5-20251001`)
- `-extract-model NAME` — extraction model (default `SLIMEMOLD_MODEL` env, then `claude-sonnet-4-6`)
- `-concurrency N` — max concurrent grader calls (default `10`; must be `>= 1`)
- `-pos-min F` — positive-control substantive rate must be ≥ this (default `0.70`)
- `-neg-max F` — negative-control substantive rate must be ≤ this (default `0.30`)
- `-min-gradable N` — min gradable claims per control before the gate can pass (default `10`)
- `-timeout DUR` — total wall-clock cap; aborts on SIGINT/SIGTERM or deadline (default `30m`)
- `-controls-only` — run pos+neg controls and exit (useful when re-tuning controls)

The harness ALWAYS runs the controls before the main fixture and refuses
to interpret the main result if the validity gate fails. The gate
requires each control to produce at least `-min-gradable` gradable
claims — a network outage that collapses the denominator to zero would
otherwise make `SubstantiveRate` return 0 and trivially satisfy
`negRate ≤ 0.30`, reporting VALID on nothing. The verdict is emitted
with a machine-readable `Kind` (one of `VALID`, `POS_SMALL_N`,
`NEG_SMALL_N`, `POS_BELOW_MIN`, `NEG_ABOVE_MAX`) so downstream
automation can route on failure type without string-parsing the prose
reason.

## Calibration: controls are intentionally domain-distant

The control fixtures live in `benchmarks/variance/fixtures/`. They're
domain-distant from slimemold's typical input (essays + emails, not
codebase prose) and deliberately written NOT to pattern-match the
rubric's example structure:

- **Pos** is a personal essay about programmer-to-manager transitions
  — substantive prose with mechanisms and contested opinions, but in
  prose form (no "Decision: ..." section headers, no comparison
  trade-off subheads that map 1:1 onto the rubric's SUBSTANTIVE example
  list).
- **Neg** is an email thread of small-talk acknowledgments — trivial
  content without using the rubric's named FILLER categories
  (no temperature readings, no grocery items, no standup chatter, no
  list-of-numbers items).

This is an attempt to break the circular-calibration trap where
controls are constructed against the same rubric the grader uses —
which would let the gate pass by example-recognition rather than
independent judgment. **Honest disclosure:** the trap is hard to fully
escape since the author writes both the rubric and the controls. The
strongest test of grader independence would be controls drawn from
external sources the author didn't write (a paper section for pos,
a Wikipedia trivia list for neg). The current fixtures are a step in
that direction but not the endpoint.

## Cost / time

| run | wall-clock | cost | use |
|---|---|---|---|
| controls only | 3-5 min | ~$0.50 | sanity-check grader after prompt tweaks |
| full eval (3 fixtures) | 8-15 min | ~$1.50-2.00 | per extraction-prompt edit |

The grader makes one Haiku call per extracted claim. At ~275 claims for
the README fixture and ~$0.0003/call, grading is ~$0.08 per fixture;
three fixtures = ~$0.25 grading + ~$1.50 extraction.

## Interpreting the verdict

The harness exits with one of:

- **Code 0 + substantive rate** — both controls passed, main rate reported.
  Interpretation bands (in the output):
  - ≥0.85: precision-heavy, very little filler
  - 0.70-0.85: healthy precision band
  - 0.50-0.70: filler rate is non-trivial; consider whether the
    aggressive-recall prompt is over-extracting on this content
  - <0.50: filler dominates; extraction is over-recalling
- **Code 2** — validity gate failed (controls invalid). Inspect the
  `Kind` and the printed pos/neg rates. Fix the grader prompt
  (`internal/qualityharness/qualityharness.go` `GraderRubric`, bump
  `GraderPromptVersion`), the control fixtures, or pass `-pos-min` /
  `-neg-max` / `-min-gradable` to retune the gate.
- **Code 1** — pipeline error (extraction or fixture read failed).

## Discipline

This is the documented protocol when editing extraction prompts
(`internal/extract/prompt.go`, `documentPromptVersion` in
`internal/mcp/ingest.go`):

1. Run `./cmd/quality` against the current README fixture BEFORE merging
2. Note the substantive rate
3. Make the prompt change
4. Run `./cmd/quality` again
5. If rate moves ≥0.05 in either direction, document in commit:
   - Up: precision improved (good if recall didn't tank — cross-check with `run.go`)
   - Down: filler increased (note whether it's the targeted trade-off)
6. If rate moves <0.05, the change is below the grader's measurement
   precision — call it out as "within quality noise"

**Honest disclosure:** the discipline is currently enforced by human
memory of this README. There is no pre-commit hook, no CI gate, and no
required-checks rule. The harness `Kind` codes are designed for future
automation but no automation consumes them yet. The package's
import-ability is real (lives in `internal/`, compiled by `go build`)
but the only consumer today is `cmd/quality`.

## Known limitations

- **One grader, one model.** A second-grader vote (e.g., Sonnet judging
  whether Haiku judged correctly) would tighten the calibration but
  triples cost. Currently not warranted — buddy's experience showed
  Haiku is a competent grader for this binary task.
- **Per-claim cost scales with extraction recall.** If the extractor
  emits 1000+ claims (lucida-class graph from a single fixture), grading
  cost approaches $0.30 per fixture. Sample-grading (random N claims per
  fixture instead of all) is the obvious extension if cost becomes the
  binding constraint.
- **Controls are project-specific.** The bundled fixtures are calibrated
  for technical-prose extraction (similar to slimemold's typical input).
  For a different domain (medical notes, legal text), write domain-
  specific controls and re-tune the gate.
