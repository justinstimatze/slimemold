package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// TestLegacyLoadBearer_FiresOnOldAndActive constructs a claim that's 45 days
// old, was last referenced yesterday, and has 3 incoming structural edges.
// All three criteria are met → finding must fire.
func TestLegacyLoadBearer_FiresOnOldAndActive(t *testing.T) {
	now := time.Now()
	anchor := types.Claim{
		ID:               "anchor",
		Text:             "Old foundational claim that's still being referenced.",
		Speaker:          types.SpeakerAssistant,
		Basis:            types.BasisResearch,
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
		LastReferencedAt: now.Add(-24 * time.Hour),
	}
	deps := []types.Claim{
		{ID: "d1", Text: "Recent dependent A", Speaker: types.SpeakerAssistant, Basis: types.BasisLLMOutput, CreatedAt: now.Add(-1 * time.Hour), LastReferencedAt: now.Add(-1 * time.Hour)},
		{ID: "d2", Text: "Recent dependent B", Speaker: types.SpeakerAssistant, Basis: types.BasisLLMOutput, CreatedAt: now.Add(-1 * time.Hour), LastReferencedAt: now.Add(-1 * time.Hour)},
		{ID: "d3", Text: "Recent dependent C", Speaker: types.SpeakerAssistant, Basis: types.BasisLLMOutput, CreatedAt: now.Add(-1 * time.Hour), LastReferencedAt: now.Add(-1 * time.Hour)},
	}
	claims := append([]types.Claim{anchor}, deps...)
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "anchor", Relation: types.RelDependsOn, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "e2", FromID: "d2", ToID: "anchor", Relation: types.RelDependsOn, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "e3", FromID: "d3", ToID: "anchor", Relation: types.RelSupports, CreatedAt: now.Add(-1 * time.Hour)},
	}

	findings := findLegacyLoadBearers(claims, edges)
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 legacy_load_bearer finding, got %d", len(findings))
	}
	if findings[0].Type != "legacy_load_bearer" {
		t.Errorf("expected type=legacy_load_bearer, got %q", findings[0].Type)
	}
	if findings[0].ClaimIDs[0] != "anchor" {
		t.Errorf("expected anchor claim, got %q", findings[0].ClaimIDs[0])
	}
	if !findings[0].FiredViaPersistent {
		t.Errorf("expected FiredViaPersistent=true so skipAnchor's age cap doesn't drop it")
	}
}

// TestLegacyLoadBearer_SuppressesYoungClaims confirms the age threshold:
// a claim that's only 5 days old is excluded even if recently referenced
// and well-supported. We have other detectors for young load-bearers.
func TestLegacyLoadBearer_SuppressesYoungClaims(t *testing.T) {
	now := time.Now()
	youngAnchor := types.Claim{
		ID:               "young",
		Text:             "Young claim with lots of dependents",
		CreatedAt:        now.Add(-5 * 24 * time.Hour),
		LastReferencedAt: now.Add(-1 * time.Hour),
	}
	claims := []types.Claim{
		youngAnchor,
		{ID: "d1", CreatedAt: now, LastReferencedAt: now},
		{ID: "d2", CreatedAt: now, LastReferencedAt: now},
		{ID: "d3", CreatedAt: now, LastReferencedAt: now},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "young", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e2", FromID: "d2", ToID: "young", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e3", FromID: "d3", ToID: "young", Relation: types.RelSupports, CreatedAt: now},
	}
	if findings := findLegacyLoadBearers(claims, edges); len(findings) != 0 {
		t.Errorf("expected 0 findings on young anchor, got %d", len(findings))
	}
}

// TestLegacyLoadBearer_SuppressesStaleClaims: claim is old AND has deps,
// but hasn't been referenced in months. That's the "no longer relevant"
// case — let the archive sweep handle it, don't surface here.
func TestLegacyLoadBearer_SuppressesStaleClaims(t *testing.T) {
	now := time.Now()
	staleAnchor := types.Claim{
		ID:               "stale",
		Text:             "Old claim that hasn't been touched in a long time",
		CreatedAt:        now.Add(-90 * 24 * time.Hour),
		LastReferencedAt: now.Add(-60 * 24 * time.Hour),
	}
	claims := []types.Claim{
		staleAnchor,
		{ID: "d1"}, {ID: "d2"}, {ID: "d3"},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "stale", Relation: types.RelDependsOn, CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "e2", FromID: "d2", ToID: "stale", Relation: types.RelDependsOn, CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "e3", FromID: "d3", ToID: "stale", Relation: types.RelSupports, CreatedAt: now.Add(-60 * 24 * time.Hour)},
	}
	if findings := findLegacyLoadBearers(claims, edges); len(findings) != 0 {
		t.Errorf("expected 0 findings on stale anchor (last referenced 60d ago), got %d", len(findings))
	}
}

