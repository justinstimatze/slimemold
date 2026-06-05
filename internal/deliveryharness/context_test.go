package deliveryharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 16), 4},
		{strings.Repeat("x", 17), 5},
	}
	for _, c := range cases {
		if got := EstimateTokens(c.text); got != c.want {
			t.Errorf("EstimateTokens(len=%d) = %d, want %d", len(c.text), got, c.want)
		}
	}
}

func TestEntryText(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *TextTurn
	}{
		{"empty", "", nil},
		{"whitespace", "   \n", nil},
		{"malformed json", "{not json", nil},
		{"missing role", `{"content":"hi"}`, nil},
		{"system role", `{"role":"system","content":"x"}`, nil},
		{
			"flat user string",
			`{"role":"user","content":"hello"}`,
			&TextTurn{Role: "user", Content: "hello"},
		},
		{
			"flat assistant string",
			`{"role":"assistant","content":"hi back"}`,
			&TextTurn{Role: "assistant", Content: "hi back"},
		},
		{
			"nested message shape",
			`{"message":{"role":"user","content":"hey"}}`,
			&TextTurn{Role: "user", Content: "hey"},
		},
		{
			"array content with text block",
			`{"role":"assistant","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`,
			&TextTurn{Role: "assistant", Content: "one\ntwo"},
		},
		{
			"array content drops tool_use",
			`{"role":"assistant","content":[{"type":"tool_use","name":"foo"},{"type":"text","text":"after"}]}`,
			&TextTurn{Role: "assistant", Content: "after"},
		},
		{
			"only tool_use becomes nil",
			`{"role":"assistant","content":[{"type":"tool_use","name":"foo"}]}`,
			nil,
		},
		{
			"empty text trimmed to nil",
			`{"role":"user","content":"   "}`,
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EntryText(c.line)
			switch {
			case got == nil && c.want == nil:
				return
			case got == nil || c.want == nil:
				t.Fatalf("EntryText(%q) = %v, want %v", c.line, got, c.want)
			case got.Role != c.want.Role || got.Content != c.want.Content:
				t.Errorf("EntryText(%q) = %+v, want %+v", c.line, *got, *c.want)
			}
		})
	}
}

func TestParseTranscriptContext_MergeAndTrim(t *testing.T) {
	lines := []string{
		`{"role":"assistant","content":"orphan assistant first"}`, // trimmed off head
		`{"role":"user","content":"u1"}`,
		`{"role":"user","content":"u2"}`, // merged with u1
		`{"role":"assistant","content":"a1"}`,
		`{"role":"user","content":"u3"}`, // trimmed off tail (no closing assistant after merge)
	}
	got := ParseTranscriptContext(lines, 10000)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; got=%+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "u1\n\nu2" {
		t.Errorf("turn[0] = %+v, want user 'u1\\n\\nu2'", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "a1" {
		t.Errorf("turn[1] = %+v, want assistant 'a1'", got[1])
	}
}

func TestParseTranscriptContext_TokenBudget(t *testing.T) {
	// Each line ~ "user content X" or "assistant ack X" — small, so we can fit
	// a known count. With 5-char content per turn, EstimateTokens=2 each.
	mk := func(role, c string) string {
		return `{"role":"` + role + `","content":"` + c + `"}`
	}
	lines := []string{
		mk("user", "uuuuu"),      // 2 tok
		mk("assistant", "aaaaa"), // 2 tok  -> 4 total
		mk("user", "vvvvv"),      // 2 tok  -> 6 total (>= 5, stop)
		mk("assistant", "wwwww"), // never read
	}
	got := ParseTranscriptContext(lines, 5)
	if len(got) != 2 {
		t.Fatalf("expected 2 turns (budget cut before 3rd user), got %d: %+v", len(got), got)
	}
	if got[len(got)-1].Role != "assistant" {
		t.Errorf("last turn must be assistant after trim, got %q", got[len(got)-1].Role)
	}
}

func TestParseTranscriptContext_NoTextTurnsEmpty(t *testing.T) {
	lines := []string{
		`{"role":"system","content":"x"}`,
		`{"role":"assistant","content":[{"type":"tool_use","name":"foo"}]}`,
		`malformed`,
	}
	got := ParseTranscriptContext(lines, 10000)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
}

func TestLoadRealContext_FromTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := strings.Join([]string{
		`{"role":"user","content":"hello"}`,
		`{"role":"assistant","content":"hi"}`,
		`{"role":"user","content":"more"}`,
		`{"role":"assistant","content":"sure"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := LoadRealContext(path, 10000)
	if err != nil {
		t.Fatalf("LoadRealContext: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 turns, got %d: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[len(got)-1].Role != "assistant" {
		t.Errorf("alternation invariant broken: %+v", got)
	}
}

func TestLoadRealContext_MissingFile(t *testing.T) {
	_, err := LoadRealContext("/nonexistent/path/to.jsonl", 1000)
	if err == nil {
		t.Fatal("expected error on missing file, got nil")
	}
}
