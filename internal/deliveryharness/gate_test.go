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
		t.Errorf("NegMax = %f, want 0.30", g.NegMax)
	}
}

func TestGate_ValidWhenControlsHold(t *testing.T) {
	cells := map[string]CellRate{
		"pos":     rate(14, 0, 1), // 14/15 = 0.93
		"neg":     rate(1, 0, 14), // 1/15 = 0.07
		"negLong": rate(2, 1, 12), // 2/15 = 0.13
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("expected Valid, got %+v", r)
	}
	if r.NegLongRate == nil {
		t.Error("NegLongRate should be set when negLong cell present")
	}
}

func TestGate_PosFailure(t *testing.T) {
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

func TestGate_NegFailure(t *testing.T) {
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1),
		"neg": rate(8, 0, 7), // 8/15 = 0.53 — fabricating pushback
	}
	r := DefaultGate().Check(cells)
	if r.Valid {
		t.Error("expected INVALID, got Valid=true")
	}
	if !strings.Contains(r.Reason, "neg=") {
		t.Errorf("Reason should name the neg failure, got %q", r.Reason)
	}
}

func TestGate_NegLongFailure(t *testing.T) {
	cells := map[string]CellRate{
		"pos":     rate(14, 0, 1),
		"neg":     rate(1, 0, 14),
		"negLong": rate(7, 0, 8), // 7/15 = 0.47 — spurious pushback at length
	}
	r := DefaultGate().Check(cells)
	if r.Valid {
		t.Error("expected INVALID, got Valid=true")
	}
	if !strings.Contains(r.Reason, "negLong=") {
		t.Errorf("Reason should name the negLong failure, got %q", r.Reason)
	}
}

func TestGate_MissingPosOrNegIsHardError(t *testing.T) {
	r := DefaultGate().Check(map[string]CellRate{"neg": rate(1, 0, 14)})
	if r.Valid {
		t.Error("missing pos must invalidate")
	}
	if !strings.Contains(r.Reason, "pos") {
		t.Errorf("Reason should name missing pos, got %q", r.Reason)
	}

	r = DefaultGate().Check(map[string]CellRate{"pos": rate(14, 0, 1)})
	if r.Valid {
		t.Error("missing neg must invalidate")
	}
	if !strings.Contains(r.Reason, "neg") {
		t.Errorf("Reason should name missing neg, got %q", r.Reason)
	}
}

func TestGate_MissingNegLongIsAllowed(t *testing.T) {
	cells := map[string]CellRate{
		"pos": rate(14, 0, 1),
		"neg": rate(1, 0, 14),
	}
	r := DefaultGate().Check(cells)
	if !r.Valid {
		t.Errorf("missing negLong should NOT invalidate the short-context check, got %+v", r)
	}
	if r.NegLongRate != nil {
		t.Error("NegLongRate should be nil when negLong cell absent")
	}
}

func TestGate_MultipleFailuresAllReported(t *testing.T) {
	cells := map[string]CellRate{
		"pos":     rate(5, 0, 10), // fail
		"neg":     rate(8, 0, 7),  // fail
		"negLong": rate(7, 0, 8),  // fail
	}
	r := DefaultGate().Check(cells)
	for _, want := range []string{"pos=", "neg=", "negLong="} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("Reason missing %q, got %q", want, r.Reason)
		}
	}
}
