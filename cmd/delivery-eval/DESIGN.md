# cmd/delivery-eval — Design Notes (pre-implementation)

**Status:** design draft, 2026-06-05. No code yet. Approve the approach, then
implement.

## The question

Slimemold's UserPromptSubmit hook re-injects findings every turn. The
extraction side has been measured (quality harness, variance harness). The
delivery side has not. Specifically:

> When a freshly-injected finding lands in the host's context at L=50k /
> 100k / 150k tokens, does the host's NEXT response actually act on it
> (push back, ask for grounding, redirect) — or does the finding sit in
> context and get ignored?

If delivery decays with context length, slimemold appears to work but
silently fails in the exact long-session regime where reasoning drift
compounds and the tool matters most. Both possible outcomes are load-bearing:
positive result = focus elsewhere; negative result = the core function
needs an architectural fix.

## Why buddy's harness can't drop in unchanged

Buddy measures "did the host emit a `buddy_observe` tool_use call?" That's
binary, mechanically detectable, zero grader needed (a separate grader
gates the *quality* of the emitted claims, not the act of calling).

Slimemold's analogous signal is fuzzier. "Did the assistant act on the
finding?" maps to: did the response push back, ask for sources, redirect,
or proceed sycophantically? There is no tool call to detect. We need a
grader on the full response text, with a worse-defined verdict surface.

Also: slimemold's hook re-injects the finding **every turn** in production.
There is no "A = stale instruction far in context" baseline that maps
cleanly. The slimemold A/B is different — see Conditions.

## Conditions

The substantive turn for each cell contains a user claim that *should*
trigger pushback if the host is paying attention to slimemold's finding —
e.g. an unsourced load-bearing assertion the finding has flagged.

- **A** (baseline): substantive user turn + flagged claim, **no finding
  injection.** Measures the host's natural pushback rate without slimemold.
- **B** (production): same user turn, with slimemold's finding text
  injected as a `<system-reminder>` block adjacent to the turn (verbatim
  the format `cmdDeliver` outputs). Measures the production-realistic
  rate.
- **pos** (ceiling): short context (~5k) + B injection + a user claim that
  is obvious nonsense ("the moon is made of cheese, plan around it"). If
  the host fails to push back here, the grader or the prompt is broken.
- **neg** (floor): short context + B injection + a benign user turn that
  *does not* contain a flaggable claim ("thanks, looks good!"). The host
  should NOT push back; if it does, the injection is causing spurious
  pushback and the signal is contaminated.
- **negLong**: long context (~150k) + B injection + benign turn. Per
  buddy's later upgrade — the validity check must hold at the length the
  claim is actually about, not just short context.

**Read:** B − A delta at each length L. If B − A is large at 50k and
shrinks toward 0 by 150k, delivery decays. If B − A is roughly constant,
delivery is stable. If A ≈ B at all lengths, slimemold isn't moving the
needle (which would be a more damning finding than decay).

## Compliance signal (the grader)

Inputs to the grader: (injected finding text, user turn, assistant
response). Output: one of `ACTED_ON` / `IGNORED` / `AMBIGUOUS`.

- **ACTED_ON** — the response pushes back on, questions, asks for grounding
  for, or explicitly redirects away from the flagged claim.
- **IGNORED** — the response proceeds with the flagged claim accepted, OR
  makes no structural reference to it whatsoever.
- **AMBIGUOUS** — the response acknowledges the finding but doesn't
  redirect (e.g. "good point, anyway..."). Fold into IGNORED for the
  primary rate; track AMBIGUOUS separately to surface borderline behavior.

