# Slimemold — Reasoning Topology Mapper

## What This Is

An MCP server + hook system that maps the topology of reasoning during AI agent
conversations. Extracts claims from transcripts, builds a persistent graph, runs
structural vulnerability analysis, and surfaces findings mechanically via Stop hooks.

Designed against Claude Code first, but the threat model is generic: any agent
harness with MCP support and a Stop/UserPromptSubmit-equivalent hook can install
slimemold. People use coding agents for everything — debugging, decisions,
journaling, philosophical work, emotional support — and the failure modes
slimemold targets (sycophancy, load-bearing vibes, sentience drift) are
agnostic to whether the surface task is "fix this build" or "help me think
through this." The reasoning-topology layer doesn't care about the domain.

## Architecture

- **Go binary** — `./slimemold mcp` starts the MCP server on stdio
- **SQLite** — persistent graph at `~/.slimemold/{project}/graph.sqlite`
- **Claude Sonnet 4.6** — extracts claims from transcript chunks via structured output
- **Stop hook** — fires every Nth turn (`SLIMEMOLD_INTERVAL`, default 3), calls parse_transcript, injects audit findings

## Two Analysis Patterns

- **Hooks** (live conversation): System observes the agent via `UserPromptSubmit`.
  Agent can't control timing or opt out. This is load-bearing for the threat model.
- **`analyze_kb` MCP action** (batch/offline): External callers analyze a knowledge
  graph. For CI pipelines, cross-project comparison, winze KB exports — not for the
  agent to self-analyze during the conversation hooks are already observing.

## Design Principle

Slimemold findings are injected into the conversation as system messages that the
**model** reads — not the user. The purpose is to nudge the model to correct the
mutual trajectory, not to dunk on the user or announce that a tool found problems.

When findings appear, push back the way a good collaborator would — friendly,
helpful, specific. The model is already trained to be helpful; the tool just gives
it structural reasons to redirect rather than agree. "That's an interesting claim —
do you have a source for it?" is better than "WARNING: load-bearing vibes detected."
The model receives the structural diagnosis; the user receives better conversation.

## Behavioral contract

The per-finding-type response guidance and tool usage reference live in the MCP
server's instructions (`internal/mcp/instructions.go`), which Claude Code loads
at session start from the MCP server registration. `slimemold init` registers
that globally in `~/.claude/settings.json` so it applies in every project on the
machine. That is the single source of truth for how the model should respond to
hook findings — this file no longer duplicates it.

If you need to read the contract directly (e.g. when editing the Go prose),
look at the `serverInstructions` constant.

## Development

```bash
go build -o slimemold .
go test ./...
./slimemold viz          # ASCII topology
./slimemold audit        # text audit summary
./slimemold calibrate    # per-session Moore et al. 2026 inventory-flag rates + saturation threshold sweep

# Online extractor accuracy check (skipped by default; costs ~$0.05 per run):
ANTHROPIC_API_KEY=... SLIMEMOLD_INVENTORY_ONLINE=1 go test -tags=online \
  ./internal/analysis/ -run TestInventoryOnlineAccuracy -v
```

## Extraction-prompt change discipline

When editing extraction prompts (`internal/extract/prompt.go`,
`documentPromptVersion` in `internal/mcp/ingest.go`), run the variance
harness before merging so the change can be evaluated against the
measured noise floor instead of n=1 vibes:

```bash
ANTHROPIC_API_KEY=... go run benchmarks/variance/run.go -runs 3
```

Compare per-metric deltas against the floor in
`benchmarks/variance/README.md`. A metric is *real signal* when the
delta exceeds ~2σ for that metric; otherwise it's within noise.

- Routine mode: `-runs 3` (~$1.50, ~10-15 min) — sufficient pre-merge check
- Gold standard: `-runs 5` (~$2.50, ~15-25 min) — annual / pre-major-release / when updating the floor itself

The floor lives in `benchmarks/variance/README.md` and gets updated when
a new prompt version meaningfully shifts it. Don't bury extraction-
prompt changes that move metrics beyond noise without saying so in
the commit message.

## Extraction model & hook cost (measured 2026-06-10)

Recurring question: "slimemold is my most expensive API project — switch the
extractor to Haiku?" Answer: **no for the live hook, and the real lever is fire
cadence, not the model.** This was measured, not guessed — re-run before
re-deciding.

**Rebench (Haiku vs Sonnet extraction, README fixture, same Haiku grader):**

| | Sonnet 4.6 | Haiku 4.5 |
|---|---|---|
| Substantive rate (claim quality) | 0.51 | 0.51 — **tie** |
| Total edges (README) | 489 | 167 — **~⅓** |
| Edges per claim | 1.82 | 0.60 |
| README chunks with 0 edges | 0 | 3 |

Claim *recall/quality* is a tie; **topology is a blowout.** Haiku gives ~⅓ the
edges and zeroes out whole chunks. Slimemold's detectors (load-bearing vibes,
amplification cascades, hubs) all run on edges — the graph structure *is* the
product — so Haiku guts the live hook's value for a 3× price cut (Sonnet
$3/$15, Haiku $1/$5 per Mtok — not the ~10× an older mental model assumes).

**Where the cost actually is:** the live Stop hook firing every N turns, on
Sonnet, in *every* project you work in. Verified empirically — the big graphs
(cupel, lexicon, lucida, …) are all `doc_cl = 0` (no document ingestion; they're
live-hook graphs from long sessions) and all carry `audits ≈ hook_fires`, i.e.
the edge-consuming detectors run on every one. There is **no batch-ingestion
bucket** to safely downgrade, and per-fire cost is graph-size-independent
(`selectRelevantClaims` caps injected context at 100 claims). Eval harnesses are
the smallest bucket.

**The lever — `SLIMEMOLD_INTERVAL` (default 3; set to 5 globally 2026-06-10).**
Raising it cuts the *repeated* per-fire overhead — mainly the uncached ~100-claim
context slab in the user prompt — not the extraction work itself (claims/output
over a session are ~constant regardless of chunking). At interval 5 the
live-hook bill drops ~30–45% with zero quality loss; only tradeoff is findings
surface every 5 turns instead of 3. Levers deliberately *not* pulled: shrinking
the 100-claim context cap (it feeds cross-batch edge resolution — the exact
thing the rebench says to protect) and content-tiering to Haiku (no ingestion
bucket exists).

**Re-run the rebench** (~$1 for both, grader fixed at Haiku so only extraction
varies):

```bash
go build -o /tmp/sm_quality ./cmd/quality
/tmp/sm_quality -extract-model claude-sonnet-4-6          # baseline
/tmp/sm_quality -extract-model claude-haiku-4-5-20251001  # candidate
# compare substantive-rate AND per-chunk "N claims, M edges" lines — edges are the tell
```

## Dependencies

- mark3labs/mcp-go — MCP server
- modernc.org/sqlite — SQLite (pure Go, no CGO)
- anthropics/anthropic-sdk-go — Sonnet extraction (default). `SLIMEMOLD_MODEL=claude-haiku-4-5-20251001` is cheaper/faster but **halves edge recall** — fine for claim-only use, wrong for the live hook. See "Extraction model & hook cost" below before switching.
- google/uuid — claim IDs
