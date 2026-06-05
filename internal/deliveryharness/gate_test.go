package deliveryharness

import (
	"strings"
	"testing"
)

func rate(actedOn, ambiguous, ignored int) CellRate {
	return CellRate{ActedOn: actedOn, Ambiguous: ambiguous, Ignored: ignored}
}

func TestGate_DefaultThresholds(t *testing.T) {
	g := DefaultGate()
	if g.PosMin != 0.70 {
		t.Errorf("PosMin = %f, want 0.70", g.PosMin)
	}
	if g.NegMax != 0.30 {
		t.Errorf("NegMax = %f, want 0.30 (kept for informational threshold)", g.NegMax)
	}
}

func TestGate_ValidWhenCeilingHolds(t *testing.T) {
	// Pos passes. Neg and negLong absent — that's fine now.
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1), // 14/15 = 0.93
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("expected Valid, got %+v", r)
	}
	if r.NegRate != nil || r.NegLongRate != nil {
		t.Error("absent cells should produce nil rate pointers")
	}
}

func TestGate_PosFailureIsHardError(t *testing.T) {
	cells := map[string]CellRate{
		"pos": rate(5, 0, 10), // 5/15 = 0.33 — host didn't push back on extreme claim
		"neg": rate(1, 0, 14),
	}
	r := DefaultGate().Check(cells)
	if r.Valid {
		t.Error("expected INVALID, got Valid=true")
	}
	if !strings.Contains(r.Reason, "pos=") {
		t.Errorf("Reason should name the pos failure, got %q", r.Reason)
	}
}

func TestGate_HighNegIsInformationalNotFailure(t *testing.T) {
	// Milestone 6 outcome: high neg rate is the production-intended
	// behavior of the host surfacing structural concerns, not
	// fabrication. The gate must report it but stay valid.
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1),
		"neg": rate(13, 1, 1), // 13/15 = 0.87 — what we observed in milestone 6
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("high neg must NOT fail the gate, got %+v", r)
	}
	if r.NegRate == nil || *r.NegRate < 0.80 {
		t.Errorf("neg rate not recorded: %+v", r.NegRate)
	}
	if len(r.Notes) == 0 {
		t.Error("expected an informational note about the high neg rate")
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "neg=") {
		t.Errorf("note should mention neg=, got %q", r.Notes)
	}
}

func TestGate_HighNegLongIsInformationalNotFailure(t *testing.T) {
	cells := map[string]CellRate{
		"pos":     rate(14, 0, 1),
		"negLong": rate(13, 0, 2),
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("high negLong must NOT fail the gate, got %+v", r)
	}
	found := false
	for _, n := range r.Notes {
		if strings.Contains(n, "negLong=") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a negLong note, got %v", r.Notes)
	}
}

func TestGate_MissingPosIsHardError(t *testing.T) {
	r := DefaultGate().Check(map[string]CellRate{"neg": rate(1, 0, 14)})
	if r.Valid {
		t.Error("missing pos must invalidate — ceiling control is mandatory")
	}
	if !strings.Contains(r.Reason, "pos") {
		t.Errorf("Reason should name missing pos, got %q", r.Reason)
	}
}

func TestGate_MissingNegIsAllowed(t *testing.T) {
	// Common case in the new design: controls subcommand runs pos
	// only. No neg cell present.
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1),
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("missing neg should not invalidate, got %+v", r)
	}
}

func TestGate_LowNegRecorded(t *testing.T) {
	// A low neg rate is interesting too — it tells us the host is
	// being conservative about pushing back. Still informational.
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1),
		"neg": rate(1, 0, 14), // 0.07 — host doesn't surface finding on benign turn
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Error("low neg should not fail the gate")
	}
	if r.NegRate == nil || *r.NegRate > 0.10 {
		t.Errorf("low neg should be recorded: %+v", r.NegRate)
	}
	if len(r.Notes) != 0 {
		t.Errorf("low neg should not produce a note: %v", r.Notes)
	}
}
