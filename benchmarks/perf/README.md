# Stop hook performance notes

Measured 2026-06-01 against four real on-disk graphs (sizes range from 183 to
13,996 claims). Probe harness lives at `cmd/perfprobe/`. Run as
`./perfprobe PROJECT` — uses the actual graph for that project under
`~/.slimemold/<project>/graph.sqlite`.

## What the Stop hook actually pays for

Per-fire cost decomposes into three layers:

1. **Process startup + config** (~10ms) — `config.Load` reads `.env`, JSON-decodes
   stdin, opens hook.log. Cheap; not worth optimizing.
2. **Local database + analysis work** (~150ms-2s depending on graph size)
   — see breakdown below.
3. **LLM API call** (~30-150s) — Sonnet 4.6 generates up to 16K tokens of
   structured JSON. Dominates wall time; the local work above is a rounding
   error against this.

Reducing local work has limited end-user impact while the LLM call is the
bottleneck. The real value of local-side optimization is **CPU on the user's
machine, GC pressure, and lock-hold time** — not perceived latency.

## Pre-fix scaling bug (resolved 2026-06-01)

`CoreParseTranscript` was loading the full claim/edge set **four times** per
fire (pre-transaction, inside pruneHighDegreeEdges, inside validateIntegrity,
post-commit for analysis). On the 13,996-claim lucida graph, that was ~1.9s
of redundant local work per fire, plus 2× full-file transcript scans for
`CountTranscriptTurns`.

Fixed by load-once-share-many in CoreParseTranscript:

- Single pre-tx load for context selection
- Inside-tx prune keeps one fresh load (needs post-Phase-2 edges)
- Single post-commit-and-CloseSuperseded load shared between integrity check,
  analysis, and counts
- `len(claims)`/`len(edges)` instead of separate `CountClaims`/`CountEdges`
  SQL roundtrips (CountEdges was 180ms on lucida — JOIN + DISTINCT)
- Single transcript chunk read: same string feeds the LLM, n-gram context
  selection, and basis validation. Was 3× full-file scans per fire.

Empirical per-call cost on lucida (warm cache, 14K claims):

| Call | Cost |
|---|---:|
| `GetClaimsByProject` | ~130-180ms |
| `GetEdgesByProject` | ~190ms (JOIN + DISTINCT on edges.from_id/to_id) |
| `CountEdges` | ~110ms (same JOIN as GetEdgesByProject) |
| `analysis.Analyze` | ~150ms (warm), ~300ms (cold) |
| `RecentHookFireTimes` | <1ms |
| `FormatHookFindings` | ~2ms |

## Per-detector breakdown (post-fix)

Wall-clock cost of `analysis.Analyze` is **non-monotonic with graph size**.
Per-detector data from `AnalyzeWithProfile`:

| Detector | schorl (183c) | cupel (3669c) | lexicon (7040c) | lucida (13996c) |
|---|---:|---:|---:|---:|
| **findUnchallengedChains** | 0.2ms | 154ms | 189ms | 39ms |
| findBottlenecks | 4.8ms | 50ms | 1.9ms | 10ms |
| buildTopology | 0.4ms | 25ms | 31ms | 40ms |
| Total | 6ms | 247ms | 255ms | 146ms |

### Key observation: cost tracks topology, not size

`findUnchallengedChains` is **slower on lexicon (7K claims) than on lucida
(14K claims)** — 189ms vs 39ms. Cost is driven by **dependency chain
density**, not claim count. Dense moderate-depth chains explode the DFS path
enumeration; graphs of disconnected `llm_output` utterances (lucida shape)
are structurally easier.

`findBottlenecks` shows the same pattern: 50ms on cupel, 1.9ms on lexicon —
cost depends on recent-subgraph connectedness, not claim count.

**Practical implication:** detector-level optimization is bounded surgery,
not a general scaling fix. If we hit a real bottleneck in the future, instrument
the specific graph shape with `AnalyzeWithProfile` before optimizing — the
right detector to fix depends on the user's graph topology, not just size.

## Why lucida has 14K claims

Not a bug. Lucida is the heaviest-used project: 36 days × ~389 claims/day.
Dedup works (99.97% unique 100-char prefixes). 98% of claims are
weak-basis (`llm_output` + `vibes`), which is consistent with "tracking a
long-running build/debug stream" — most assistant utterances are conversational
glue, not load-bearing reasoning.

