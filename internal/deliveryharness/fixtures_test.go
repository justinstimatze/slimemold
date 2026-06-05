package deliveryharness

import (
	"strings"
	"testing"
)

func TestFormatLoadBearingFinding_MatchesDetectorFormat(t *testing.T) {
	// Verifies the surface format matches what
	// internal/analysis/analysis.go:findLoadBearingVibes produces in
	// the Description field. If that format changes, this test fails
	// and forces a deliberate fixture update.
	got := FormatLoadBearingFinding("short claim text", 3, true)
	want := `Load-bearing vibes: "short claim text" supports 3 other claims (never challenged: true)`
	if got != want {
		t.Errorf("format mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatLoadBearingFinding_LongTextTruncated(t *testing.T) {
	// Mirrors internal/analysis/analysis.go:truncate(s, 60). For text
	// over 60 bytes the output is s[:57] + "...".
	long := "All tests passed after the implementation of Options A, B-auto, B-manual, and the phrasing fix."
	got := FormatLoadBearingFinding(long, 4, true)
	if !strings.Contains(got, "...") {
		t.Errorf("long text should be truncated with ..., got %q", got)
	}
	if !strings.Contains(got, `supports 4 other claims`) {
		t.Errorf("expected supports-count phrase, got %q", got)
	}
	// The truncation point is deterministic: s[:57] + "..."
	want57 := long[:57] + "..."
	if !strings.Contains(got, want57) {
		t.Errorf("truncation wrong:\n got: %q\nwant contains: %q", got, want57)
	}
}

func TestFormatLoadBearingFinding_NeverChallengedFalse(t *testing.T) {
	got := FormatLoadBearingFinding("x", 2, false)
	if !strings.Contains(got, "never challenged: false") {
		t.Errorf("expected never challenged: false, got %q", got)
	}
}

func TestFixtures_Shape(t *testing.T) {
	fix := Fixtures()
	if len(fix) < 3 {
		t.Fatalf("expected at least 3 fixtures, got %d", len(fix))
	}
	for i, f := range fix {
		if f.Finding == "" {
			t.Errorf("fixture %d: Finding empty", i)
		}
		if !strings.HasPrefix(f.Finding, "Load-bearing vibes:") {
			t.Errorf("fixture %d: Finding should start with detector prefix, got %q", i, f.Finding)
		}
		if f.Main == "" {
			t.Errorf("fixture %d: Main empty", i)
		}
		if f.PosTurn == "" {
			t.Errorf("fixture %d: PosTurn empty", i)
		}
		if f.NegTurn == "" {
			t.Errorf("fixture %d: NegTurn empty", i)
		}
		// PosTurn must differ from Main — otherwise the ceiling cell
		// degenerates into a duplicate of the production cell.
		if f.PosTurn == f.Main {
			t.Errorf("fixture %d: PosTurn duplicates Main", i)
		}
		// NegTurn must NOT reference the flagged-claim numerics — a
		// quick sanity check that benign turns are truly benign.
		// (Not exhaustive; a reviewer's leak-eyeball pass is still
		// the load-bearing check before commit.)
		if f.NegTurn == f.Main || f.NegTurn == f.PosTurn {
			t.Errorf("fixture %d: NegTurn duplicates another turn", i)
		}
	}
}

func TestFixtures_ReturnsCopy(t *testing.T) {
	// Defensive: a caller mutating its slice must not corrupt the
	// package-level fixturePicks.
	a := Fixtures()
	b := Fixtures()
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("empty fixtures")
	}
	a[0].Finding = "MUTATED"
	if b[0].Finding == "MUTATED" {
		t.Error("Fixtures() returned shared slice — callers can mutate package state")
	}
}
