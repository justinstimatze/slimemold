package analysis

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

func makeClaim(id, text string, basis types.Basis) types.Claim {
	return types.Claim{ID: id, Text: text, Basis: basis, Project: "test"}
}

func makeClaimWithConfidence(id, text string, basis types.Basis, confidence float64) types.Claim {
	return types.Claim{ID: id, Text: text, Basis: basis, Confidence: confidence, Project: "test"}
}

func makeClaimInSession(id, text string, basis types.Basis, sessionID string, createdAt time.Time) types.Claim {
	return types.Claim{ID: id, Text: text, Basis: basis, SessionID: sessionID, CreatedAt: createdAt, Project: "test"}
}

func makeEdge(from, to string, rel types.Relation) types.Edge {
	return types.Edge{ID: from + "-" + to, FromID: from, ToID: to, Relation: rel}
}

func TestAnalyzeEmpty(t *testing.T) {
	topo, vulns := Analyze(nil, nil, "empty")
	if topo.ClaimCount != 0 {
		t.Errorf("claims = %d, want 0", topo.ClaimCount)
	}
	if len(vulns.Items) != 0 {
		t.Errorf("vulns = %d, want 0", len(vulns.Items))
	}
}

func TestOrphanDetection(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "connected", types.BasisResearch),
		makeClaim("b", "connected too", types.BasisResearch),
		makeClaim("c", "orphan", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelSupports),
	}

	topo, _ := Analyze(claims, edges, "test")
	if len(topo.Orphans) != 1 {
		t.Fatalf("orphans = %d, want 1", len(topo.Orphans))
	}
	if topo.Orphans[0].ID != "c" {
		t.Errorf("orphan = %s, want c", topo.Orphans[0].ID)
	}
}

func TestLoadBearingVibes(t *testing.T) {
	claims := []types.Claim{
		makeClaim("vibes1", "just a feeling", types.BasisVibes),
		makeClaim("dep1", "depends on vibes", types.BasisDeduction),
		makeClaim("dep2", "also depends on vibes", types.BasisDeduction),
	}
	edges := []types.Edge{
		makeEdge("vibes1", "dep1", types.RelSupports),
		makeEdge("vibes1", "dep2", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" {
			found = true
			if v.Severity != "critical" {
				t.Errorf("severity = %s, want critical", v.Severity)
			}
			if len(v.ClaimIDs) != 1 || v.ClaimIDs[0] != "vibes1" {
				t.Errorf("claim_ids = %v, want [vibes1]", v.ClaimIDs)
			}
		}
	}
	if !found {
		t.Error("expected load_bearing_vibes vulnerability")
	}
}

func TestNoLoadBearingVibesForResearch(t *testing.T) {
	claims := []types.Claim{
		makeClaim("solid", "well-sourced claim", types.BasisResearch),
		makeClaim("dep1", "depends", types.BasisDeduction),
		makeClaim("dep2", "also depends", types.BasisDeduction),
	}
	edges := []types.Edge{
		makeEdge("solid", "dep1", types.RelSupports),
		makeEdge("solid", "dep2", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" {
			t.Errorf("unexpected load_bearing_vibes for research-basis claim")
		}
	}
}

func TestUnchallengedChain(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "first", types.BasisVibes),
		makeClaim("b", "second", types.BasisVibes),
		makeClaim("c", "third", types.BasisVibes),
		makeClaim("d", "fourth", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelDependsOn),
		makeEdge("b", "c", types.RelDependsOn),
		makeEdge("c", "d", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "unchallenged_chain" {
			found = true
			if len(v.ClaimIDs) < 3 {
				t.Errorf("chain length = %d, want >= 3", len(v.ClaimIDs))
			}
		}
	}
	if !found {
		t.Error("expected unchallenged_chain vulnerability")
	}
}

func TestChallengedClaimBreaksChain(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "first", types.BasisVibes),
		{ID: "b", Text: "second (challenged)", Basis: types.BasisVibes, Challenged: true, Project: "test"},
		makeClaim("c", "third", types.BasisVibes),
		makeClaim("d", "fourth", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelDependsOn),
		makeEdge("b", "c", types.RelDependsOn),
		makeEdge("c", "d", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "unchallenged_chain" && len(v.ClaimIDs) >= 4 {
			t.Errorf("chain should be broken by challenged claim, got length %d", len(v.ClaimIDs))
		}
	}
}

func TestClusterDetection(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a1", "cluster A first", types.BasisResearch),
		makeClaim("a2", "cluster A second", types.BasisResearch),
		makeClaim("b1", "cluster B first", types.BasisVibes),
		makeClaim("b2", "cluster B second", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("a1", "a2", types.RelSupports),
		makeEdge("b1", "b2", types.RelSupports),
	}

	topo, _ := Analyze(claims, edges, "test")
	if len(topo.Clusters) != 2 {
		t.Errorf("clusters = %d, want 2", len(topo.Clusters))
	}
}

