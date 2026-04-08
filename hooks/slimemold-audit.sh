#!/bin/bash
# Stop hook: periodic topology audit for slimemold.
# Fires after every assistant response, rate-limited to every Nth turn.
set -euo pipefail
INPUT=$(cat)

CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty')
[ -z "$CWD" ] || [ -z "$TRANSCRIPT" ] && exit 0

# Project name: explicit override file, or fall back to directory name
PROJECT_FILE="$CWD/.slimemold-project"
if [ -f "$PROJECT_FILE" ]; then
  PROJECT=$(cat "$PROJECT_FILE")
else
  PROJECT=$(basename "$CWD")
fi

# Sanitize project name: alphanumeric, hyphens, underscores only
PROJECT=$(echo "$PROJECT" | tr -cd 'a-zA-Z0-9_-')
[ -z "$PROJECT" ] && exit 0

# Rate limit: every 5th stop event
STATE_DIR="$HOME/.slimemold/tmp"
mkdir -p "$STATE_DIR" 2>/dev/null
COUNTER="$STATE_DIR/turns-$(echo "$PROJECT" | md5sum | cut -c1-8).txt"
COUNT=$(( $(cat "$COUNTER" 2>/dev/null || echo 0) + 1 ))
echo "$COUNT" > "$COUNTER"
[ $((COUNT % 5)) -ne 0 ] && exit 0

# Check binary exists
BINARY="$CWD/slimemold"
[ ! -x "$BINARY" ] && exit 0

# Build MCP request safely via jq (no shell interpolation in JSON)
REQUEST=$(jq -n \
  --arg project "$PROJECT" \
  --arg path "$TRANSCRIPT" \
  '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"claims",arguments:{action:"parse_transcript",project:$project,transcript_path:$path}}}')

RESULT=$(echo "$REQUEST" \
  | timeout 45 "$BINARY" mcp 2>/dev/null \
  | jq -r '.result.content[0].text // empty' 2>/dev/null || true)

# Surface if there are any actionable findings
if [ -n "$RESULT" ] && echo "$RESULT" | grep -qE "CHALLENGE|REBALANCE|REVISIT|PUSHBACK|INVESTIGATE|WATCH"; then
    echo "$RESULT"
fi