// TestLegacyLoadBearer_SkipsClosed: a claim explicitly closed by the model
// (e.g. "feature X is missing" after X was implemented) is not a finding.
// Once closed, the claim is resolved — keeping it would surface noise.
func TestLegacyLoadBearer_SkipsClosed(t *testing.T) {
	now := time.Now()
	closedAnchor := types.Claim{
		ID:               "closed",
		Text:             "Old claim that was explicitly closed",
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
		LastReferencedAt: now.Add(-1 * 24 * time.Hour),
		Closed:           true,
	}
	claims := []types.Claim{
		closedAnchor,
		{ID: "d1"}, {ID: "d2"}, {ID: "d3"},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "closed", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e2", FromID: "d2", ToID: "closed", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e3", FromID: "d3", ToID: "closed", Relation: types.RelSupports, CreatedAt: now},
	}
	if findings := findLegacyLoadBearers(claims, edges); len(findings) != 0 {
		t.Errorf("expected 0 findings on closed anchor, got %d", len(findings))
	}
}

// TestLegacyLoadBearer_RequiresMinDeps: an old, recently-referenced claim
// with only 1 incoming edge isn't load-bearing. Threshold is intentionally
// >=2 — anything below that is too noisy to surface.
func TestLegacyLoadBearer_RequiresMinDeps(t *testing.T) {
	now := time.Now()
	anchor := types.Claim{
		ID:               "anchor",
		Text:             "Old claim with just one dependent",
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
		LastReferencedAt: now.Add(-1 * time.Hour),
	}
	claims := []types.Claim{anchor, {ID: "d1"}}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "anchor", Relation: types.RelDependsOn, CreatedAt: now},
	}
	if findings := findLegacyLoadBearers(claims, edges); len(findings) != 0 {
		t.Errorf("expected 0 findings with only 1 incoming dep, got %d", len(findings))
	}
}

// TestLegacyLoadBearer_ContradictsAndQuestionsDontCount: 5 incoming
// contradicts edges + 5 incoming questions edges should NOT count toward
// LegacyMinDeps. Being argued with is not load-bearing.
func TestLegacyLoadBearer_ContradictsAndQuestionsDontCount(t *testing.T) {
	now := time.Now()
	anchor := types.Claim{
		ID:               "anchor",
		Text:             "Old contested claim",
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
		LastReferencedAt: now.Add(-1 * time.Hour),
	}
	claims := []types.Claim{anchor}
	var edges []types.Edge
	for i := 0; i < 5; i++ {
		edges = append(edges,
			types.Edge{ID: "c" + string(rune('0'+i)), FromID: "x", ToID: "anchor", Relation: types.RelContradicts, CreatedAt: now},
			types.Edge{ID: "q" + string(rune('0'+i)), FromID: "y", ToID: "anchor", Relation: types.RelQuestions, CreatedAt: now},
		)
	}
	if findings := findLegacyLoadBearers(claims, edges); len(findings) != 0 {
		t.Errorf("expected 0 findings (only contradicts/questions edges), got %d", len(findings))
	}
}

// TestLegacyLoadBearer_DescriptionMentionsAge confirms the description
// includes the age info — important so the model can reason about
// "how old" without re-querying the timestamp itself.
func TestLegacyLoadBearer_DescriptionMentionsAge(t *testing.T) {
	now := time.Now()
	anchor := types.Claim{
		ID:               "anchor",
		Text:             "Foundational anchor",
		CreatedAt:        now.Add(-60 * 24 * time.Hour),
		LastReferencedAt: now.Add(-2 * 24 * time.Hour),
	}
	claims := []types.Claim{
		anchor,
		{ID: "d1"}, {ID: "d2"}, {ID: "d3"},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "anchor", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e2", FromID: "d2", ToID: "anchor", Relation: types.RelDependsOn, CreatedAt: now},
		{ID: "e3", FromID: "d3", ToID: "anchor", Relation: types.RelSupports, CreatedAt: now},
	}
	findings := findLegacyLoadBearers(claims, edges)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	desc := findings[0].Description
	if !strings.Contains(desc, "60d") {
		t.Errorf("expected description to include age '60d', got %q", desc)
	}
	if !strings.Contains(desc, "2d") {
		t.Errorf("expected description to include reference recency '2d', got %q", desc)
	}
}
