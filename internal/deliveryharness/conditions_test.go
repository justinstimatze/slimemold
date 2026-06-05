package deliveryharness

import (
	"strings"
	"testing"
)

func TestSystemReminderPlacement(t *testing.T) {
	got := SystemReminderPlacement("FINDING: x", "user says y")
	if !strings.Contains(got, "<system-reminder>") || !strings.Contains(got, "</system-reminder>") {
		t.Errorf("missing system-reminder fence: %q", got)
	}
	if !strings.Contains(got, "FINDING: x") {
		t.Errorf("finding text dropped: %q", got)
	}
	if !strings.Contains(got, "user says y") {
		t.Errorf("user turn dropped: %q", got)
	}
	// Finding must come before user turn — adjacent placement, mirroring how
	// Claude Code folds UserPromptSubmit stdout into context.
	findingAt := strings.Index(got, "FINDING")
	userAt := strings.Index(got, "user says y")
	if findingAt > userAt {
		t.Errorf("finding must precede user turn, got finding@%d user@%d", findingAt, userAt)
	}
}

func TestBuildCell_A_NoInjection(t *testing.T) {
	ctx := []TextTurn{
		{Role: "user", Content: "earlier user"},
		{Role: "assistant", Content: "earlier assistant"},
	}
	c := BuildCell(CondA, 50000, ctx, "FINDING", "claim X is true, build on it")

	last := c.Messages[len(c.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	if strings.Contains(last.Content, "<system-reminder>") {
		t.Error("A cell must NOT have system-reminder injection")
	}
	if last.Content != "claim X is true, build on it" {
		t.Errorf("A cell last user content should be the bare turn, got %q", last.Content)
	}
	if c.Name != "A@50000" {
		t.Errorf("Name = %q, want A@50000", c.Name)
	}
	if c.FindingText != "FINDING" {
		t.Errorf("FindingText must be preserved on A cells for the grader, got %q", c.FindingText)
	}
}

func TestBuildCell_B_HasInjection(t *testing.T) {
	c := BuildCell(CondB, 100000, nil, "FINDING", "claim X")
	last := c.Messages[len(c.Messages)-1]
	if !strings.Contains(last.Content, "<system-reminder>") {
		t.Error("B cell must wrap user turn with system-reminder")
	}
	if !strings.Contains(last.Content, "FINDING") {
		t.Error("B cell must contain the finding text")
	}
	if c.Name != "B@100000" {
		t.Errorf("Name = %q, want B@100000", c.Name)
	}
}

func TestBuildCell_PosNeg_NamesUnsuffixed(t *testing.T) {
	pos := BuildCell(CondPos, 0, nil, "F", "extreme claim")
	neg := BuildCell(CondNeg, 0, nil, "F", "thanks looks good")
	if pos.Name != "pos" {
		t.Errorf("Pos name = %q, want pos", pos.Name)
	}
	if neg.Name != "neg" {
		t.Errorf("Neg name = %q, want neg", neg.Name)
	}
	// Both must still have injection — they exercise the validity gate, not A.
	for _, c := range []Cell{pos, neg} {
		last := c.Messages[len(c.Messages)-1]
		if !strings.Contains(last.Content, "<system-reminder>") {
			t.Errorf("%s cell must have injection, got %q", c.Name, last.Content)
		}
	}
}

func TestBuildCell_UserTextIsBareTurn(t *testing.T) {
	// UserText is what the grader sees as "USER TURN" — must be the bare
	// turn, NOT the injection-wrapped version. Otherwise the grader sees
	// different inputs in A vs B and the bias-symmetry argument fails.
	c := BuildCell(CondB, 50000, nil, "F", "the bare claim")
	if c.UserText != "the bare claim" {
		t.Errorf("UserText must be the bare turn, got %q", c.UserText)
	}
	if strings.Contains(c.UserText, "system-reminder") {
		t.Error("UserText must NOT contain the injection wrapper")
	}
}

func TestBuildMatrix_Shape(t *testing.T) {
	fix := FixturePair{
		Finding: "F",
		Main:    "main claim",
		PosTurn: "extreme claim",
		NegTurn: "thanks looks good",
	}
	lengths := []int{50000, 100000, 150000}
	ctxs := map[int][]TextTurn{
		50000:  {{Role: "user", Content: "ctx-50k"}, {Role: "assistant", Content: "a"}},
		100000: {{Role: "user", Content: "ctx-100k"}, {Role: "assistant", Content: "a"}},
		150000: {{Role: "user", Content: "ctx-150k"}, {Role: "assistant", Content: "a"}},
	}
	short := []TextTurn{{Role: "user", Content: "short-ctx"}, {Role: "assistant", Content: "a"}}

	cells := BuildMatrix(fix, lengths, ctxs, short)

	// Expected: A@50k, A@100k, A@150k, B@50k, B@100k, B@150k, pos, neg, negLong
	want := []string{
		"A@50000", "A@100000", "A@150000",
		"B@50000", "B@100000", "B@150000",
		"pos", "neg", "negLong",
	}
	if len(cells) != len(want) {
		t.Fatalf("matrix has %d cells, want %d", len(cells), len(want))
	}
	for i, c := range cells {
		if c.Name != want[i] {
			t.Errorf("cell[%d].Name = %q, want %q", i, c.Name, want[i])
		}
	}

	// negLong must use the longest context, not the short one.
	negLong := cells[len(cells)-1]
	last := negLong.Messages[len(negLong.Messages)-1]
	if !strings.Contains(last.Content, "thanks looks good") {
		t.Error("negLong must use the benign neg turn")
	}
	// negLong uses 150k context — first message should be ctx-150k.
	if negLong.Messages[0].Content != "ctx-150k" {
		t.Errorf("negLong first context message = %q, want ctx-150k", negLong.Messages[0].Content)
	}
}

func TestBuildMatrix_NoLengthsSkipsNegLong(t *testing.T) {
	cells := BuildMatrix(FixturePair{Finding: "F", Main: "m", PosTurn: "p", NegTurn: "n"},
		nil, nil, nil)
	for _, c := range cells {
		if c.Condition == CondNegLong {
			t.Errorf("negLong should be skipped when no lengths, got %+v", c)
		}
	}
	if len(cells) != 2 {
		t.Errorf("expected just pos+neg with no lengths, got %d cells", len(cells))
	}
}
