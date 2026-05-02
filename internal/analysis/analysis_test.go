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

func TestPrematureClosureBothSignals(t *testing.T) {
	// A thought-terminating cliche (flagged by LLM) capping weak upstream claims
	claims := []types.Claim{
		makeClaim("vibes1", "language models amplify fluency", types.BasisVibes),
		makeClaim("deduction1", "therefore all AI output is suspect", types.BasisDeduction),
		{ID: "closure1", Text: "it's turtles all the way down", Basis: types.BasisVibes,
			Project: "test", TerminatesInquiry: true},
	}
	edges := []types.Edge{
		makeEdge("deduction1", "vibes1", types.RelDependsOn),
		makeEdge("closure1", "deduction1", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "premature_closure" {
			found = true
			if v.Severity != "warning" {
				t.Errorf("severity = %s, want warning (both signals present)", v.Severity)
			}
			if !strings.Contains(v.Description, "thought-terminating") {
				t.Error("description should mention thought-terminating cliche")
			}
			if !strings.Contains(v.Description, "unverified") {
				t.Error("description should mention unverified upstream")
			}
		}
	}
	if !found {
		t.Error("expected premature_closure vulnerability")
	}
}

func TestPrematureClosureLLMOnlyIsInfo(t *testing.T) {
	// LLM flags terminates_inquiry but upstream is all research — info level only
	claims := []types.Claim{
		makeClaim("research1", "Reber & Schwarz 1999 showed fluency affects truth judgments", types.BasisResearch),
		makeClaim("deduction1", "therefore fluency is measurable", types.BasisDeduction),
		{ID: "closure1", Text: "it is what it is", Basis: types.BasisVibes,
			Project: "test", TerminatesInquiry: true},
	}
	edges := []types.Edge{
		makeEdge("deduction1", "research1", types.RelDependsOn),
		makeEdge("closure1", "deduction1", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "premature_closure" {
			found = true
			if v.Severity != "info" {
				t.Errorf("severity = %s, want info (LLM signal only, strong upstream)", v.Severity)
			}
		}
	}
	if !found {
		t.Error("expected premature_closure vulnerability (info level)")
	}
}

func TestPrematureClosureUpstreamOnlyIsInfo(t *testing.T) {
	// Not flagged by LLM, but it's a weak-basis leaf capping weak upstream — info level
	claims := []types.Claim{
		makeClaim("vibes1", "AI makes people dumber", types.BasisVibes),
		makeClaim("middle1", "this affects everything", types.BasisDeduction),
		makeClaim("leaf1", "so we should be careful", types.BasisVibes),
	}
	edges := []types.Edge{
		makeEdge("middle1", "vibes1", types.RelDependsOn),
		makeEdge("leaf1", "middle1", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	var found bool
	for _, v := range vulns.Items {
		if v.Type == "premature_closure" && strings.Contains(v.Description, "so we should be careful") {
			found = true
			if v.Severity != "info" {
				t.Errorf("severity = %s, want info (upstream signal only)", v.Severity)
			}
		}
	}
	if !found {
		t.Error("expected premature_closure vulnerability for weak-upstream leaf")
	}
}

func TestNoPrematureClosureForDeductionLeaf(t *testing.T) {
	// A deduction leaf capping weak upstream without LLM flag — should NOT trigger
	// (normal reasoning, not a thought-terminating cliche)
	claims := []types.Claim{
		makeClaim("vibes1", "AI makes people dumber", types.BasisVibes),
		makeClaim("middle1", "this affects everything", types.BasisDeduction),
		makeClaim("leaf1", "therefore we should restructure", types.BasisDeduction),
	}
	edges := []types.Edge{
		makeEdge("middle1", "vibes1", types.RelDependsOn),
		makeEdge("leaf1", "middle1", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "premature_closure" && strings.Contains(v.Description, "therefore we should restructure") {
			t.Error("should not flag deduction leaf as premature closure without LLM flag")
		}
	}
}

func TestNoPrematureClosureForResolvedLeaf(t *testing.T) {
	// A leaf with strong upstream and no LLM flag — should NOT trigger
	claims := []types.Claim{
		makeClaim("research1", "well-sourced finding", types.BasisResearch),
		makeClaim("research2", "another sourced finding", types.BasisResearch),
		makeClaim("conclusion1", "therefore X follows", types.BasisDeduction),
	}
	edges := []types.Edge{
		makeEdge("conclusion1", "research1", types.RelDependsOn),
		makeEdge("conclusion1", "research2", types.RelDependsOn),
	}

	_, vulns := Analyze(claims, edges, "test")

	for _, v := range vulns.Items {
		if v.Type == "premature_closure" && strings.Contains(v.Description, "therefore X follows") {
			t.Error("should not flag a legitimate conclusion with strong upstream as premature closure")
		}
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

// TestFormatHookFindings_Cooldown verifies that FormatHookFindings skips a
// priority-eligible finding when its (claim_id, finding_type) appears in the
// recentFires set — end-to-end integration of the cooldown filter.
func TestFormatHookFindings_Cooldown(t *testing.T) {
	// Graph size must exceed HookColdStartMinClaims or the hook short-
	// circuits before any filtering logic runs.
	anchorID := "claim-anchor"
	downstreamID := "claim-downstream"
	claims := []types.Claim{
		{ID: anchorID, Text: "load-bearing vibes anchor", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: downstreamID, Text: "downstream A", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "claim-c", Text: "downstream B", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "claim-d", Text: "filler D", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "claim-e", Text: "filler E", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "claim-f", Text: "filler F", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
	}
	edges := []types.Edge{
		{FromID: downstreamID, ToID: anchorID, Relation: types.RelDependsOn},
		{FromID: "claim-c", ToID: anchorID, Relation: types.RelDependsOn},
	}

	topo, vulns := Analyze(claims, edges, "test")
	// Sanity: we should have produced a load-bearing vibes finding on anchorID.
	foundAnchor := false
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == anchorID {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatal("setup: expected load_bearing_vibes on anchorID")
	}

	// Without cooldown, FormatHookFindings picks anchorID.
	summary, pickedID, pickedType, _ := FormatHookFindings(topo, vulns, claims, nil, 0, 0, 5)
	if summary == "" {
		t.Fatal("expected non-empty summary without cooldown")
	}
	if pickedID != anchorID || pickedType != "load_bearing_vibes" {
		t.Errorf("without cooldown: picked (%q, %q), want (%q, load_bearing_vibes)", pickedID, pickedType, anchorID)
	}

	// With cooldown set on that exact (claim, type), we should either pick
	// something else or emit nothing — but definitely not pick anchorID again.
	recent := map[string]time.Time{anchorID + "|load_bearing_vibes": time.Now()}
	_, pickedID2, pickedType2, _ := FormatHookFindings(topo, vulns, claims, recent, 0, 0, 5)
	if pickedID2 == anchorID && pickedType2 == "load_bearing_vibes" {
		t.Error("cooldown failed: picked the suppressed finding")
	}
}

// TestLoadBearingVibes_AncientDependentsDoNotCount verifies that a vibes
// anchor whose only dependents were created outside HookConversationalWindow
// does NOT fire as load-bearing. Conversationally load-bearing means recent
// claims rest on it, not that any claims ever rested on it.
func TestLoadBearingVibes_AncientDependentsDoNotCount(t *testing.T) {
	ancient := time.Now().Add(-2 * HookConversationalWindow)
	claims := []types.Claim{
		{ID: "anchor", Text: "old vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
		{ID: "d1", Text: "ancient dep 1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: ancient},
		{ID: "d2", Text: "ancient dep 2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: ancient},
	}
	edges := []types.Edge{
		{FromID: "anchor", ToID: "d1", Relation: types.RelSupports},
		{FromID: "anchor", ToID: "d2", Relation: types.RelSupports},
	}
	_, vulns := Analyze(claims, edges, "test")
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			t.Error("anchor with only ancient dependents should not fire as load-bearing")
		}
	}
}

// TestLoadBearingVibes_RecentDependentsCount verifies that a vibes anchor
// with recent dependents does fire as load-bearing, even if the anchor itself
// is old. The anchor's age is checked separately at the priority-selection
// step (HookMaxClaimAge); the load-bearing detection is about whether recent
// claims rest on the anchor.
func TestLoadBearingVibes_RecentDependentsCount(t *testing.T) {
	now := time.Now()
	claims := []types.Claim{
		{ID: "anchor", Text: "older vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: now.Add(-30 * time.Minute)},
		{ID: "d1", Text: "recent dep 1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "d2", Text: "recent dep 2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
	}
	edges := []types.Edge{
		{FromID: "anchor", ToID: "d1", Relation: types.RelSupports},
		{FromID: "anchor", ToID: "d2", Relation: types.RelSupports},
	}
	_, vulns := Analyze(claims, edges, "test")
	var found bool
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			found = true
		}
	}
	if !found {
		t.Error("anchor with 2 recent dependents should fire as load-bearing")
	}
}

// TestLoadBearingVibes_MixedDependentsCountsOnlyRecent verifies the count is
// strictly limited to recent dependents — an anchor with 1 ancient + 1 recent
// dependent has degree 1 (below the 2-threshold) and does not fire.
func TestLoadBearingVibes_MixedDependentsCountsOnlyRecent(t *testing.T) {
	now := time.Now()
	ancient := now.Add(-2 * HookConversationalWindow)
	claims := []types.Claim{
		{ID: "anchor", Text: "vibes anchor", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "old-dep", Text: "ancient dep", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: ancient},
		{ID: "new-dep", Text: "recent dep", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
	}
	edges := []types.Edge{
		{FromID: "anchor", ToID: "old-dep", Relation: types.RelSupports},
		{FromID: "anchor", ToID: "new-dep", Relation: types.RelSupports},
	}
	_, vulns := Analyze(claims, edges, "test")
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			t.Error("anchor with only 1 recent dependent (out of 2 total) should not fire — recent count below threshold")
		}
	}
}

// TestLoadBearingVibes_PersistentWeightFiresWithoutRecent verifies that an
// anchor with enough total dependents (>= LoadBearingPersistentThreshold)
// fires even when none of those dependents are recent. The persistent-weight
// branch surfaces genuinely foundational claims that have gone dormant —
// "something we said two weeks ago that's still underpinning everything"
// should keep coming up even without fresh activity.
func TestLoadBearingVibes_PersistentWeightFiresWithoutRecent(t *testing.T) {
	ancient := time.Now().Add(-2 * HookConversationalWindow)
	claims := []types.Claim{
		{ID: "anchor", Text: "persistent vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
	}
	edges := []types.Edge{}
	for i := 0; i < LoadBearingPersistentThreshold; i++ {
		depID := fmt.Sprintf("dep%d", i)
		claims = append(claims, types.Claim{ID: depID, Text: depID, Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: ancient})
		edges = append(edges, types.Edge{FromID: "anchor", ToID: depID, Relation: types.RelSupports})
	}
	_, vulns := Analyze(claims, edges, "test")
	var found bool
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			found = true
		}
	}
	if !found {
		t.Error("anchor at persistent-threshold dependent count should fire even without recent activity")
	}
}

// TestLoadBearingVibes_BelowPersistentThresholdDoesNotFire verifies the gap
// between the recent and persistent thresholds — a claim with several ancient
// dependents but below the persistent threshold and zero recent dependents
// should not fire. This is the "implicitly resolved" range: enough dependents
// that it was used a lot, but not so many that it qualifies as truly
// foundational, and no current activity to suggest it's still relevant.
func TestLoadBearingVibes_BelowPersistentThresholdDoesNotFire(t *testing.T) {
	ancient := time.Now().Add(-2 * HookConversationalWindow)
	claims := []types.Claim{
		{ID: "anchor", Text: "moderate-degree vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
	}
	edges := []types.Edge{}
	belowThreshold := LoadBearingPersistentThreshold - 1
	for i := 0; i < belowThreshold; i++ {
		depID := fmt.Sprintf("dep%d", i)
		claims = append(claims, types.Claim{ID: depID, Text: depID, Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: ancient})
		edges = append(edges, types.Edge{FromID: "anchor", ToID: depID, Relation: types.RelSupports})
	}
	_, vulns := Analyze(claims, edges, "test")
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			t.Errorf("anchor below persistent threshold (%d ancient deps, 0 recent) should not fire", belowThreshold)
		}
	}
}

// TestLoadBearingVibes_StressTestedSuppressed verifies that a claim whose
// dependents span StressTestedSessionThreshold+ distinct sessions (no
// contradicts) is treated as implicitly stress-tested and does NOT fire from
// the persistent branch. The conversation has accepted it through use across
// distinct contexts; surfacing adds noise rather than signal.
func TestLoadBearingVibes_StressTestedSuppressed(t *testing.T) {
	ancient := time.Now().Add(-7 * 24 * time.Hour)
	claims := []types.Claim{
		{ID: "anchor", Text: "stress-tested vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
	}
	edges := []types.Edge{}
	// LoadBearingPersistentThreshold dependents, distributed across at least
	// StressTestedSessionThreshold distinct sessions.
	for i := 0; i < LoadBearingPersistentThreshold; i++ {
		depID := fmt.Sprintf("dep%d", i)
		sess := fmt.Sprintf("session-%d", i%StressTestedSessionThreshold)
		claims = append(claims, types.Claim{ID: depID, Text: depID, Basis: types.BasisDeduction, Speaker: types.SpeakerUser, SessionID: sess, CreatedAt: ancient})
		edges = append(edges, types.Edge{FromID: "anchor", ToID: depID, Relation: types.RelSupports, CreatedAt: ancient})
	}
	_, vulns := Analyze(claims, edges, "test")
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			t.Errorf("stress-tested anchor (deps span %d+ sessions, no contradicts) should be suppressed", StressTestedSessionThreshold)
		}
	}
}

// TestLoadBearingVibes_BurstPatternStillFires verifies that a claim with
// many dependents all from a single session (concentrated burst) still fires
// from the persistent branch — that's the high-risk "everything rested on
// this for one session and we never came back" case.
func TestLoadBearingVibes_BurstPatternStillFires(t *testing.T) {
	ancient := time.Now().Add(-7 * 24 * time.Hour)
	claims := []types.Claim{
		{ID: "anchor", Text: "burst-pattern vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
	}
	edges := []types.Edge{}
	for i := 0; i < LoadBearingPersistentThreshold; i++ {
		depID := fmt.Sprintf("dep%d", i)
		claims = append(claims, types.Claim{ID: depID, Text: depID, Basis: types.BasisDeduction, Speaker: types.SpeakerUser, SessionID: "single-burst-session", CreatedAt: ancient})
		edges = append(edges, types.Edge{FromID: "anchor", ToID: depID, Relation: types.RelSupports, CreatedAt: ancient})
	}
	_, vulns := Analyze(claims, edges, "test")
	var found bool
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			found = true
		}
	}
	if !found {
		t.Error("burst-pattern anchor (deps from single session, no stress-test signal) should still fire")
	}
}

// TestLoadBearingVibes_ContradictedSpreadStillFires verifies that even if
// dependents span many sessions, the presence of a contradicts edge
// disqualifies the claim from stress-tested suppression — contested claims
// aren't implicitly accepted.
func TestLoadBearingVibes_ContradictedSpreadStillFires(t *testing.T) {
	ancient := time.Now().Add(-7 * 24 * time.Hour)
	claims := []types.Claim{
		{ID: "anchor", Text: "contested vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: ancient},
		{ID: "objector", Text: "objection", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, SessionID: "objection-session", CreatedAt: ancient},
	}
	edges := []types.Edge{
		{FromID: "objector", ToID: "anchor", Relation: types.RelContradicts, CreatedAt: ancient},
	}
	for i := 0; i < LoadBearingPersistentThreshold; i++ {
		depID := fmt.Sprintf("dep%d", i)
		sess := fmt.Sprintf("session-%d", i%StressTestedSessionThreshold)
		claims = append(claims, types.Claim{ID: depID, Text: depID, Basis: types.BasisDeduction, Speaker: types.SpeakerUser, SessionID: sess, CreatedAt: ancient})
		edges = append(edges, types.Edge{FromID: "anchor", ToID: depID, Relation: types.RelSupports, CreatedAt: ancient})
	}
	_, vulns := Analyze(claims, edges, "test")
	var found bool
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" && len(v.ClaimIDs) > 0 && v.ClaimIDs[0] == "anchor" {
			found = true
		}
	}
	if !found {
		t.Error("contested anchor with cross-session dependents should still fire (contradicts edge disqualifies stress-test suppression)")
	}
}

// TestFormatHookFindings_DifferentialCooldown verifies that persistent-only
// findings (FiredViaPersistent=true) use HookPersistentCooldown (7d) instead
// of the standard HookCooldownWindow (24h). A finding that fired 2 days ago
// is past the standard cooldown but still inside the persistent one.
func TestFormatHookFindings_DifferentialCooldown(t *testing.T) {
	makeFixture := func(persistent bool) (*types.Topology, *types.Vulnerabilities, []types.Claim) {
		claims := []types.Claim{
			{ID: "anchor", Text: "load-bearing claim", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
			{ID: "f1", Text: "f1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
			{ID: "f2", Text: "f2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
			{ID: "f3", Text: "f3", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
			{ID: "f4", Text: "f4", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
			{ID: "f5", Text: "f5", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		}
		topo := &types.Topology{Project: "test", ClaimCount: len(claims)}
		vulns := &types.Vulnerabilities{
			Project: "test",
			Items: []types.Vulnerability{
				{
					Severity:           "critical",
					Type:               "load_bearing_vibes",
					Description:        `Load-bearing vibes: "load-bearing claim" supports 8 other claims (never challenged: true)`,
					ClaimIDs:           []string{"anchor"},
					FiredViaPersistent: persistent,
				},
			},
		}
		return topo, vulns, claims
	}

	twoDaysAgo := time.Now().Add(-2 * 24 * time.Hour)
	cooldownMap := map[string]time.Time{"anchor|load_bearing_vibes": twoDaysAgo}

	// Persistent-only finding: 2 days ago is INSIDE HookPersistentCooldown (7d)
	// → cooldown still active → should not pick.
	topo, vulns, claims := makeFixture(true)
	_, pickedID, _, _ := FormatHookFindings(topo, vulns, claims, cooldownMap, 0, 0, 5)
	if pickedID == "anchor" {
		t.Error("persistent-only finding should be suppressed 2 days after firing (within HookPersistentCooldown)")
	}

	// Non-persistent finding: 2 days ago is PAST HookCooldownWindow (24h)
	// → cooldown expired → should pick.
	topo, vulns, claims = makeFixture(false)
	_, pickedID, _, _ = FormatHookFindings(topo, vulns, claims, cooldownMap, 0, 0, 5)
	if pickedID != "anchor" {
		t.Errorf("non-persistent finding should be eligible 2 days after firing (past HookCooldownWindow); got pickedID=%q", pickedID)
	}
}

// TestFormatHookFindings_InventoryFlagPrioritized verifies that within the
// same detector-priority tier, findings on claims with Moore inventory flags
// (grand_significance, sentience_claim, etc.) are surfaced ahead of findings
// on neutral claims. The inventory flags are explicit risk markers — a
// load-bearing vibes claim that's also marked grand_significance is
// structurally riskier than a neutral one and should win the priority slot.
func TestFormatHookFindings_InventoryFlagPrioritized(t *testing.T) {
	now := time.Now()
	claims := []types.Claim{
		{ID: "neutral-anchor", Text: "neutral load-bearing", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "flagged-anchor", Text: "flagged load-bearing", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: now, GrandSignificance: true},
		{ID: "f1", Text: "f1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "f2", Text: "f2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "f3", Text: "f3", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
		{ID: "f4", Text: "f4", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: now},
	}
	topo := &types.Topology{Project: "test", ClaimCount: len(claims)}
	// Both findings are critical load-bearing — same detector priority.
	// Neutral one listed first to ensure ordering reflects flag, not position.
	vulns := &types.Vulnerabilities{
		Project: "test",
		Items: []types.Vulnerability{
			{Severity: "critical", Type: "load_bearing_vibes", Description: `Load-bearing vibes: "neutral" supports 3 other claims (never challenged: true)`, ClaimIDs: []string{"neutral-anchor"}},
			{Severity: "critical", Type: "load_bearing_vibes", Description: `Load-bearing vibes: "flagged" supports 3 other claims (never challenged: true)`, ClaimIDs: []string{"flagged-anchor"}},
		},
	}
	_, pickedID, _, _ := FormatHookFindings(topo, vulns, claims, nil, 0, 0, 5)
	if pickedID != "flagged-anchor" {
		t.Errorf("inventory-flagged claim should win priority slot; got pickedID=%q want flagged-anchor", pickedID)
	}
}

// TestFormatHookFindings_AgeDecay verifies that a priority candidate whose
// anchor claim is older than HookMaxClaimAge gets filtered out.
func TestFormatHookFindings_AgeDecay(t *testing.T) {
	// Graph size must exceed HookColdStartMinClaims.
	anchorID := "old-claim"
	claims := []types.Claim{
		{ID: anchorID, Text: "ancient load-bearing vibes", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: time.Now().Add(-2 * HookMaxClaimAge)},
		{ID: "d1", Text: "d1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "d2", Text: "d2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "f1", Text: "filler 1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "f2", Text: "filler 2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "f3", Text: "filler 3", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
	}
	edges := []types.Edge{
		{FromID: "d1", ToID: anchorID, Relation: types.RelDependsOn},
		{FromID: "d2", ToID: anchorID, Relation: types.RelDependsOn},
	}
	topo, vulns := Analyze(claims, edges, "test")
	_, pickedID, _, _ := FormatHookFindings(topo, vulns, claims, nil, 0, 0, 5)
	if pickedID == anchorID {
		t.Error("age decay failed: picked an anchor older than HookMaxClaimAge")
	}
}

// TestStrengthCount_SeparateFromInfo verifies that bright/strength findings
// are counted in StrengthCount and not double-counted in InfoCount.
func TestStrengthCount_SeparateFromInfo(t *testing.T) {
	// Graph with a well-sourced load-bearer (strong basis, 2+ dependents) —
	// should produce a strength_well_sourced_load_bearer finding.
	claims := []types.Claim{
		{ID: "anchor", Text: "research-backed anchor", Basis: types.BasisResearch, Source: "Schultz 1997", Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "d1", Text: "d1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "d2", Text: "d2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
	}
	edges := []types.Edge{
		{FromID: "d1", ToID: "anchor", Relation: types.RelDependsOn},
		{FromID: "d2", ToID: "anchor", Relation: types.RelDependsOn},
	}
	_, vulns := Analyze(claims, edges, "test")
	if vulns.StrengthCount < 1 {
		t.Errorf("StrengthCount = %d, want >= 1", vulns.StrengthCount)
	}
	// Count strength items manually to confirm InfoCount didn't swallow them.
	var actualStrengths int
	for _, v := range vulns.Items {
		if strings.HasPrefix(v.Type, "strength_") {
			actualStrengths++
			if v.Severity != "info" {
				t.Errorf("strength finding has severity %q, want info", v.Severity)
			}
		}
	}
	if actualStrengths != vulns.StrengthCount {
		t.Errorf("StrengthCount = %d, but %d items have strength_ prefix", vulns.StrengthCount, actualStrengths)
	}
	// InfoCount must not include strengths — StrengthCount is the sole
	// counter for them. The two counters partition severity=info items
	// (strength_-prefixed → StrengthCount; rest → InfoCount).
	var expectedInfo int
	for _, v := range vulns.Items {
		if v.Severity == "info" && !strings.HasPrefix(v.Type, "strength_") {
			expectedInfo++
		}
	}
	if vulns.InfoCount != expectedInfo {
		t.Errorf("InfoCount = %d but expected %d non-strength info items", vulns.InfoCount, expectedInfo)
	}
}

// TestProductiveStressTest_ContradictsProxy verifies the broadened detector
// fires when a mid-chain claim has an incoming contradicts edge (the
// structural proxy for "someone pushed back") even without the Challenged
// flag explicitly set.
func TestProductiveStressTest_ContradictsProxy(t *testing.T) {
	claims := []types.Claim{
		{ID: "premise", Text: "the stressed premise", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "upstream", Text: "upstream node", Basis: types.BasisResearch, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "downstream", Text: "downstream node", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "critic", Text: "counter-argument", Basis: types.BasisResearch, Speaker: types.SpeakerAssistant, CreatedAt: time.Now()},
	}
	edges := []types.Edge{
		// premise has both outgoing (it depends on upstream) and incoming
		// (downstream depends on premise) support/depends_on edges — i.e.,
		// premise is mid-chain, which is what the detector requires.
		{FromID: "premise", ToID: "upstream", Relation: types.RelDependsOn},
		{FromID: "downstream", ToID: "premise", Relation: types.RelDependsOn},
		// Incoming contradicts from a critic — the proxy for "someone
		// pushed back on premise."
		{FromID: "critic", ToID: "premise", Relation: types.RelContradicts},
	}
	out := findProductiveStressTest(claims, edges)
	if len(out) == 0 {
		t.Fatal("expected productive_stress_test finding via contradicts proxy, got 0")
	}
	if out[0].ClaimIDs[0] != "premise" {
		t.Errorf("finding anchor = %q, want premise", out[0].ClaimIDs[0])
	}
}

// TestFormatHookFindings_ColdStart verifies the HookColdStartMinClaims gate:
// below the minimum graph size the hook stays silent even when individual
// detectors would otherwise fire.
func TestFormatHookFindings_ColdStart(t *testing.T) {
	// 3-claim graph — below HookColdStartMinClaims (6). Construct a shape
	// that WOULD trigger load_bearing_vibes in a larger graph.
	claims := []types.Claim{
		{ID: "anchor", Text: "vibes anchor", Basis: types.BasisVibes, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "d1", Text: "d1", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
		{ID: "d2", Text: "d2", Basis: types.BasisDeduction, Speaker: types.SpeakerUser, CreatedAt: time.Now()},
	}
	edges := []types.Edge{
		{FromID: "d1", ToID: "anchor", Relation: types.RelDependsOn},
		{FromID: "d2", ToID: "anchor", Relation: types.RelDependsOn},
	}
	topo, vulns := Analyze(claims, edges, "test")
	// Sanity: without cold-start, there IS a load-bearing vibes finding in vulns.
	foundInVulns := false
	for _, v := range vulns.Items {
		if v.Type == "load_bearing_vibes" {
			foundInVulns = true
		}
	}
	if !foundInVulns {
		t.Fatal("setup: expected load_bearing_vibes in vulns for a below-cold-start graph")
	}

	// FormatHookFindings should return empty despite vulns having items —
	// cold-start gate blocks the hook.
	summary, pickedID, _, _ := FormatHookFindings(topo, vulns, claims, nil, 0, 0, 5)
	if summary != "" || pickedID != "" {
		t.Errorf("cold-start failed: got summary=%q picked=%q, want empty", summary, pickedID)
	}
}

// TestUnchallengedChain_BreaksOnPushbackEdge verifies that an incoming
// contradicts or questions edge mid-chain breaks the chain — a preexisting
// gap until v0.5.2, since the detector previously only checked the
// Challenged flag (which is set only via explicit MCP claims.challenge
// calls, almost never in real data).
func TestUnchallengedChain_BreaksOnPushbackEdge(t *testing.T) {
	// Build a 5-claim chain a→b→c→d→e. Without pushback, this is a clean
	// 5-claim unchallenged chain.
	claims := []types.Claim{
		makeClaim("a", "first", types.BasisVibes),
		makeClaim("b", "second", types.BasisVibes),
		makeClaim("c", "third (middle)", types.BasisVibes),
		makeClaim("d", "fourth", types.BasisVibes),
		makeClaim("e", "fifth", types.BasisVibes),
		// A critic off-chain that questions the middle claim.
		makeClaim("critic", "counter", types.BasisResearch),
	}
	chainEdges := []types.Edge{
		makeEdge("a", "b", types.RelDependsOn),
		makeEdge("b", "c", types.RelDependsOn),
		makeEdge("c", "d", types.RelDependsOn),
		makeEdge("d", "e", types.RelDependsOn),
	}

	// Run 1: chain with no pushback — expect a long chain to fire.
	_, vulns := Analyze(claims, chainEdges, "test")
	var foundLong bool
	for _, v := range vulns.Items {
		if v.Type == "unchallenged_chain" && len(v.ClaimIDs) >= 4 {
			foundLong = true
		}
	}
	if !foundLong {
		t.Fatal("setup: expected a long unchallenged_chain finding without pushback")
	}

	// Run 2: add a questions edge pointing at 'c' mid-chain. Chain should
	// break, and either no unchallenged_chain fires OR it fires with a
	// shorter length that stops at c.
	withQuestions := append([]types.Edge(nil), chainEdges...)
	withQuestions = append(withQuestions, makeEdge("critic", "c", types.RelQuestions))
	_, vulns = Analyze(claims, withQuestions, "test")
	for _, v := range vulns.Items {
		if v.Type == "unchallenged_chain" {
			for _, id := range v.ClaimIDs {
				if id == "c" {
					t.Errorf("unchallenged_chain should not include claim 'c' after a questions edge targets it, got chain %v", v.ClaimIDs)
				}
			}
		}
	}

	// Run 3: same but with a contradicts edge. Same expectation.
	withContradicts := append([]types.Edge(nil), chainEdges...)
	withContradicts = append(withContradicts, makeEdge("critic", "c", types.RelContradicts))
	_, vulns = Analyze(claims, withContradicts, "test")
	for _, v := range vulns.Items {
		if v.Type == "unchallenged_chain" {
			for _, id := range v.ClaimIDs {
				if id == "c" {
					t.Errorf("unchallenged_chain should not include claim 'c' after a contradicts edge targets it, got chain %v", v.ClaimIDs)
				}
			}
		}
	}
}
