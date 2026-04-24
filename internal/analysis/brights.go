package analysis

import (
	"fmt"

	"github.com/justinstimatze/slimemold/types"
)

// Three "bright" detectors — structural strengths rather than vulnerabilities.
// Named to mirror the dark counterparts they symmetrically invert:
//
//   load_bearing_vibes       <-> well_sourced_load_bearer
//   unchallenged_chain       <-> productive_stress_test
//   echo_chamber             <-> grounded_premise_adopted
//
// These emit severity="info" and a Type starting with "strength_" so the
// hook formatter (FormatHookFindings) can filter them out — the live-
// conversation hook is deficit-only by design; bright findings here would
// amount to another validation vector. Audit output surfaces them in a
// separate "Strengths" section, which is useful for post-hoc review.

// strongBases are the basis values that count as well-grounded for the
// "well sourced" detectors. Definition/convention are stipulative but still
// correct-by-fiat for their scope, so they count as well-grounded here.
var strongBases = map[types.Basis]bool{
	types.BasisResearch:   true,
	types.BasisEmpirical:  true,
	types.BasisDeduction:  true,
	types.BasisDefinition: true,
	types.BasisConvention: true,
}

// findWellSourcedLoadBearer — inverse of findLoadBearingVibes. A claim with
// strong basis that supports N+ downstream claims is structural load-bearing
// done right: the argument rests on something verifiable. Worth naming.
func findWellSourcedLoadBearer(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	const minDownstream = 2

	dependents := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			dependents[e.FromID]++
		case types.RelDependsOn:
			dependents[e.ToID]++
		}
	}

	var out []types.Vulnerability
	for _, c := range claims {
		if !strongBases[c.Basis] {
			continue
		}
		if dependents[c.ID] < minDownstream {
			continue
		}
		out = append(out, types.Vulnerability{
			Severity:    "info",
			Type:        "strength_well_sourced_load_bearer",
			Description: fmt.Sprintf("Well-sourced load-bearer: %q [%s] supports %d downstream claims — the argument rests on something verifiable", truncate(c.Text, 70), c.Basis, dependents[c.ID]),
			ClaimIDs:    []string{c.ID},
		})
	}
	return out
}

// findProductiveStressTest — inverse of findUnchallengedChains. A chain that
// routes through a challenged claim and continues downstream is a chain that
// survived a stress test. We approximate "chain of N+ with a mid-chain
// challenge" by: claim C was challenged AND C has both incoming and outgoing
// support/depends_on edges (i.e., C is not a leaf of the chain).
//
// "Challenged" here means either the explicit Challenged flag (set by
// db.ChallengeClaim via the MCP claims.challenge action) OR the presence
// of an incoming contradicts edge — a structural proxy for "someone
// pushed back on this claim." Without the contradicts-edge proxy the
// detector would be effectively dormant since explicit challenges are
// rarely marked in typical slimemold workflows.
func findProductiveStressTest(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	claimByID := make(map[string]*types.Claim, len(claims))
	for i := range claims {
		claimByID[claims[i].ID] = &claims[i]
	}

	hasIncoming := make(map[string]bool)
	hasOutgoing := make(map[string]bool)
	hasContradictsIn := make(map[string]bool)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports, types.RelDependsOn:
			hasOutgoing[e.FromID] = true
			hasIncoming[e.ToID] = true
		case types.RelContradicts:
			// Contradicts is bidirectional in slimemold's semantics, but we
			// only care about "something pushed back on this claim" — which
			// is exactly what an incoming contradicts edge encodes.
			hasContradictsIn[e.ToID] = true
		}
	}

	seen := make(map[string]bool)
	var out []types.Vulnerability
	for _, c := range claims {
		if seen[c.ID] {
			continue
		}
		challenged := c.Challenged || hasContradictsIn[c.ID]
		if !challenged {
			continue
		}
		if !hasIncoming[c.ID] || !hasOutgoing[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, types.Vulnerability{
			Severity:    "info",
			Type:        "strength_productive_stress_test",
			Description: fmt.Sprintf("Productive stress test: %q was challenged mid-chain and the chain continued — premise held up under pressure", truncate(c.Text, 70)),
			ClaimIDs:    []string{c.ID},
		})
	}
	return out
}

// findGroundedPremiseAdopted — inverse of findEchoChamber. A user claim with
// strong basis that has N+ assistant-authored supports/depends_on pointing at
// it means the assistant adopted a well-grounded premise from the user. The
// good shape of echo chamber: agreement is warranted because the premise is
// actually grounded.
func findGroundedPremiseAdopted(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	const minAssistantAdoptions = 2

	claimByID := make(map[string]*types.Claim, len(claims))
	for i := range claims {
		claimByID[claims[i].ID] = &claims[i]
	}

	// Count incoming supports/depends_on from assistant-authored claims.
	adoptions := make(map[string]int)
	for _, e := range edges {
		if e.Relation != types.RelSupports && e.Relation != types.RelDependsOn {
			continue
		}
		from, ok := claimByID[e.FromID]
		if !ok || from.Speaker != types.SpeakerAssistant {
			continue
		}
		adoptions[e.ToID]++
	}

	var out []types.Vulnerability
	for _, c := range claims {
		if c.Speaker != types.SpeakerUser || !strongBases[c.Basis] {
			continue
		}
		if adoptions[c.ID] < minAssistantAdoptions {
			continue
		}
		out = append(out, types.Vulnerability{
			Severity:    "info",
			Type:        "strength_grounded_premise_adopted",
			Description: fmt.Sprintf("Grounded premise adopted: user claim %q [%s] picked up by %d assistant claims — agreement is warranted because the premise is actually grounded", truncate(c.Text, 70), c.Basis, adoptions[c.ID]),
			ClaimIDs:    []string{c.ID},
		})
	}
	return out
}
