#!/usr/bin/env bash
# Manually trigger claim extraction on a transcript file.
# Usage: ./scripts/test-extract.sh [transcript_path] [project] [since_turn]

set -euo pipefail

TRANSCRIPT="${1:?Usage: $0 <transcript_path> [project] [since_turn]}"
PROJECT="${2:-$(basename "$PWD")}"
SINCE_TURN="${3:-0}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$SCRIPT_DIR/../slimemold"

if [[ ! -x "$BINARY" ]]; then
  echo "Binary not found at $BINARY — run: go build -o slimemold ." >&2
  exit 1
fi

echo "Extracting from: $TRANSCRIPT"
echo "Project: $PROJECT | Since turn: $SINCE_TURN"
echo "---"

# Build MCP requests: initialize handshake, then parse_transcript
INIT='{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-extract","version":"0.1"}}}'

REQUEST=$(jq -n \
  --arg project "$PROJECT" \
  --arg path "$TRANSCRIPT" \
  --argjson since "$SINCE_TURN" \
  '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"claims",arguments:{action:"parse_transcript",project:$project,transcript_path:$path,since_turn:$since}}}')

RESULT=$(printf '%s\n%s\n' "$INIT" "$REQUEST" | "$BINARY" --project "$PROJECT" mcp)

echo "$RESULT" | python3 -m json.tool 2>/dev/null || echo "$RESULT"

echo "---"
echo "Run './slimemold viz' to see the graph."
