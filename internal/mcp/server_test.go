package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir, "test-project")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCoreRegisterClaim(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	result, err := CoreRegisterClaim(ctx, db, "test-project", "test claim", types.BasisVibes, 0.7, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CoreRegisterClaim: %v", err)
	}
	if result.ClaimID == "" {
		t.Error("expected claim ID")
	}
	if result.GraphSize != 1 {
		t.Errorf("graph_size = %d, want 1", result.GraphSize)
	}
	// Vibes basis should produce a warning
	if len(result.Warnings) == 0 {
		t.Error("expected warnings for vibes basis")
	}
	foundBasisWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "weak basis") {
			foundBasisWarning = true
		}
	}
	if !foundBasisWarning {
		t.Errorf("expected weak basis warning, got %v", result.Warnings)
	}
}

func TestCoreRegisterClaimWithEdges(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Register first claim
	r1, _ := CoreRegisterClaim(ctx, db, "test-project", "foundation claim", types.BasisResearch, 0.9, "Smith 2024", nil, nil, nil)

	// Register second claim that depends on first
	r2, err := CoreRegisterClaim(ctx, db, "test-project", "dependent claim", types.BasisDeduction, 0.8, "", []string{"foundation claim"}, nil, nil)
	if err != nil {
		t.Fatalf("CoreRegisterClaim: %v", err)
	}

	// Should have created an edge
	edges, _ := db.GetEdgesForClaim(r2.ClaimID)
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].ToID != r1.ClaimID {
		t.Errorf("edge to=%s, want %s", edges[0].ToID, r1.ClaimID)
	}
}

func TestCoreRegisterOrphanWarning(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	result, _ := CoreRegisterClaim(ctx, db, "test-project", "isolated claim", types.BasisResearch, 0.9, "Jones 2025", nil, nil, nil)

	foundOrphan := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "Orphan") {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Errorf("expected orphan warning, got %v", result.Warnings)
	}
}

func TestCoreChallengeClaim(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	r, _ := CoreRegisterClaim(ctx, db, "test-project", "weak claim", types.BasisVibes, 0.5, "", nil, nil, nil)

	if err := CoreChallengeClaim(ctx, db, r.ClaimID, "upheld", "confirmed after review"); err != nil {
		t.Fatalf("CoreChallengeClaim: %v", err)
	}

	claim, _ := db.GetClaim(r.ClaimID)
	if !claim.Challenged {
		t.Error("expected challenged = true")
	}
}

func TestCoreGetTopology(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	CoreRegisterClaim(ctx, db, "test-project", "claim A", types.BasisResearch, 0.9, "", nil, nil, nil)
	CoreRegisterClaim(ctx, db, "test-project", "claim B", types.BasisVibes, 0.5, "", nil, nil, nil)

	topo, err := CoreGetTopology(ctx, db, "test-project")
	if err != nil {
		t.Fatalf("CoreGetTopology: %v", err)
	}
	if topo.ClaimCount != 2 {
		t.Errorf("claims = %d, want 2", topo.ClaimCount)
	}
	if topo.BasisCounts[types.BasisResearch] != 1 {
		t.Errorf("research = %d, want 1", topo.BasisCounts[types.BasisResearch])
	}
}

