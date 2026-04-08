package viz

import (
	"strings"
	"testing"

	"github.com/justinstimatze/slimemold/types"
)

func TestRenderASCIIEmpty(t *testing.T) {
	topo := &types.Topology{Project: "test", ClaimCount: 0}
	vulns := &types.Vulnerabilities{Project: "test"}

	out := RenderASCII(topo, vulns)
	if !strings.Contains(out, "empty graph") {
		t.Error("expected empty graph message")
	}
}

func TestRenderASCIIWithClusters(t *testing.T) {
	topo := &types.Topology{
		Project:    "test",
		ClaimCount: 3,
		EdgeCount:  2,
		MaxDepth:   2,
		BasisCounts: map[types.Basis]int{
			types.BasisResearch: 2,
			types.BasisVibes:    1,
		},
		Clusters: []types.ClusterInfo{
			{
				ID:    0,
				Label: "test cluster",
				Claims: []types.Claim{
					{ID: "a", Text: "research claim", Basis: types.BasisResearch, Source: "Smith 2024"},
					{ID: "b", Text: "vibes claim", Basis: types.BasisVibes},
				},
				Density: 0.75,
				Edges:   2,
			},
		},
		Orphans: []types.Claim{
			{ID: "c", Text: "orphan claim", Basis: types.BasisAnalogy},
		},
	}
	vulns := &types.Vulnerabilities{
		Project: "test",
		Items: []types.Vulnerability{
			{Severity: "critical", Type: "load_bearing_vibes", Description: "vibes claim supports 3", ClaimIDs: []string{"b"}},
		},
		CriticalCount: 1,
	}

	out := RenderASCII(topo, vulns)

	checks := []string{
		"SLIMEMOLD TOPOLOGY",
		"3 claims, 2 edges",
		"research",
		"vibes",
		"test cluster",
		"Smith 2024",
		"LOAD-BEARING",
		"Orphans",
		"orphan claim",
		"● = research/empirical",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderDOT(t *testing.T) {
	claims := []types.Claim{
		{ID: "a", Text: "claim A", Basis: types.BasisResearch},
		{ID: "b", Text: "claim B", Basis: types.BasisVibes},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "a", ToID: "b", Relation: types.RelSupports},
		{ID: "e2", FromID: "b", ToID: "a", Relation: types.RelContradicts},
	}

	out := RenderDOT(claims, edges, "test")

	if !strings.Contains(out, "digraph") {
		t.Error("missing digraph")
	}
	if !strings.Contains(out, "claim A") {
		t.Error("missing claim A label")
	}
	if !strings.Contains(out, "style=dashed") {
		t.Error("contradicts edge should be dashed")
	}
	if !strings.Contains(out, "#d4edda") {
		t.Error("research should have green fill")
	}
	if !strings.Contains(out, "#f8d7da") {
		t.Error("vibes should have red fill")
	}
}

func TestBasisIcons(t *testing.T) {
	cases := []struct {
		basis types.Basis
		icon  string
	}{
		{types.BasisResearch, "●"},
		{types.BasisEmpirical, "●"},
		{types.BasisAnalogy, "△"},
		{types.BasisVibes, "○"},
		{types.BasisLLMOutput, "○"},
		{types.BasisDeduction, "◆"},
	}
	for _, tc := range cases {
		got := basisIcon(tc.basis)
		if got != tc.icon {
			t.Errorf("basisIcon(%s) = %s, want %s", tc.basis, got, tc.icon)
		}
	}
}
