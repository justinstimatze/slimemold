# Slimemold — Reasoning Topology Mapper

## What This Is

An MCP server + hook system that maps the topology of reasoning during Claude Code
conversations. Extracts claims from transcripts, builds a persistent graph, runs
structural vulnerability analysis, and surfaces findings mechanically via Stop hooks.

## Architecture

- **Go binary** — `./slimemold mcp` starts the MCP server on stdio
- **SQLite** — persistent graph at `~/.slimemold/{project}/graph.sqlite`
- **Claude Sonnet 4.6** — extracts claims from transcript chunks via structured output
- **Stop hook** — fires every 5th turn, calls parse_transcript, injects audit findings

## Design Principle

Slimemold findings are injected into the conversation as system messages that the
**model** reads — not the user. The purpose is to nudge the model to correct the
mutual trajectory, not to dunk on the user or announce that a tool found problems.

When findings appear, push back the way a good collaborator would — friendly,
helpful, specific. The model is already trained to be helpful; the tool just gives
it structural reasons to redirect rather than agree. "That's an interesting claim —
do you have a source for it?" is better than "WARNING: load-bearing vibes detected."
The model receives the structural diagnosis; the user receives better conversation.

## Slimemold Behavioral Instructions

During essay and research conversations in this project, the slimemold audit hook
runs automatically. When audit findings appear as system messages, address them:

- **CHALLENGE (load-bearing vibes)**: A claim with basis=vibes/assumption supports 2+
  other claims. Upgrade it with a source, mark it as explicitly speculative, or
  challenge it with reasoning.
- **CHALLENGE (fluency trap)**: A claim stated with high confidence but weak basis —
  processing fluency may be masquerading as truth. Ask: would you still believe this
  if it didn't feel so obvious? Find evidence or lower the confidence.
- **REBALANCE (coverage imbalance)**: Reasoning effort is unevenly distributed across
  clusters. "Rabbit hole" means lots of activity but nothing depends on it. "Neglected
  foundation" means other claims depend on it but it's under-explored. Redirect attention.
- **REVISIT (abandoned topic)**: A topic was explored in earlier sessions but dropped.
  Was it resolved, or did something more interesting come along? Either reconnect it
  or explicitly close it out.
- **PUSHBACK (echo chamber)**: The assistant is validating user claims without
  challenging them. Zero contradictions with substantial agreement, or user vibes
  claims accumulating assistant support. Disagree with something concrete, ask for
  counter-evidence, or flag an assumption you've been building on uncritically.
- **INVESTIGATE (unchallenged chain)**: Long chain of claims where nothing was questioned.
  Pick the weakest link and probe it.
- **WATCH (bottleneck)**: Many reasoning paths flow through one unchallenged claim.
  Consider whether downstream conclusions survive if this claim is wrong.
- **WARNING (orphan)**: A claim was registered but never connected. Either connect it
  to the graph or acknowledge it's tangential.

You can also call slimemold tools directly:
- `topology action:viz project:slimemold-essay` — render the ASCII map
- `topology action:get_vulnerabilities project:slimemold-essay` — structural weaknesses
- `claims action:register project:slimemold-essay text:"..." basis:research source:"..."` — manually register
- `claims action:challenge project:slimemold-essay claim_id:... result:upheld` — mark as interrogated

## Development

```bash
go build -o slimemold .
go test ./...
./slimemold viz          # ASCII topology
./slimemold audit        # text audit summary
```

## Dependencies

- mark3labs/mcp-go — MCP server
- modernc.org/sqlite — SQLite (pure Go, no CGO)
- anthropics/anthropic-sdk-go — Sonnet extraction (default; set SLIMEMOLD_MODEL=claude-haiku-4-5-20251001 for cheaper/faster)
- google/uuid — claim IDs
