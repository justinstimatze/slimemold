package adapt

import (
	"testing"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/types"
)

func makeKB(id, predType, subject, object string, hasQuote bool) types.KBClaim {
	return types.KBClaim{
		ID:            id,
		PredicateType: predType,
		Subject:       subject,
		Object:        object,
		HasQuote:      hasQuote,
	}
}

func TestAdaptKBClaimsEmpty(t *testing.T) {
	claims, edges := AdaptKBClaims(nil)
	if len(claims) != 0 || len(edges) != 0 {
		t.Errorf("expected empty, got %d claims %d edges", len(claims), len(edges))
	}
}

func TestAdaptKBClaimsSingle(t *testing.T) {
	claims, edges := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory", true),
	})
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for single claim, got %d", len(edges))
	}
}

func TestAdaptKBClaimsTextFormat(t *testing.T) {
	claims, _ := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "TheoryOf", "Darwin", "Evolution", true),
	})
	want := "Darwin --TheoryOf--> Evolution"
	if claims[0].Text != want {
		t.Errorf("text = %q, want %q", claims[0].Text, want)
	}
}

func TestAdaptKBClaimsBasisFromHasQuote(t *testing.T) {
	claims, _ := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "A", "B", true),
		makeKB("2", "Proposes", "C", "D", false),
	})
	if claims[0].Basis != types.BasisResearch || claims[0].Confidence != 0.8 {
		t.Errorf("HasQuote=true: got basis=%s conf=%.1f, want research/0.8", claims[0].Basis, claims[0].Confidence)
	}
	if claims[1].Basis != types.BasisAssumption || claims[1].Confidence != 0.7 {
		t.Errorf("HasQuote=false: got basis=%s conf=%.1f, want assumption/0.7", claims[1].Basis, claims[1].Confidence)
	}
}

func TestAdaptKBClaimsExplicitBasis(t *testing.T) {
	kb := types.KBClaim{
		ID:            "1",
		PredicateType: "Proposes",
		Subject:       "A",
		Object:        "B",
		Basis:         types.BasisEmpirical,
		HasQuote:      true, // should be ignored when Basis is set
	}
	claims, _ := AdaptKBClaims([]types.KBClaim{kb})
	if claims[0].Basis != types.BasisEmpirical {
		t.Errorf("explicit basis ignored: got %s, want empirical", claims[0].Basis)
	}
	if claims[0].Confidence != 0.7 {
		t.Errorf("non-research explicit basis should get 0.7, got %.1f", claims[0].Confidence)
	}
}

func TestAdaptKBClaimsSharedEntity(t *testing.T) {
	claims, edges := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory", true),
		makeKB("2", "Extends", "Bob", "Theory", true),
	})
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge for shared Object, got %d", len(edges))
	}
	if edges[0].Relation != types.RelSupports {
		t.Errorf("expected supports edge, got %s", edges[0].Relation)
	}
}

func TestAdaptKBClaimsChainNotClique(t *testing.T) {
	// 3 claims sharing the same entity → 2 chained edges, not 3 clique edges
	claims, edges := AdaptKBClaims([]types.KBClaim{
		makeKB("a", "Proposes", "X", "Shared", true),
		makeKB("b", "TheoryOf", "Y", "Shared", true),
		makeKB("c", "Extends", "Z", "Shared", true),
	})
	if len(claims) != 3 {
		t.Fatalf("expected 3 claims, got %d", len(claims))
	}
	if len(edges) != 2 {
		t.Errorf("expected 2 chained edges, got %d (would be 3 for clique)", len(edges))
	}
}

func TestAdaptKBClaimsDisputeEdge(t *testing.T) {
	_, edges := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory", true),
		makeKB("2", "Disputes", "Bob", "Theory", false),
	})
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Relation != types.RelContradicts {
		t.Errorf("expected contradicts edge for Disputes, got %s", edges[0].Relation)
	}
}

func TestAdaptKBClaimsNoFalseEdge(t *testing.T) {
	_, edges := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory1", true),
		makeKB("2", "Proposes", "Bob", "Theory2", true),
	})
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for disjoint entities, got %d", len(edges))
	}
}

func TestAdaptKBClaimsEmptySpeaker(t *testing.T) {
	claims, _ := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "A", "B", true),
	})
	if claims[0].Speaker != "" {
		t.Errorf("expected empty speaker for KB claims, got %q", claims[0].Speaker)
	}
}

func TestAdaptKBClaimsProvenanceURL(t *testing.T) {
	kb := types.KBClaim{
		ID:            "1",
		PredicateType: "Proposes",
		Subject:       "A",
		Object:        "B",
		ProvenanceURL: "https://arxiv.org/abs/2301.00001",
	}
	claims, _ := AdaptKBClaims([]types.KBClaim{kb})
	if claims[0].Source != "https://arxiv.org/abs/2301.00001" {
		t.Errorf("source = %q, want arxiv URL", claims[0].Source)
	}
}

func TestAdaptKBClaimsDuplicateID(t *testing.T) {
	claims, _ := AdaptKBClaims([]types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory", true),
		makeKB("1", "Disputes", "Bob", "Theory", false), // duplicate ID, should be skipped
	})
	if len(claims) != 1 {
		t.Errorf("expected 1 claim (duplicate skipped), got %d", len(claims))
	}
	if claims[0].Basis != types.BasisResearch {
		t.Errorf("expected first claim kept, got basis=%s", claims[0].Basis)
	}
}

func TestAdaptKBClaimsEndToEnd(t *testing.T) {
	// Feed adapted KB claims through the full analysis pipeline.
	kbClaims := []types.KBClaim{
		makeKB("1", "Proposes", "Alice", "Theory", false),   // assumption, supports 2 others
		makeKB("2", "Extends", "Bob", "Theory", true),       // research
		makeKB("3", "Extends", "Carol", "Theory", true),     // research
		makeKB("4", "Proposes", "Dave", "Unrelated", false), // orphan
	}

	claims, edges := AdaptKBClaims(kbClaims)
	for i := range claims {
		claims[i].Project = "test"
	}

	topo, vulns := analysis.Analyze(claims, edges, "test")

	if topo.ClaimCount != 4 {
		t.Errorf("expected 4 claims in topology, got %d", topo.ClaimCount)
	}

	// Claim 4 should be an orphan (no shared entities with others).
	if len(topo.Orphans) != 1 || topo.Orphans[0].ID != "4" {
		t.Errorf("expected claim 4 as orphan, got %v", topo.Orphans)
	}

	// Should have at least one vulnerability (orphan warning for claim 4).
	if len(vulns.Items) == 0 {
		t.Error("expected at least one vulnerability from analysis")
	}

	foundOrphan := false
	for _, v := range vulns.Items {
		if v.Type == "orphan" {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Error("expected orphan vulnerability for disconnected claim")
	}
}