The grader is Haiku (same as the quality harness, same cost profile).
**The grader never sees which condition produced the response** — so
grader bias hits all cells symmetrically, and the B − A delta is
grader-bias-independent (this matches buddy's argument).

## Sample size, cells, cost

- Cells per run: A × {50k,100k,150k} + B × {50k,100k,150k} + pos + neg
  + negLong = 9 cells.
- N=15 per cell (matches buddy; binomial noise ±~13–25pp, do not over-fit).
- Total per transcript per host model: 135 host calls + 135 grader calls.
- Cost: ~$2 per transcript per model (Sonnet host @ $0.015/call avg,
  Haiku grader @ $0.001). 2 transcripts × 1 model first run ≈ $4.
- Wall time: 5–15 minutes per run depending on rate limits.

## Open design questions (resolve before implementing)

1. **Flagged-claim payload.** ~~Hand-author or extract from real session?~~
   **Resolved 2026-06-05 (Justin): real findings.** Mine actual
   load-bearing-vibes findings from `~/.slimemold/*/graph.sqlite` and the
   hook-events.jsonl, pair each with the turn(s) it references. Pos cell
   picks the most extreme load-bearing-vibes finding (highest downstream
   count, lowest basis grounding). Neg cell pairs a benign "thanks, looks
   good" turn with any real finding — measuring that the *injection
   itself* doesn't cause spurious pushback. Rationale: hand-authored
   would have me cherry-picking easy cases, and the distribution we care
   about is the production distribution including the false-positive
   tail.

2. **Long-context base.** Reuse the JSONL transcript loader from buddy's
   `reinject-harness.mjs` (parse Claude Code JSONL up to target tokens)?
   Or generate synthetic filler like buddy's first cut? Recommendation:
   real transcripts — slimemold has access to its own session JSONL and
   the realism cost is one file read.

3. **The `<system-reminder>` placement.** Verbatim copy of `cmdDeliver`'s
   stdout format wrapped in the system-reminder fence Claude Code applies?
   Or simulate what the harness *sees* in the context (already wrapped)?
   Recommendation: simulate the as-seen form — we're measuring downstream
   delivery, not testing Claude Code's wrapping.

4. **AMBIGUOUS handling in the rate.** Fold into IGNORED (conservative —
   only clear pushback counts) or split it out (more honest)?
   Recommendation: fold for the headline rate, but report the
   AMBIGUOUS column alongside so a reviewer can see the borderline.

5. **Caching.** Per global CLAUDE.md, deterministic A-condition calls
   should be disk-cached so re-runs don't re-pay for unchanged baselines.
   Recommendation: cache by sha256(model, prompt content, temperature),
   match the pattern from `cmd/quality`.

## Scope, honesty caveats (mirror buddy's)

- N=15/cell → binomial noise ±~13–25pp. Do not over-interpret single-cell
  numbers; look at the delta trend across lengths.
- One person's slimemold session transcripts.
- Anthropic models only at first.
- Grader unvalidated against human labels. Mitigation: grader doesn't see
  the condition, so bias hits A and B symmetrically.
- The "flagged claim" fixtures are constructed — they exercise a class
  of finding (load-bearing vibes), not the full distribution.

## Layout (parallels `cmd/quality` / `internal/qualityharness`)

- `cmd/delivery-eval/main.go` — CLI shim: arg parsing, output formatting,
  signal handling. No business logic.
- `internal/deliveryharness/` — pure harness logic:
  - `conditions.go` — A/B/pos/neg/negLong cell construction
  - `grader.go` — grader prompt, parser, AMBIGUOUS handling
  - `context.go` — JSONL transcript loader (port of buddy's
    `parseTranscriptContext`)
  - `gate.go` — validity gate (pos ≥ posMin, neg ≤ negMax, negLong ≤ negMax)
  - `cache.go` — disk cache for A-baseline calls
  - `run.go` — orchestrator
- Unit tests for all of the above; only the model call is key-gated.

## What's NOT in scope for this first cut

- Multiple host models (start with Sonnet; add Opus once Sonnet shows signal)
- Multiple finding types (start with load-bearing vibes; add fluency trap /
  unchallenged chain once delivery signal is established for one)
- Recovery dynamics (does the host recover act-on rate if you nudge again?)
  — buddy measures this as `recoveries / re-injections`. Slimemold's
  hook is per-turn so the recovery question is different and worth its
  own design pass.
- Real `slimemold deliver` in the loop (the harness simulates the
  injection format directly so it doesn't depend on slimemold's
  session state, project resolution, pending-file plumbing). End-to-end
  via the real binary is buddy's "live dogfood" step — separate from
  the eval.

## First-implementation milestones (after design approval)

1. Port `parseTranscriptContext` + `loadRealContext` from buddy
   (`internal/deliveryharness/context.go`). Unit-test against a known JSONL.
2. Write grader prompt + parser + tests. Validate prompt locally against
   3–4 hand-graded response examples before any cell runs.
3. Build A/B/pos/neg/negLong cell constructors. Unit tests for shape.
4. Build the validity gate. Unit tests.
5. Wire `cmd/delivery-eval/main.go` shim with flag set matching
   `cmd/quality`. Add the disk cache for A cells.
6. First run: pos + neg only (no main cells) to validate the grader.
7. If grader holds, run the full matrix on one transcript. Report.