func TestBasisCounts(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "research claim", types.BasisResearch),
		makeClaim("b", "another research", types.BasisResearch),
		makeClaim("c", "vibes claim", types.BasisVibes),
	}

	topo, _ := Analyze(claims, nil, "test")
	if topo.BasisCounts[types.BasisResearch] != 2 {
		t.Errorf("research = %d, want 2", topo.BasisCounts[types.BasisResearch])
	}
	if topo.BasisCounts[types.BasisVibes] != 1 {
		t.Errorf("vibes = %d, want 1", topo.BasisCounts[types.BasisVibes])
	}
}

func TestMaxDepth(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "root", types.BasisResearch),
		makeClaim("b", "mid", types.BasisResearch),
		makeClaim("c", "leaf", types.BasisResearch),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelSupports),
		makeEdge("b", "c", types.RelSupports),
	}

	topo, _ := Analyze(claims, edges, "test")
	if topo.MaxDepth != 3 {
		t.Errorf("max_depth = %d, want 3", topo.MaxDepth)
	}
}

func TestBottleneckDetection(t *testing.T) {
	// Star topology: center connected to 8+ outer nodes (above min threshold of 8)
	claims := []types.Claim{
		makeClaim("center", "bottleneck node", types.BasisAnalogy),
		makeClaim("n1", "node 1", types.BasisResearch),
		makeClaim("n2", "node 2", types.BasisResearch),
		makeClaim("n3", "node 3", types.BasisResearch),
		makeClaim("n4", "node 4", types.BasisResearch),
		makeClaim("n5", "node 5", types.BasisResearch),
		makeClaim("n6", "node 6", types.BasisResearch),
		makeClaim("n7", "node 7", types.BasisResearch),
		makeClaim("n8", "node 8", types.BasisResearch),
	}
	edges := []types.Edge{
		makeEdge("n1", "center", types.RelSupports),
		makeEdge("n2", "center", types.RelSupports),
		makeEdge("n3", "center", types.RelSupports),
		makeEdge("center", "n4", types.RelSupports),
		makeEdge("center", "n5", types.RelSupports),
		makeEdge("center", "n6", types.RelSupports),
		makeEdge("n7", "n1", types.RelSupports),
		makeEdge("n8", "n2", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "bottleneck" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "center" {
			found = true
		}
	}
	if !found {
		t.Error("expected bottleneck vulnerability for center node")
	}
}

func TestSmallGraphNoBottleneckSpam(t *testing.T) {
	// 5 claims — below the min threshold of 8, should produce zero bottleneck findings
	claims := []types.Claim{
		makeClaim("a", "claim a", types.BasisVibes),
		makeClaim("b", "claim b", types.BasisVibes),
		makeClaim("c", "claim c", types.BasisVibes),
		makeClaim("d", "claim d", types.BasisVibes),
		makeClaim("e", "claim e", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelSupports),
		makeEdge("b", "c", types.RelSupports),
		makeEdge("c", "d", types.RelSupports),
		makeEdge("d", "e", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "bottleneck" {
			t.Errorf("unexpected bottleneck finding on small graph: %s", v.Description)
		}
	}
}

func TestFormatAuditSummary(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "research claim", types.BasisResearch),
		makeClaim("b", "vibes claim", types.BasisVibes),
	}

	topoPtr, _ := Analyze(claims, nil, "test")
	topo := *topoPtr
	vulns := &types.Vulnerabilities{
		Project: "test",
		Items: []types.Vulnerability{
			{Severity: "critical", Type: "load_bearing_vibes", Description: "test critical"},
			{Severity: "warning", Type: "orphan", Description: "test warning"},
		},
		CriticalCount: 1,
		WarningCount:  1,
	}

	summary := FormatAuditSummary(&topo, vulns)
	if !strings.Contains(summary, "SLIMEMOLD TOPOLOGY AUDIT") {
		t.Error("missing header")
	}
	if !strings.Contains(summary, "CRITICAL") {
		t.Error("missing CRITICAL")
	}
	if !strings.Contains(summary, "WARNING") {
		t.Error("missing WARNING")
	}
}

