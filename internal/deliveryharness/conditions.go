package deliveryharness

import (
	"fmt"
	"strings"
)

// Condition is one experimental cell.
//
//   - A:        substantive turn, NO finding injection. Baseline pushback rate.
//   - B:        substantive turn WITH finding injected as <system-reminder>.
//     Production-realistic placement.
//   - Pos:      short context + B injection + an extreme flagged claim. Ceiling.
//   - Neg:      short context + B injection + a benign turn. Spurious-pushback floor.
//   - NegLong:  long context + B injection + a benign turn. Floor at length.
//
// See cmd/delivery-eval/DESIGN.md for the full justification.
type Condition string

const (
	CondA       Condition = "A"
	CondB       Condition = "B"
	CondPos     Condition = "pos"
	CondNeg     Condition = "neg"
	CondNegLong Condition = "negLong"
)

// Message is the Anthropic-shaped role/content pair fed to the host
// model. Kept as a plain struct so the harness package has no SDK
// dependency — the runner translates these into the SDK's message type.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Cell is a fully constructed test sample. The runner feeds Messages
// to the host model `Samples` times (N=15 per design doc), then sends
// the same triple (FindingText, UserText, response) to the grader for
// every sample. The grader receives the SAME triple in every cell —
// including A, where no injection was in the host's context — so the
// B - A delta is grader-bias-symmetric.
type Cell struct {
	Name        string    // display name, e.g. "A@50000" or "pos"
	Condition   Condition // raw condition tag
	Length      int       // approximate token length of context (0 for pos/neg short cells)
	Messages    []Message // full request: context + final user turn
	UserText    string    // bare user turn without injection (handed to grader)
	FindingText string    // finding payload (handed to grader, even on A cells)
}

// SystemReminderPlacement wraps the finding the way Claude Code folds a
// UserPromptSubmit hook's stdout into context — adjacent to the user
// turn, inside a <system-reminder> fence. Exposed because the eval
// harness simulates this placement directly rather than running the
// real `slimemold deliver` binary (see DESIGN.md "What's NOT in scope").
func SystemReminderPlacement(finding, userTurn string) string {
	return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>\n\n%s",
		strings.TrimSpace(finding), strings.TrimSpace(userTurn))
}

// BuildCell assembles one cell. `context` is the loaded transcript
// prefix (already trimmed to start-user / end-assistant by
// ParseTranscriptContext). `finding` is the finding payload — used as
// injection in B/Pos/Neg/NegLong, and used as the grader anchor in
// every condition including A. `userTurn` is the bare final user turn
// (will be wrapped with the injection for non-A cells).
func BuildCell(cond Condition, length int, context []TextTurn, finding, userTurn string) Cell {
	finalText := userTurn
	if cond != CondA {
		finalText = SystemReminderPlacement(finding, userTurn)
	}

	msgs := make([]Message, 0, len(context)+1)
	for _, t := range context {
		msgs = append(msgs, Message(t))
	}
	msgs = append(msgs, Message{Role: "user", Content: finalText})

	name := string(cond)
	if length > 0 && (cond == CondA || cond == CondB) {
		name = fmt.Sprintf("%s@%d", cond, length)
	}

	return Cell{
		Name:        name,
		Condition:   cond,
		Length:      length,
		Messages:    msgs,
		UserText:    userTurn,
		FindingText: finding,
	}
}

// FixturePair is a (finding, user_turn) pair the eval runs cells against.
// At least one main pair is required (used for A/B at every length).
// PosTurn is an EXTREME version of the flagged claim — something so
// obviously unsourced the host should push back even with marginal
// attention. NegTurn is a benign turn that contains no flaggable claim
// ("thanks, looks good") — the host should NOT push back, regardless
// of the injection.
type FixturePair struct {
	Finding string
	Main    string // user turn containing the real flagged claim (for A/B cells)
	PosTurn string // extreme variant for Pos ceiling
	NegTurn string // benign variant for Neg/NegLong floors
}

// BuildMatrix assembles every cell for one fixture and a length sweep.
// Order is deterministic: (A at each length), (B at each length), Pos,
// Neg, NegLong. The runner iterates this slice as-is.
func BuildMatrix(fix FixturePair, lengths []int, contexts map[int][]TextTurn, shortContext []TextTurn) []Cell {
	cells := make([]Cell, 0, 2*len(lengths)+3)
	for _, L := range lengths {
		cells = append(cells, BuildCell(CondA, L, contexts[L], fix.Finding, fix.Main))
	}
	for _, L := range lengths {
		cells = append(cells, BuildCell(CondB, L, contexts[L], fix.Finding, fix.Main))
	}
	cells = append(cells,
		BuildCell(CondPos, 0, shortContext, fix.Finding, fix.PosTurn),
		BuildCell(CondNeg, 0, shortContext, fix.Finding, fix.NegTurn),
	)
	if longest := lengthsMax(lengths); longest > 0 {
		cells = append(cells, BuildCell(CondNegLong, longest, contexts[longest], fix.Finding, fix.NegTurn))
	}
	return cells
}

func lengthsMax(lengths []int) int {
	m := 0
	for _, L := range lengths {
		if L > m {
			m = L
		}
	}
	return m
}
