package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript writes n user/assistant turns (alternating) as a JSONL file
// and returns its path. Each line matches the {"role","content"} shape
// extractTextContent expects.
func writeTranscript(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		fmt.Fprintf(&b, `{"role":%q,"content":"turn %d"}`+"\n", role, i)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatalf("writing fixture transcript: %v", err)
	}
	return path
}

// A backlog bigger than the 50-message read cap must report a boundary
// short of the transcript's true end — regression guard for the
// cursor-overshoot bug in FEEDBACK-extraction-backlog-ratchet.md, where
// advancing the cursor to a full-file turn count silently discarded every
// unread turn past the cap.
func TestReadRecentTranscript_CappedBacklogReportsPartialBoundary(t *testing.T) {
	path := writeTranscript(t, 200)

	chunk, readThroughTurn, err := readRecentTranscript(path, 10)
	if err != nil {
		t.Fatalf("readRecentTranscript: %v", err)
	}
	if readThroughTurn <= 10 || readThroughTurn >= 200 {
		t.Fatalf("readThroughTurn = %d, want strictly between sinceTurn (10) and total (200)", readThroughTurn)
	}
	if got := strings.Count(chunk, "\n\n") + 1; got != 50 {
		t.Fatalf("chunk contains %d messages, want the 50-message cap", got)
	}
}

// A backlog smaller than the cap should read to EOF and report the
// transcript's true total, matching CountTranscriptTurns.
func TestReadRecentTranscript_SmallBacklogReachesTotal(t *testing.T) {
	path := writeTranscript(t, 30)

	_, readThroughTurn, err := readRecentTranscript(path, 10)
	if err != nil {
		t.Fatalf("readRecentTranscript: %v", err)
	}
	total, err := CountTranscriptTurns(path)
	if err != nil {
		t.Fatalf("CountTranscriptTurns: %v", err)
	}
	if readThroughTurn != total {
		t.Fatalf("readThroughTurn = %d, want %d (transcript total)", readThroughTurn, total)
	}
}

// Baseline reads (sinceTurn == 0) tail-seek from an arbitrary offset, so
// their local turn counter isn't comparable to sinceTurn/preCountedTurns —
// callers must fall back to a full-file count instead.
func TestReadRecentTranscript_BaselineReportsNoBoundary(t *testing.T) {
	path := writeTranscript(t, 30)

	_, readThroughTurn, err := readRecentTranscript(path, 0)
	if err != nil {
		t.Fatalf("readRecentTranscript: %v", err)
	}
	if readThroughTurn != 0 {
		t.Fatalf("readThroughTurn = %d, want 0 for a baseline (sinceTurn=0) read", readThroughTurn)
	}
}