// TestClaimFromExtracted_PreservesAllFields locks the field mapping between
// ExtractedClaim (extractor JSON) and Claim (DB row). Both ingestion paths
// (transcript and document) go through claimFromExtracted, so a regression
// here drops fields silently — tests at higher levels would still pass on
// the basic flow but inventory flags would be quietly false everywhere.
func TestClaimFromExtracted_PreservesAllFields(t *testing.T) {
	ec := types.ExtractedClaim{
		Text:                     "everything-flagged claim",
		Basis:                    string(types.BasisLLMOutput),
		Source:                   "src",
		Confidence:               0.42,
		Speaker:                  string(types.SpeakerAssistant),
		TerminatesInquiry:        true,
		GrandSignificance:        true,
		UniqueConnection:         true,
		DismissesCounterevidence: true,
		AbilityOverstatement:     true,
		SentienceClaim:           true,
		RelationalDrift:          true,
	}
	got := claimFromExtracted(ec, "sess-x", "proj-y")
	if got.Text != ec.Text || got.Basis != types.BasisLLMOutput || got.Confidence != 0.42 ||
		got.Source != "src" || got.SessionID != "sess-x" || got.Project != "proj-y" ||
		got.Speaker != types.SpeakerAssistant {
		t.Errorf("scalar fields not preserved: %+v", got)
	}
	if !got.TerminatesInquiry {
		t.Errorf("TerminatesInquiry not propagated")
	}
	if !got.GrandSignificance || !got.UniqueConnection || !got.DismissesCounterevidence ||
		!got.AbilityOverstatement || !got.SentienceClaim || !got.RelationalDrift {
		t.Errorf("inventory flags lost in mapping: %+v", got)
	}
	if got.ID == "" {
		t.Error("expected fresh UUID assigned")
	}
	if got.CreatedAt.IsZero() {
		t.Error("expected CreatedAt timestamp")
	}
}

// TestClaimFromExtracted_RoundTripsThroughStore verifies that an extracted
// claim with inventory flags survives the full path from ExtractedClaim →
// claimFromExtracted → CreateClaim → GetClaim. Catches regressions where the
// helper preserves fields but the store INSERT/scan drops them, or vice versa.
func TestClaimFromExtracted_RoundTripsThroughStore(t *testing.T) {
	db := testDB(t)

	ec := types.ExtractedClaim{
		Text:              "roundtrip target",
		Basis:             string(types.BasisLLMOutput),
		Confidence:        0.7,
		Speaker:           string(types.SpeakerAssistant),
		SentienceClaim:    true,
		RelationalDrift:   true,
		GrandSignificance: true,
	}
	claim := claimFromExtracted(ec, "rt-session", "test-project")
	if err := db.CreateClaim(claim); err != nil {
		t.Fatalf("CreateClaim: %v", err)
	}
	got, err := db.GetClaim(claim.ID)
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if !got.SentienceClaim || !got.RelationalDrift || !got.GrandSignificance {
		t.Errorf("inventory flags lost on round-trip: %+v", got)
	}
	if got.UniqueConnection || got.DismissesCounterevidence || got.AbilityOverstatement {
		t.Errorf("unset flags spuriously true: %+v", got)
	}
}

// TestCoerceBasis_ValidPassthrough locks the contract that valid basis values
// from the closed enum survive coercion unchanged. Sanity guard against a
// future "normalize" refactor that maps everything to a default.
func TestCoerceBasis_ValidPassthrough(t *testing.T) {
	for _, b := range []string{"research", "empirical", "analogy", "vibes", "llm_output", "deduction", "assumption", "definition", "convention"} {
		got := coerceBasis(b, "assistant")
		if string(got) != b {
			t.Errorf("coerceBasis(%q) = %q, want %q", b, got, b)
		}
	}
}

// TestCoerceBasis_OutOfEnumFallback exercises the defensive layer that saved
// the v5 README run when Sonnet emitted basis="document" (confused with the
// speaker enum). Whitespace + case normalization, plus speaker-appropriate
// fallback, keep ingest moving instead of aborting the whole document.
func TestCoerceBasis_OutOfEnumFallback(t *testing.T) {
	cases := []struct {
		raw, speaker, want string
	}{
		{"document", "document", "vibes"},
		{"document", "user", "vibes"},
		{"document", "assistant", "llm_output"},
		{" Vibes ", "user", "vibes"},          // whitespace + case
		{"RESEARCH", "assistant", "research"}, // case
		{"experience", "user", "vibes"},       // truly off-enum
		{"", "assistant", "llm_output"},       // empty
	}
	for _, c := range cases {
		got := coerceBasis(c.raw, c.speaker)
		if string(got) != c.want {
			t.Errorf("coerceBasis(%q, %q) = %q, want %q", c.raw, c.speaker, got, c.want)
		}
	}
}

