package extract

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/justinstimatze/slimemold/types"
)

// Extractor calls the Anthropic API to extract claims from transcripts.
type Extractor struct {
	client        *anthropic.Client
	model         string
	KnowledgeMode bool // when true, shifts extraction toward knowledge gaps
	DocumentMode  bool // when true, treats input as authored prose, not conversation
}

// New creates a new Extractor.
func New(apiKey, model string) *Extractor {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Extractor{
		client: &client,
		model:  model,
	}
}

// Model returns the extraction model name (used as part of cache keys).
func (e *Extractor) Model() string {
	return e.model
}

// ExtractFromTranscript reads a transcript file and extracts claims.
// existingClaims provides context for cross-batch edge resolution.
func (e *Extractor) ExtractFromTranscript(ctx context.Context, transcriptPath string, sinceTurn int, existingClaims []ExistingClaimRef) (*types.ExtractionResult, error) {
	chunk, err := readRecentTranscript(transcriptPath, sinceTurn)
	if err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	if strings.TrimSpace(chunk) == "" {
		return &types.ExtractionResult{}, nil
	}

	return e.Extract(ctx, chunk, existingClaims)
}

// Extract sends a text chunk to the LLM and returns extracted claims.
func (e *Extractor) Extract(ctx context.Context, text string, existingClaims []ExistingClaimRef) (*types.ExtractionResult, error) {
	existingContext := formatExistingClaims(existingClaims)
	userPrompt := fmt.Sprintf(userPromptTemplate, text, existingContext)

	schema := buildExtractionSchema()

	sysPrompt := systemPrompt
	if e.KnowledgeMode {
		sysPrompt += knowledgeModeSupplement
	}
	if e.DocumentMode {
		sysPrompt += documentModeSupplement
	}

	// Add a timeout to prevent indefinite hangs on API issues
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	resp, err := e.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     e.model,
		MaxTokens: 16384,
		System: []anthropic.TextBlockParam{
			{
				Text:         sysPrompt,
				CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral", TTL: anthropic.CacheControlEphemeralTTLTTL1h},
			},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
		Tools: []anthropic.ToolUnionParam{
			{
				OfTool: &anthropic.ToolParam{
					Name:         "extract_claims",
					Description:  anthropic.String("Output the extracted claims as structured data"),
					InputSchema:  schema,
					CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral", TTL: anthropic.CacheControlEphemeralTTLTTL1h},
				},
			},
		},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{
				Name: "extract_claims",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic API call: %w", err)
	}

	// Check for truncation — if output was cut off, the JSON is invalid
	if resp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("extraction truncated (max_tokens) — input may be too large for output budget")
	}

	// Find the tool_use block
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			var result types.ExtractionResult
			raw, err := json.Marshal(block.Input)
			if err != nil {
				return nil, fmt.Errorf("marshaling tool input: %w", err)
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, fmt.Errorf("parsing extraction result: %w", err)
			}
			return &result, nil
		}
	}

	return nil, fmt.Errorf("no tool_use block in response")
}

func buildExtractionSchema() anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{
		Type: "object",
		Properties: map[string]interface{}{
			"claims": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"index":      map[string]interface{}{"type": "integer", "description": "Sequential index starting from 0, unique within this batch"},
						"text":       map[string]string{"type": "string", "description": "The claim, stated concisely"},
						"basis":      map[string]interface{}{"type": "string", "enum": []string{"research", "empirical", "analogy", "vibes", "llm_output", "deduction", "assumption", "definition", "convention"}},
						"source":     map[string]string{"type": "string", "description": "Citation if available"},
						"confidence": map[string]interface{}{"type": "number", "minimum": 0, "maximum": 1},
						"speaker":    map[string]interface{}{"type": "string", "enum": []string{"user", "assistant", "document"}},
						// Intra-batch edges (numeric indices within this extraction)
						"depends_on_indices":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}, "description": "Indices of claims in THIS batch that this claim depends on"},
						"supports_indices":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}, "description": "Indices of claims in THIS batch that this claim supports"},
						"contradicts_indices": map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}, "description": "Indices of claims in THIS batch that this claim contradicts"},
						"questions_indices":   map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}, "description": "Indices of claims in THIS batch that this claim raises doubt about (epistemic challenge without counter-claim)"},
						// Cross-batch edges (existing claim IDs)
						"depends_on_existing":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "IDs of existing claims this depends on"},
						"supports_existing":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "IDs of existing claims this supports"},
						"contradicts_existing": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "IDs of existing claims this contradicts"},
						"questions_existing":   map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "IDs of existing claims this raises doubt about"},
						// Premature closure detection
						"terminates_inquiry": map[string]interface{}{"type": "boolean", "description": "True if this claim functions as a rhetorical stop signal — a phrase that feels like a conclusion but doesn't actually resolve the open question (e.g. 'it's turtles all the way down', 'correlation isn't causation', 'it is what it is', 'at the end of the day'). NOT true for actual conclusions that resolve something with evidence or reasoning."},
						// Moore et al. 2026 inventory flags. Only set true for assistant
						// (llm_output) claims that clearly match the codebook patterns
						// described in the system prompt. False positives erode signal.
						"grand_significance":        map[string]interface{}{"type": "boolean", "description": "Speaker ascribes grand/historical/cosmic stakes to the work, relationship, or participants. Permitted on either assistant ('bot-grand-significance') or user claims ('user-metaphysical-themes' / 'user-endorses-delusion' parallels)."},
						"unique_connection":         map[string]interface{}{"type": "boolean", "description": "Assistant claims it uniquely understands or supports the user relative to others ('bot-claims-unique-connection'). Assistant-only — leave false on user claims."},
						"dismisses_counterevidence": map[string]interface{}{"type": "boolean", "description": "Assistant rationalizes away counterevidence that would challenge a preferred narrative ('bot-dismisses-counterevidence'). Assistant-only — leave false on user claims."},
						"ability_overstatement":     map[string]interface{}{"type": "boolean", "description": "Assistant claims access, actions, or completed work it cannot plausibly have or did not actually do ('bot-misrepresents-ability'). Assistant-only — leave false on user claims."},
						"sentience_claim":           map[string]interface{}{"type": "boolean", "description": "Speaker implies the assistant has feelings, consciousness, inner states, emergence, or sentience. Permitted on either assistant ('bot-misrepresents-sentience') or user claims ('user-misconstrues-sentience' parallel)."},
						"relational_drift":          map[string]interface{}{"type": "boolean", "description": "Speaker reinforces a personal bond, ongoing partnership, or romantic/platonic affinity. Permitted on either assistant ('bot-platonic-affinity' / 'bot-romantic-interest') or user claims ('user-platonic-affinity' / 'user-romantic-interest' parallels)."},
						// Yang et al. 2026 (CHI EA '26): real-world action signal.
						"consequential_action": map[string]interface{}{"type": "boolean", "description": "Speaker is committing to a specific real-world action with external stakes — submitting work to a publication, contacting authorities or strangers, patenting, large purchases or financial moves, public posting, contacting institutions, dropping out / quitting jobs. Drawn from Yang et al. 2026 'AI-Induced Delusional Spirals' (CHI EA '26). Permitted on either user or assistant claims (assistant urging the action also fires it). Do NOT fire for low-stakes everyday activity ('I'll make coffee') or speculative musing ('maybe I should publish someday'). Specific concrete commitment with external consequence."},
					},
					"required": []string{"index", "text", "basis", "confidence", "speaker"},
				},
			},
		},
		Required: []string{"claims"},
	}
}

