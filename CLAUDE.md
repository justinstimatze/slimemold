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
```

## Dependencies

- mark3labs/mcp-go — MCP server
- modernc.org/sqlite — SQLite (pure Go, no CGO)
- anthropics/anthropic-sdk-go — Sonnet extraction (default; set SLIMEMOLD_MODEL=claude-haiku-4-5-20251001 for cheaper/faster)
- google/uuid — claim IDs
