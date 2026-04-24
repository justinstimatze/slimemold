package extract

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// readRecentTranscript reads a Claude Code transcript (.jsonl) and extracts
// recent conversation turns.
func readRecentTranscript(path string, sinceTurn int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	// Increase buffer for large transcripts
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	// Parse JSONL and extract recent messages
	var messages []string
	turnCount := 0

	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		role, _ := entry["role"].(string)
		if role == "" {
			// Try nested structure
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

		// Extract text content
		text := extractTextContent(entry)
		if text != "" {
			messages = append(messages, fmt.Sprintf("[%s]: %s", role, text))
		}
	}

	// Take last ~50 messages to stay within output token budget (16k).
	// More messages = more claims = larger output JSON. 50 messages typically
	// produces 30-80 claims which fits comfortably in 16k output tokens.
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