Note: dedup operates on character n-gram Jaccard with a 0.5 threshold, same
speaker. Effective for paraphrase but not for genuinely-different claims about
the same topic.

## Stale-binary trap

The hook binary is registered globally at the absolute path
`/home/gas6amus/Documents/slimemold/slimemold`. Whenever this binary is rebuilt
the new code takes effect on the next Stop fire — but if you only build
`./cmd/perfprobe` or other subpackages, the main binary stays old. **Always
`go build -o slimemold .` after substantive changes to extraction, hook, or
analysis code.** This trap caught us once (binary was 30 days stale across
~14 commits, so the production hook was running pre-fix code).

## Archival sweep (shipped 2026-06-01)

Stale claims are now soft-archived to reduce the working set Analyze operates
over. The sweep runs automatically at the end of every successful
`CoreParseTranscript` call, debounced to once per day per project via the
`slimemold_meta` table.

**Criteria for archival** (in `internal/store/sweep.go`):
- Claim is at least 30 days old (`created_at < now - 30d`)
- AND no recent reference (`last_referenced_at < now - 30d`)
- AND one of:
  - `closed = true` (model explicitly resolved the claim)
  - `< 2` incoming structural edges AND weak basis (vibes / llm_output / assumption)

Strong-basis claims (research / empirical / definition / convention) are
never archived solely for low dep count — they earned their spot by being
grounded, not by being popular.

**Soft, not hard:** archive sets a flag; rows stay in the DB. Recover with
`slimemold [--project NAME] unarchive --all` or `unarchive CLAIM_ID...`.

**Disable:** set `SLIMEMOLD_AUTO_SWEEP=off` in the environment that runs
the Stop hook.

**First-pass empirical result on lucida** (14K claims): 5,155 candidates
(36.7% of active), 110 closed + 5,045 weak-basis+no-deps. After the sweep
lands in production, GetClaimsByProject returns ~62% of the previous size,
proportionally reducing Analyze cost on every subsequent fire.

The `legacy_load_bearer` detector handles the inverse case: an old claim
that IS being touched currently gets surfaced as a finding rather than
archived. See `internal/analysis/legacy.go`.

### Audit history gotcha

`CreateAudit` records claim/edge counts post-sweep. Pre-deploy audit rows
have raw counts (pre-archive); post-deploy rows have working-set counts
(post-archive). Any chart of `audits.claim_count` over time will show a
phantom dip on the deploy date for projects with stale-claim backlog —
that's the sweep draining the queue, not a real loss of data. If you want
the true total, query with `claims WHERE project=?` instead of trusting
the audit count column.

### First-sweep cap

The first auto-sweep on a heavy project (lucida-class — thousands of stale
candidates) would archive thousands of claims in one fire. To bound blast
radius, the sweep caps archives at `SLIMEMOLD_SWEEP_CAP` (default 1000) per
invocation, archiving the oldest candidates first. A multi-thousand backlog
drains over several days of daily sweeps; the user sees an
`auto-swept N stale claims (cap N hit, more pending)` log line whenever the
cap bites.

## Future levers, not yet pulled

- **Pre-loaded edges into pruneHighDegreeEdges**: requires tracking newly-added
  edge IDs in Phase 2 to build the post-Phase-2 snapshot in memory. Saves
  ~190ms per fire on large graphs. Modest gain; complex plumbing.
- **N-gram index sharing**: `selectRelevantClaims` and `buildNgramIndex` both
  compute n-grams over the full existing-claim set. Could share. Saves
  ~100-300ms per fire on 14K-claim graphs.
- **Cache topology between fires**: most fires add <50 claims, so topology
  changes are tiny. Persisting `buildTopology` output and invalidating only
  when the new-claim count exceeds a threshold would skip 40ms per fire on
  lucida. Adds invalidation complexity.
- **Detector-level fast path**: `findUnchallengedChains` could cap DFS branching
  more aggressively. Worth investigating if mid-size graphs become the user's
  hot case.
- **Soft archive by reference time** (Option A — in progress): adds
  `last_referenced_at` column, sweep stale low-traffic claims to an archived
  flag, surface "old but still touched" as a new detector. Reduces the
  working set for Analyze AND surfaces an interesting signal (old claims
  doing structural work in current sessions).

## Skipped levers

- **Switch to Haiku 4.5**: user tested previously; extraction quality dropped
  below acceptable. Documented here so future-us doesn't re-evaluate
  without checking with user first.
