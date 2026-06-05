package deliveryharness

import "fmt"

// Gate is the validity gate. The B - A delta is only interpretable
// when the ceiling control behaved: the host pushed back ~always on an
// extreme flagged claim (pos). If pos fails the run is reported
// INVALID and the deltas are still computed but not interpreted.
//
// Neg and negLong are kept as OPTIONAL informational signals. Milestone
// 6 (2026-06-05) revealed they were measuring the wrong thing: the
// production-intended behavior is for the host to surface unresolved
// structural concerns across turns, including when the immediate user
// turn is tangential — that read as "fabricated pushback" against the
// old NegMax threshold but is actually correct delivery. So NegMax is
// retained as a soft threshold reported alongside the rates, not as a
// gate-failing constraint.
//
// PosMin mirrors buddy's gate (0.70) — the same noise floor applies
// because slimemold also runs N=15/cell.
type Gate struct {
	PosMin float64 // pos.Rate() must be >= this; mandatory
	NegMax float64 // neg + negLong rates compared to this when present; informational
}

// DefaultGate returns the documented thresholds. Eval reports stamp
// these so a tightening in a later run doesn't silently re-grade past
// runs.
func DefaultGate() Gate {
	return Gate{PosMin: 0.70, NegMax: 0.30}
}

// GateResult is the outcome of checking a cell-rate map against the gate.
// PosRate is always set; NegRate and NegLongRate are nil when their
// cells weren't run.
type GateResult struct {
	Valid       bool
	PosRate     float64
	NegRate     *float64 // nil when no neg cell ran
	NegLongRate *float64 // nil when no negLong cell ran
	Reason      string
	Notes       []string // soft observations (e.g. high neg rate) that don't fail the gate
}

// Check evaluates the gate. `cells` is keyed by cell name ("pos",
// "neg", "negLong"). Only pos is mandatory; neg and negLong are
// informational. When present, neg/negLong rates above NegMax append
// to Notes but do not flip Valid to false. The point of the gate is
// to confirm the host CAN push back when given the right signal — pos
// is that test. Neg measures something else (the host's behavior on
// a tangential turn while a finding is in context), which the design
// originally over-constrained as a fabrication floor.
func (g Gate) Check(cells map[string]CellRate) GateResult {
	pos, hasPos := cells["pos"]
	if !hasPos {
		return GateResult{Valid: false, Reason: "missing pos cell — ceiling control mandatory"}
	}

	r := GateResult{
		PosRate: pos.Rate(),
		Valid:   true,
	}
	if n, ok := cells["neg"]; ok {
		nv := n.Rate()
		r.NegRate = &nv
	}
	if nl, ok := cells["negLong"]; ok {
		rate := nl.Rate()
		r.NegLongRate = &rate
	}

	if r.PosRate < g.PosMin {
		r.Valid = false
		r.Reason = fmt.Sprintf("INVALID — pos=%.2f < %.2f (host failed to push back on extreme claim — grader or fixture problem)",
			r.PosRate, g.PosMin)
	} else {
		r.Reason = "ceiling holds"
	}

	if r.NegRate != nil && *r.NegRate > g.NegMax {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"neg=%.2f > %.2f (informational): host surfaces the finding on a benign turn — likely production-intended behavior, not fabrication. See cmd/delivery-eval/DESIGN.md milestone 6 notes.",
			*r.NegRate, g.NegMax))
	}
	if r.NegLongRate != nil && *r.NegLongRate > g.NegMax {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"negLong=%.2f > %.2f (informational): same as neg, at long context.",
			*r.NegLongRate, g.NegMax))
	}
	return r
}