func TestDeduplicateBatch(t *testing.T) {
	batch := []types.ExtractedClaim{
		{Index: 0, Text: "Nick Thomas-Symonds endorsed Labour's decision not to accept the IHRA definition", Basis: "vibes", Confidence: 0.8, Speaker: "user"},
		{Index: 1, Text: "Nick Thomas-Symonds went out on the airwaves defending the decision not to accept the IHRA definition", Basis: "vibes", Confidence: 0.7, Speaker: "user"},
		{Index: 2, Text: "Something completely different about fish", Basis: "vibes", Confidence: 0.5, Speaker: "user", DependsOnIndices: []int{0}},
	}

	result := deduplicateBatch(batch)

	// Claims 0 and 1 should merge (same speaker, near-identical text); claim 2 should remain
	if len(result) != 2 {
		t.Errorf("dedup produced %d claims, want 2", len(result))
		for _, c := range result {
			t.Logf("  [%d] %s", c.Index, c.Text[:50])
		}
		return
	}

	// The merged claim should pick the longer text (claim 1)
	var merged, fish *types.ExtractedClaim
	for i := range result {
		if containsWord(result[i].Text, "airwaves") || containsWord(result[i].Text, "endorsed") {
			merged = &result[i]
		}
		if containsWord(result[i].Text, "fish") {
			fish = &result[i]
		}
	}
	if merged == nil {
		t.Fatal("merged claim not found")
		return
	}
	if fish == nil {
		t.Fatal("fish claim not found")
		return
	}
	// Longer text wins
	if !containsWord(merged.Text, "airwaves") {
		t.Error("expected longer text (with 'airwaves') to be the representative")
	}
}

func TestDeduplicateBatchPreservesContradictions(t *testing.T) {
	// Claims with high text overlap but connected by contradicts should NOT merge
	batch := []types.ExtractedClaim{
		{Index: 0, Text: "The user never used the word token during this conversation", Basis: "vibes", Confidence: 0.9, Speaker: "user", ContradictsIndices: []int{1}},
		{Index: 1, Text: "The user used the word token twice during this conversation", Basis: "llm_output", Confidence: 0.8, Speaker: "assistant"},
	}

	result := deduplicateBatch(batch)

	if len(result) != 2 {
		t.Errorf("dedup merged contradicting claims: got %d, want 2", len(result))
	}
}

func containsWord(text, word string) bool {
	return strings.Contains(strings.ToLower(text), word)
}

func TestDeduplicateBatchEdgeRemapping(t *testing.T) {
	// Claim 0 and 1 are near-identical (same speaker); claim 2 depends on claim 0.
	// After dedup, claim 2's depends_on should be remapped to the representative.
	batch := []types.ExtractedClaim{
		{Index: 0, Text: "Nick Thomas-Symonds endorsed Labour's decision not to accept the IHRA definition", Basis: "vibes", Confidence: 0.8, Speaker: "user"},
		{Index: 1, Text: "Nick Thomas-Symonds went out on the airwaves defending the decision not to accept the IHRA definition", Basis: "vibes", Confidence: 0.7, Speaker: "user"},
		{Index: 2, Text: "Something completely different about fish", Basis: "vibes", Confidence: 0.5, Speaker: "user", DependsOnIndices: []int{0}},
	}

	result := deduplicateBatch(batch)
	if len(result) != 2 {
		t.Fatalf("dedup produced %d claims, want 2", len(result))
	}

	// Find the fish claim and verify its depends_on was remapped
	for _, c := range result {
		if containsWord(c.Text, "fish") {
			if len(c.DependsOnIndices) != 1 {
				t.Fatalf("fish.DependsOnIndices = %v, want exactly 1 entry", c.DependsOnIndices)
			}
			// The representative of the merged cluster should be claim 1 (longer text)
			repIdx := -1
			for _, r := range result {
				if containsWord(r.Text, "airwaves") {
					repIdx = r.Index
				}
			}
			if c.DependsOnIndices[0] != repIdx {
				t.Errorf("fish.DependsOnIndices[0] = %d, want %d (representative index)", c.DependsOnIndices[0], repIdx)
			}
			return
		}
	}
	t.Fatal("fish claim not found in deduped batch")
}

