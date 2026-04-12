package adapt

import (
	"fmt"
	"sort"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// predicateRelation maps KB predicate types to slimemold edge relations.
var predicateRelation = map[string]types.Relation{
	"Disputes":    types.RelContradicts,
	"DisputesOrg": types.RelContradicts,
	"DependsOn":   types.RelDependsOn,
	"Requires":    types.RelDependsOn,
	"Proposes":    types.RelSupports,
	"TheoryOf":    types.RelSupports,
	"Supports":    types.RelSupports,
	"Extends":     types.RelSupports,
}

// AdaptKBClaims converts typed knowledge base claims into slimemold Claims and Edges.
// Pure function: no DB access, no side effects.
func AdaptKBClaims(kbClaims []types.KBClaim) ([]types.Claim, []types.Edge) {
	if len(kbClaims) == 0 {
		return nil, nil
	}

	claims := make([]types.Claim, 0, len(kbClaims))
	kbByID := make(map[string]*types.KBClaim, len(kbClaims))

	for i := range kbClaims {
		kb := &kbClaims[i]
		if _, dup := kbByID[kb.ID]; dup {
			continue // skip duplicate IDs
		}
		kbByID[kb.ID] = kb

		basis, confidence := deriveBasis(kb)

		claims = append(claims, types.Claim{
			ID:         kb.ID,
			Text:       fmt.Sprintf("%s --%s--> %s", kb.Subject, kb.PredicateType, kb.Object),
			Basis:      basis,
			Confidence: confidence,
			Source:     kb.ProvenanceURL,
			SessionID:  "kb-import",
			CreatedAt:  time.Now(),
		})
	}

	// Build entity → claim ID index for co-occurrence edges.
	entityClaims := make(map[string][]string) // entity name → sorted claim IDs
	for _, kb := range kbClaims {
		entityClaims[kb.Subject] = append(entityClaims[kb.Subject], kb.ID)
		if kb.Object != kb.Subject {
			entityClaims[kb.Object] = append(entityClaims[kb.Object], kb.ID)
		}
	}

	// Chain edges between claims sharing an entity (sorted by ID for determinism).
	// Chain keeps edge count linear: n claims sharing entity → n-1 edges.
	seen := make(map[[2]string]bool)
	var edges []types.Edge

	for _, claimIDs := range entityClaims {
		if len(claimIDs) < 2 {
			continue
		}
		sort.Strings(claimIDs)
		// Dedup within this entity bucket (a claim can appear via both Subject and Object).
		unique := claimIDs[:0:0]
		for i, id := range claimIDs {
			if i == 0 || id != claimIDs[i-1] {
				unique = append(unique, id)
			}
		}
		if len(unique) < 2 {
			continue
		}

		for i := 0; i < len(unique)-1; i++ {
			fromID, toID := unique[i], unique[i+1]
			key := [2]string{fromID, toID}
			if seen[key] {
				continue
			}
			seen[key] = true

			rel := edgeRelation(kbByID[fromID], kbByID[toID])
			edges = append(edges, types.Edge{
				ID:       fmt.Sprintf("kb-%s-%s", fromID, toID),
				FromID:   fromID,
				ToID:     toID,
				Relation: rel,
				Strength: 0.5,
			})
		}
	}

	return claims, edges
}

// deriveBasis determines Basis and Confidence for a KBClaim.
// If the KBClaim has an explicit Basis, use it. Otherwise derive from HasQuote.
func deriveBasis(kb *types.KBClaim) (types.Basis, float64) {
	if kb.Basis != "" {
		conf := 0.7
		if kb.Basis == types.BasisResearch {
			conf = 0.8
		}
		return kb.Basis, conf
	}
	if kb.HasQuote {
		return types.BasisResearch, 0.8
	}
	return types.BasisAssumption, 0.7
}

// edgeRelation determines the edge relation between two co-occurring KB claims.
// If either claim has a contradiction predicate, the edge is RelContradicts.
// Otherwise use the predicate mapping, defaulting to RelSupports.
func edgeRelation(a, b *types.KBClaim) types.Relation {
	if relationFor(a.PredicateType) == types.RelContradicts ||
		relationFor(b.PredicateType) == types.RelContradicts {
		return types.RelContradicts
	}
	return types.RelSupports
}

// relationFor returns the mapped relation for a predicate type, defaulting to RelSupports.
func relationFor(predicateType string) types.Relation {
	if rel, ok := predicateRelation[predicateType]; ok {
		return rel
	}
	return types.RelSupports
}
