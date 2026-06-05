// Package deliveryharness measures whether the host model acts on
// slimemold's injected findings at long context. See cmd/delivery-eval/
// DESIGN.md for the question, conditions, validity gate, and scope.
//
// This file is the transcript loader — a Go port of the equivalent
// piece of buddy's reinject-harness.mjs (parseTranscriptContext +
// loadRealContext). The choice not to share slimemold/internal/extract's
// existing JSONL parser is deliberate: that path is shaped for the
// extraction pipeline (single concatenated string, last-50 cap, error
// silencing for graceful hook degradation). The eval needs structured
// turns, token-budgeted streaming, same-role merging, and alternation
// trimming — different enough that wrapping would obscure both.
package deliveryharness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// TextTurn is one user or assistant turn with its plain-text content.
// tool_use / tool_result blocks are intentionally dropped — the host
// re-running on this context would re-make any tool calls it judges
// necessary, and dropping them keeps the alternation clean.
type TextTurn struct {
	Role    string // "user" or "assistant"
	Content string
}

// EstimateTokens approximates token count without a tokenizer. Matches
// buddy's harness (~4 chars/token) so cross-tool calibration stays
// trivially comparable. The ratio is an over-estimate for code-heavy
// text and an under-estimate for prose — fine for "stop at ~150k".
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// EntryText parses one JSONL line into a TextTurn, or returns nil if the
// line is empty, malformed, or not a user/assistant text turn. Accepts
// both the flat shape ({role, content}) and Claude Code's nested shape
// ({message: {role, content}}).
//
// Returns nil for tool_use/tool_result/system entries — they are noise
// for the conversational context the eval reconstructs.
func EntryText(line string) *TextTurn {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}

	role, _ := raw["role"].(string)
	content := raw["content"]
	if role == "" {
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			role, _ = msg["role"].(string)
			content = msg["content"]
		}
	}
	if role != "user" && role != "assistant" {
		return nil
	}

	text := extractText(content)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &TextTurn{Role: role, Content: text}
}

// extractText pulls the human-readable text out of a content field that
// may be a bare string or an array of typed blocks. Anything that isn't
// a `text` block is ignored — tool_use/tool_result/image etc. don't
// belong in a reconstructed conversation.
func extractText(content interface{}) string {
	if s, ok := content.(string); ok {
		return s
	}
	arr, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, b := range arr {
		m, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] != "text" {
			continue
		}
		if t, ok := m["text"].(string); ok {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// ParseTranscriptContext folds a slice of JSONL lines into a usable
// context up to ~targetTokens. Three normalizations, in order:
//
//  1. Drop non-text-turn lines (EntryText returns nil).
//  2. Merge consecutive same-role turns — the Messages API rejects
//     two user or two assistant blocks in a row.
//  3. Trim head and tail so the slice starts with a user turn and ends
//     with an assistant turn — the eval appends a final user turn to
//     this context, so it must end on assistant for valid alternation.
//
// Stops accumulating raw turns once the running token estimate crosses
// targetTokens; later trims may shrink the result.
func ParseTranscriptContext(lines []string, targetTokens int) []TextTurn {
	var raw []TextTurn
	tokens := 0
	for _, line := range lines {
		t := EntryText(line)
		if t == nil {
			continue
		}
		raw = append(raw, *t)
		tokens += EstimateTokens(t.Content)
		if tokens >= targetTokens {
			break
		}
	}

	merged := make([]TextTurn, 0, len(raw))
	for _, t := range raw {
		if n := len(merged); n > 0 && merged[n-1].Role == t.Role {
			merged[n-1].Content += "\n\n" + t.Content
			continue
		}
		merged = append(merged, t)
	}

	for len(merged) > 0 && merged[0].Role != "user" {
		merged = merged[1:]
	}
	for len(merged) > 0 && merged[len(merged)-1].Role != "assistant" {
		merged = merged[:len(merged)-1]
	}
	return merged
}

// LoadRealContext streams a JSONL transcript and returns a trimmed,
// merged context targeting ~targetTokens. Reads only the prefix
// needed — a multi-hundred-MB transcript is fine — with 10% headroom
// so ParseTranscriptContext can trim to the exact target.
func LoadRealContext(path string, targetTokens int) ([]TextTurn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var lines []string
	textTokens := 0
	headroom := targetTokens + targetTokens/10
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if t := EntryText(line); t != nil {
			textTokens += EstimateTokens(t.Content)
		}
		if textTokens >= headroom {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript %s: %w", path, err)
	}

	return ParseTranscriptContext(lines, targetTokens), nil
}