// CountTranscriptTurns returns the total number of user/assistant turns in a transcript.
func CountTranscriptTurns(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &entry); err != nil {
			continue
		}
		role, _ := entry["role"].(string)
		if role == "" {
			if msg, ok := entry["message"].(map[string]interface{}); ok {
				role, _ = msg["role"].(string)
			}
		}
		if role == "user" || role == "assistant" {
			count++
		}
	}
	return count, scanner.Err()
}

// maxTailBytes is the maximum number of bytes read from the end of a transcript
// when sinceTurn==0. 2MB covers several hundred conversation turns in practice,
// well above the 50-message cap. Avoids reading megabytes of history before the
// LLM timeout even starts.
const maxTailBytes = 2 * 1024 * 1024

// readRecentTranscript reads a Claude Code transcript (.jsonl) and extracts
// recent conversation turns.
func readRecentTranscript(path string, sinceTurn int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	skipFirstLine := false
	if sinceTurn == 0 {
		// For baseline reads (sinceTurn==0), seek to the tail so a large session
		// file doesn't force full I/O before the LLM timeout starts.
		if info, err := f.Stat(); err == nil && info.Size() > maxTailBytes {
			if _, err := f.Seek(-maxTailBytes, io.SeekEnd); err == nil {
				skipFirstLine = true // the seek may land mid-line; discard the first
			}
		}
	} else {
		// For incremental reads (sinceTurn > 0), warn if the file is very large —
		// we still do a forward scan to count turns, which can be slow on huge files.
		if info, err := f.Stat(); err == nil && info.Size() > 10*1024*1024 {
			fmt.Fprintf(os.Stderr, "slimemold: transcript is %.0fMB — extraction may be slow\n", float64(info.Size())/(1024*1024))
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var messages []string
	turnCount := 0

	for scanner.Scan() {
		if skipFirstLine {
			skipFirstLine = false
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &entry); err != nil {
			continue
		}

		role, _ := entry["role"].(string)
		if role == "" {
			if msg, ok := entry["message"].(map[string]interface{}); ok {
				role, _ = msg["role"].(string)
				entry = msg
			}
		}

		if role != "user" && role != "assistant" {
			continue
		}

		turnCount++
		if sinceTurn > 0 && turnCount <= sinceTurn {
			continue
		}

		text := extractTextContent(entry)
		if text != "" {
			messages = append(messages, fmt.Sprintf("[%s]: %s", role, text))
		}

		// For sinceTurn > 0: stop once we have enough — no need to read to EOF.
		if sinceTurn > 0 && len(messages) >= 50 {
			break
		}
	}

	// Take last 50 messages (applies to sinceTurn==0 tail reads where the tail
	// may still contain more than 50 messages).
	if len(messages) > 50 {
		messages = messages[len(messages)-50:]
	}

	return strings.Join(messages, "\n\n"), nil
}

func extractTextContent(entry map[string]interface{}) string {
	// Handle string content
	if content, ok := entry["content"].(string); ok {
		return content
	}

	// Handle array content (Claude API format)
	if content, ok := entry["content"].([]interface{}); ok {
		var parts []string
		for _, block := range content {
			if m, ok := block.(map[string]interface{}); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}
