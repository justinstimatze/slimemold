package mcp

import (
	"fmt"
	"testing"

	"github.com/justinstimatze/slimemold/types"
)

func TestSelectRelevantClaimsCapsHubs(t *testing.T) {
	var claims []types.Claim
	var edges []types.Edge
	for i := 0; i < 2000; i++ {
		claims = append(claims, types.Claim{ID: fmt.Sprintf("c%d", i), Text: fmt.Sprintf("claim %d", i)})
	}
	// every claim in the first 1500 is a hub (3 dependents each)
	for i := 0; i < 1500; i++ {
		for j := 0; j < 3; j++ {
			edges = append(edges, types.Edge{FromID: fmt.Sprintf("c%d", 1500+j), ToID: fmt.Sprintf("c%d", i), Relation: types.RelDependsOn})
		}
	}
	got := selectRelevantClaims(claims, edges, "")
	if len(got) > 100 {
		t.Fatalf("selected %d claims, want <= 100", len(got))
	}
	if len(got) < 50 {
		t.Fatalf("selected %d claims, want recent 50 retained", len(got))
	}
}