func TestPruneHighDegreeEdges(t *testing.T) {
	db := testDB(t)

	// Create a hub claim with many outgoing edges
	hub := &types.Claim{ID: "hub", Text: "hub claim", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(hub)

	// Create 8 target claims and edges from hub to each
	for i := 0; i < 8; i++ {
		target := &types.Claim{
			ID: fmt.Sprintf("t%d", i), Text: fmt.Sprintf("target %d", i),
			Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser,
		}
		db.CreateClaim(target)
		rel := types.RelSupports
		if i < 2 {
			rel = types.RelContradicts // first 2 are contradicts (highest priority)
		}
		db.CreateEdge(&types.Edge{FromID: "hub", ToID: target.ID, Relation: rel})
	}

	// Verify 8 edges exist
	edges, _ := db.GetEdgesForClaim("hub")
	if len(edges) != 8 {
		t.Fatalf("before prune: %d edges, want 8", len(edges))
	}

	// Prune
	pruneHighDegreeEdges(db, "test-project")

	// Should be capped at 5
	edges, _ = db.GetEdgesForClaim("hub")
	if len(edges) != 5 {
		t.Fatalf("after prune: %d edges, want 5", len(edges))
	}

	// Both contradicts edges should survive (higher priority)
	contradicts := 0
	for _, e := range edges {
		if e.Relation == types.RelContradicts {
			contradicts++
		}
	}
	if contradicts != 2 {
		t.Errorf("contradicts edges after prune: %d, want 2", contradicts)
	}
}

func TestCoreGetVulnerabilities(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Create load-bearing vibes scenario
	CoreRegisterClaim(ctx, db, "test-project", "vibes foundation", types.BasisVibes, 0.5, "", nil, nil, nil)
	CoreRegisterClaim(ctx, db, "test-project", "dependent A", types.BasisDeduction, 0.8, "", []string{"vibes foundation"}, nil, nil)
	CoreRegisterClaim(ctx, db, "test-project", "dependent B", types.BasisDeduction, 0.8, "", []string{"vibes foundation"}, nil, nil)

	vulns, err := CoreGetVulnerabilities(ctx, db, "test-project")
	if err != nil {
		t.Fatalf("CoreGetVulnerabilities: %v", err)
	}
	if vulns.CriticalCount == 0 {
		t.Error("expected critical vulnerabilities for load-bearing vibes")
	}
}

func TestValidateBasis_DowngradesWithoutSignal(t *testing.T) {
	// Transcript uses a neutral string so our source strings below don't
	// accidentally match — the research guardrail walks the source for its
	// first significant word and checks for it in the transcript.
	transcript := "foo bar baz"
	claims := []types.ExtractedClaim{
		// Research without source in transcript → llm_output
		{Text: "Einstein showed special relativity", Basis: "research", Source: "Einstein 1905"},
		// Research with empty source → llm_output (no source at all)
		{Text: "latency is bounded", Basis: "research", Source: ""},
		// Deduction without logical-step signal → vibes
		{Text: "the system will obviously scale linearly", Basis: "deduction"},
		// Deduction WITH signal → stays deduction
		{Text: "if latency is bounded then throughput follows", Basis: "deduction"},
		// Empirical without observer signal → vibes
		{Text: "latency is typically 200ms under load", Basis: "empirical"},
		// Empirical WITH signal → stays empirical
		{Text: "we measured latency at 200ms under load", Basis: "empirical"},
		// Convention without declared-practice signal → vibes
		{Text: "beads is the best issue tracker", Basis: "convention"},
		// Convention WITH signal → stays convention
		{Text: "this project uses beads for issue tracking", Basis: "convention"},
	}

	validateResearchBasis(claims, transcript)

	expected := []string{
		"llm_output", // research-with-non-matching-source
		"llm_output", // research-with-empty-source
		"vibes",      // deduction-without-signal
		"deduction",  // deduction-with-signal
		"vibes",      // empirical-without-signal
		"empirical",  // empirical-with-signal
		"vibes",      // convention-without-signal
		"convention", // convention-with-signal
	}
	for i, want := range expected {
		if claims[i].Basis != want {
			t.Errorf("claim %d: basis = %q, want %q (text %q)", i, claims[i].Basis, want, claims[i].Text)
		}
	}
}