func TestFluencyTrap(t *testing.T) {
	claims := []types.Claim{
		makeClaimWithConfidence("vibes-high", "feels really true", types.BasisVibes, 0.9),
		makeClaimWithConfidence("research-high", "well-sourced fact", types.BasisResearch, 0.9),
		{ID: "vibes-challenged", Text: "challenged vibes", Basis: types.BasisVibes, Confidence: 0.9, Challenged: true, Project: "test"},
		makeClaim("dep1", "depends on vibes-high", types.BasisDeduction),
	}
	// vibes-high needs a dependent to have structural importance
	edges := []types.Edge{
		{FromID: "dep1", ToID: "vibes-high", Relation: types.RelDependsOn},
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "fluency_trap" {
			if v.ClaimIDs[0] == "research-high" {
				t.Error("research claim at 0.9 should NOT trigger fluency trap")
			}
			if v.ClaimIDs[0] == "vibes-challenged" {
				t.Error("challenged claim should NOT trigger fluency trap")
			}
			if v.ClaimIDs[0] == "vibes-high" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected fluency_trap for vibes claim at confidence 0.9")
	}
}

func TestFluencyTrapModerate(t *testing.T) {
	claims := []types.Claim{
		makeClaimWithConfidence("analogy-high", "strong analogy", types.BasisAnalogy, 0.9),
		makeClaimWithConfidence("analogy-ok", "moderate analogy", types.BasisAnalogy, 0.75),
		makeClaim("dep-ah", "depends on analogy-high", types.BasisDeduction),
		makeClaim("dep-ao", "depends on analogy-ok", types.BasisDeduction),
	}
	// Both need structural importance (dependents)
	edges := []types.Edge{
		{FromID: "dep-ah", ToID: "analogy-high", Relation: types.RelDependsOn},
		{FromID: "dep-ao", ToID: "analogy-ok", Relation: types.RelDependsOn},
	}

	_, vulns := Analyze(claims, edges, "test")

	var highFlagged, okFlagged bool
	for _, v := range vulns.Items {
		if v.Type == "fluency_trap" {
			if v.ClaimIDs[0] == "analogy-high" {
				highFlagged = true
			}
			if v.ClaimIDs[0] == "analogy-ok" {
				okFlagged = true
			}
		}
	}
	if !highFlagged {
		t.Error("analogy at 0.9 should trigger fluency trap (ceiling 0.7)")
	}
	if !okFlagged {
		t.Error("analogy at 0.75 should trigger fluency trap (ceiling 0.7, no buffer)")
	}
}

func TestCoverageImbalance(t *testing.T) {
	// Cluster A: big, lots of internal edges, but no claim is depended on (rabbit hole)
	// Cluster B: small, few edges, but every claim supports others (high importance)
	claims := []types.Claim{
		makeClaim("a1", "cluster A first", types.BasisDeduction),
		makeClaim("a2", "cluster A second", types.BasisDeduction),
		makeClaim("a3", "cluster A third", types.BasisDeduction),
		makeClaim("a4", "cluster A fourth", types.BasisDeduction),
		makeClaim("a5", "cluster A fifth", types.BasisDeduction),
		makeClaim("a6", "cluster A sixth", types.BasisDeduction),
		makeClaim("a7", "cluster A seventh", types.BasisDeduction),
		makeClaim("a8", "cluster A eighth", types.BasisDeduction),
		makeClaim("b1", "cluster B first", types.BasisResearch),
		makeClaim("b2", "cluster B second", types.BasisResearch),
		makeClaim("b3", "cluster B third", types.BasisResearch),
	}
	edges := []types.Edge{
		// Cluster A: many edges (high attention) but only contradicts (low importance)
		makeEdge("a1", "a2", types.RelContradicts),
		makeEdge("a2", "a3", types.RelContradicts),
		makeEdge("a3", "a4", types.RelContradicts),
		makeEdge("a4", "a5", types.RelContradicts),
		makeEdge("a5", "a6", types.RelContradicts),
		makeEdge("a6", "a7", types.RelContradicts),
		makeEdge("a7", "a8", types.RelContradicts),
		makeEdge("a1", "a5", types.RelContradicts),
		makeEdge("a2", "a6", types.RelContradicts),
		makeEdge("a3", "a7", types.RelContradicts),
		// Cluster B: minimal edges but high directed importance
		makeEdge("b1", "b2", types.RelSupports),
		makeEdge("b1", "b3", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	var rabbitHole, neglected bool
	for _, v := range vulns.Items {
		if v.Type == "coverage_imbalance" {
			if strings.Contains(v.Description, "Rabbit hole") {
				rabbitHole = true
			}
			if strings.Contains(v.Description, "Neglected foundation") {
				neglected = true
			}
		}
	}
	if !rabbitHole {
		t.Error("expected rabbit_hole finding for cluster A (high attention, low importance)")
	}
	if !neglected {
		t.Error("expected neglected_foundation finding for cluster B (high importance, low attention)")
	}
}

func TestCoverageImbalanceTooFewClusters(t *testing.T) {
	claims := []types.Claim{
		makeClaim("a", "only cluster", types.BasisResearch),
		makeClaim("b", "also only cluster", types.BasisResearch),
		makeClaim("c", "still only cluster", types.BasisResearch),
	}
	edges := []types.Edge{
		makeEdge("a", "b", types.RelSupports),
		makeEdge("b", "c", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "coverage_imbalance" {
			t.Error("should not flag coverage_imbalance with only one cluster")
		}
	}
}

func TestAbandonedCluster(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-2 * time.Hour)

	claims := []types.Claim{
		// Cluster A: only in session 1 (abandoned)
		makeClaimInSession("a1", "old topic first", types.BasisResearch, "session-1", earlier),
		makeClaimInSession("a2", "old topic second", types.BasisResearch, "session-1", earlier),
		// Cluster B: in both sessions (active)
		makeClaimInSession("b1", "active topic first", types.BasisResearch, "session-1", earlier),
		makeClaimInSession("b2", "active topic second", types.BasisResearch, "session-2", now),
	}
	edges := []types.Edge{
		makeEdge("a1", "a2", types.RelSupports),
		makeEdge("b1", "b2", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "abandoned_topic" {
			if strings.Contains(v.Description, "old topic") {
				found = true
			}
			if strings.Contains(v.Description, "active topic") {
				t.Error("active cluster should NOT be flagged as abandoned")
			}
		}
	}
	if !found {
		t.Error("expected abandoned_topic for cluster with only session-1 claims")
	}
}

func TestAbandonedSingleSession(t *testing.T) {
	now := time.Now()
	claims := []types.Claim{
		makeClaimInSession("a1", "claim one", types.BasisResearch, "session-1", now),
		makeClaimInSession("a2", "claim two", types.BasisResearch, "session-1", now),
		makeClaimInSession("b1", "claim three", types.BasisResearch, "session-1", now),
		makeClaimInSession("b2", "claim four", types.BasisResearch, "session-1", now),
	}
	edges := []types.Edge{
		makeEdge("a1", "a2", types.RelSupports),
		makeEdge("b1", "b2", types.RelSupports),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "abandoned_topic" {
			t.Error("should not flag abandonment with only one session")
		}
	}
}

func TestEchoChamber(t *testing.T) {
	// Assistant supports user vibes claims, never contradicts
	// Need 10+ from each speaker to trigger Pattern 1
	claims := make([]types.Claim, 0, 24)
	for i := 0; i < 12; i++ {
		claims = append(claims, types.Claim{
			ID: fmt.Sprintf("user-%d", i), Text: fmt.Sprintf("user vibes claim %d", i),
			Basis: types.BasisVibes, Speaker: types.SpeakerUser, Project: "test",
		})
		claims = append(claims, types.Claim{
			ID: fmt.Sprintf("asst-%d", i), Text: fmt.Sprintf("assistant agrees %d", i),
			Basis: types.BasisLLMOutput, Speaker: types.SpeakerAssistant, Project: "test",
		})
	}
	var edges []types.Edge
	for i := 0; i < 12; i++ {
		edges = append(edges, makeEdge(fmt.Sprintf("asst-%d", i), fmt.Sprintf("user-%d", i), types.RelSupports))
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "echo_chamber" {
			found = true
		}
	}
	if !found {
		t.Error("expected echo_chamber finding when assistant only supports user vibes")
	}
}

func TestEchoChamberWithContradiction(t *testing.T) {
	// Same as above but assistant contradicts at least once — should NOT trigger
	claims := make([]types.Claim, 0, 24)
	for i := 0; i < 12; i++ {
		claims = append(claims, types.Claim{
			ID: fmt.Sprintf("user-%d", i), Text: fmt.Sprintf("user vibes claim %d", i),
			Basis: types.BasisVibes, Speaker: types.SpeakerUser, Project: "test",
		})
		claims = append(claims, types.Claim{
			ID: fmt.Sprintf("asst-%d", i), Text: fmt.Sprintf("assistant response %d", i),
			Basis: types.BasisLLMOutput, Speaker: types.SpeakerAssistant, Project: "test",
		})
	}
	var edges []types.Edge
	for i := 0; i < 11; i++ {
		edges = append(edges, makeEdge(fmt.Sprintf("asst-%d", i), fmt.Sprintf("user-%d", i), types.RelSupports))
	}
	// One contradiction
	edges = append(edges, makeEdge("asst-11", "user-11", types.RelContradicts))

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "echo_chamber" && strings.Contains(v.Description, "never contradicts") {
			t.Error("should not flag zero-pushback echo chamber when assistant contradicts at least once")
		}
	}
}
