package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

// TestSweepLegacyThresholdsArePaired enforces the documented invariant:
// store.SweepStructuralMin and analysis.LegacyMinDeps must be equal so the
// "old + few-deps" boundary partitions cleanly into sweep candidates vs
// legacy_load_bearer findings — no gap, no overlap at the boundary.
//
// Both constants live in sibling packages (which can't import each other) and
// each carries a doc comment saying "keep equal," but nothing enforced it
// before this test. This package imports both, so it's the natural home for
// the cross-package assertion. Bump one without bumping the other and CI
// breaks here with a message that names the invariant.
func TestSweepLegacyThresholdsArePaired(t *testing.T) {
	if store.SweepStructuralMin != analysis.LegacyMinDeps {
		t.Errorf("paired-threshold invariant violated: "+
			"store.SweepStructuralMin=%d, analysis.LegacyMinDeps=%d (must be equal); "+
			"see sweep.go and legacy.go doc comments — boundary cases at deps=X "+
			"would either fire both detectors (overlap) or neither (gap).",
			store.SweepStructuralMin, analysis.LegacyMinDeps)
	}
}

// TestSweepLegacyThresholdBoundaryBehavior pins the actual *behavior* at the
// boundary, not just constant equality. Comparator drift (`>= K` →  `> K` on
// either side) would still satisfy the equality assertion above but break
// the no-gap-no-overlap invariant in the wild.
//
// The test needs TWO boundary claims sharing the same K dependents because
// sweep and legacy use mutually-exclusive LastReferencedAt windows: sweep
// requires idle >= SweepIdleDays (30d), legacy requires sinceRef <=
// LegacyRecentRefThreshold (7d). A single claim cannot exercise both — the
// idle gate (sweep.go:156) would short-circuit before the structural-rescue
// branch (sweep.go:159) ever ran on a recently-referenced claim, so the
// "no-overlap" assertion would pass for the wrong reason (idle gate, not
// structural rescue) and a comparator drift on SweepStructuralMin would
// slip through undetected.
//
// Two claims, both at deps == K, both old enough to qualify:
//   - boundary-idle: old + idle. The idle gate doesn't skip it; the
//     structural-rescue branch keeps it from sweep. A drift `>= K` →
//     `> K` on the sweep side would make this claim get swept.
//   - boundary-recent: old + recently referenced. Fires as a
//     legacy_load_bearer. A drift on the legacy side (`>= K` → `> K`,
//     equivalently `< K` → `<= K`) would make this claim NOT fire.
//
// Together they pin the partition: at deps == K, sweep rescues old idle
// claims AND legacy fires on old recently-touched claims. No row falls
// into both buckets or neither.
func TestSweepLegacyThresholdBoundaryBehavior(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)    // > LegacyAgeThreshold (30d) AND > SweepAgeDays (30d)
	idleRef := now.Add(-40 * 24 * time.Hour) // > SweepIdleDays (30d) — passes sweep idle gate
	recentRef := now.Add(-2 * 24 * time.Hour) // < LegacyRecentRefThreshold (7d) — fires legacy
	const K = store.SweepStructuralMin       // == analysis.LegacyMinDeps by paired-threshold invariant

	// Two boundary claims at deps == K, sharing the same K dependents.
	// boundary-idle exercises sweep's structural-rescue; boundary-recent
	// exercises legacy_load_bearer firing. The shared dependents make
	// each boundary claim hit exactly K incoming structural edges.
	claims := []types.Claim{
		{ID: "boundary-idle", Project: "p", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: old, LastReferencedAt: idleRef},
		{ID: "boundary-recent", Project: "p", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: old, LastReferencedAt: recentRef},
	}
	var edges []types.Edge
	for i := 0; i < K; i++ {
		depID := fmt.Sprintf("dep%d", i)
		claims = append(claims, types.Claim{ID: depID, Project: "p", Speaker: types.SpeakerAssistant,
			Basis: types.BasisVibes, CreatedAt: now, LastReferencedAt: now})
		edges = append(edges,
			types.Edge{ID: fmt.Sprintf("e-idle-%d", i), FromID: depID, ToID: "boundary-idle",
				Relation: types.RelSupports, CreatedAt: now},
			types.Edge{ID: fmt.Sprintf("e-recent-%d", i), FromID: depID, ToID: "boundary-recent",
				Relation: types.RelSupports, CreatedAt: now},
		)
	}

	// OVERLAP check: at deps == K, boundary-idle MUST NOT be swept. The
	// idle gate doesn't skip it (idle = 40d > 30d), so this assertion
	// actually exercises the structural-rescue branch. A comparator drift
	// `>= K` → `> K` on the sweep side would make boundary-idle (deps=K)
	// fall through to the weak-basis archive branch.
	swept := store.SweepCandidates(claims, edges)
	for _, id := range swept {
		if id == "boundary-idle" {
			t.Errorf("OVERLAP at deps=K (=%d): boundary-idle claim got swept despite "+
				"deps >= SweepStructuralMin — structural-rescue branch broken or comparator drifted.", K)
		}
	}

	// GAP check: at deps == K with recent activity, boundary-recent MUST
	// fire as legacy_load_bearer. A comparator drift on the legacy side
	// (the `< LegacyMinDeps` continue at legacy.go:80 flipping to `<=`)
	// would make this claim silently skipped.
	_, vulns := analysis.Analyze(claims, edges, "p")
	fired := false
	for _, v := range vulns.Items {
		if v.Type == "legacy_load_bearer" {
			for _, cid := range v.ClaimIDs {
				if cid == "boundary-recent" {
					fired = true
				}
			}
		}
	}
	if !fired {
		t.Errorf("GAP at deps=K (=%d): boundary-recent claim did NOT fire as legacy_load_bearer despite "+
			"age=%v, recent reference (%v), and deps=%d (== LegacyMinDeps) — partition broken.",
			K, now.Sub(old), now.Sub(recentRef), K)
	}
}

// TestCoreParseTranscript_TriggersAutoSweep covers the integration this
// review pass identified as untested (R4): CoreParseTranscript must call
// SweepStaleClaimsDebounced after each successful parse. If a future
// refactor silently drops that call site, runtime hook logs would catch
// it but only after weeks of stale-claim accumulation. This test catches
// it at build time.
//
// Strategy: write an empty transcript so ExtractFromTranscript short-
// circuits (no API call needed), seed a stale-eligible claim, run
// CoreParseTranscript, then assert:
//   - the auto-sweep meta key was written for the project, AND
//   - the stale claim is now archived.
func TestCoreParseTranscript_TriggersAutoSweep(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir, "auto-sweep-test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Seed: one old, idle, weak-basis, no-deps claim — meets sweep criteria.
	old := time.Now().Add(-40 * 24 * time.Hour)
	stale := &types.Claim{
		ID: "stale-claim", Project: "auto-sweep-test", Text: "old vibes",
		Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
		Source: "", Confidence: 0.5, SessionID: "prior",
		CreatedAt: old, LastReferencedAt: old,
	}
	if err := db.CreateClaim(stale); err != nil {
		t.Fatalf("create claim: %v", err)
	}

	// Empty transcript: ExtractFromTranscript reads, finds no user/assistant
	// turns, returns empty chunk. CoreParseTranscript short-circuits the
	// extraction path without calling the Anthropic API.
	transcriptPath := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(""), 0600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Extractor with empty API key — won't be called given the empty chunk,
	// but New() needs the model to construct properly.
	ext := extract.New("test-key-unused", "claude-sonnet-4-6")

	_, err = CoreParseTranscript(context.Background(), db, ext,
		"auto-sweep-test", transcriptPath, 0, "test-session", 0)
	if err != nil {
		t.Fatalf("CoreParseTranscript: %v", err)
	}

	// Assertion 1: the meta key was written, signaling the auto-sweep path
	// was invoked. If a future refactor drops the SweepStaleClaimsDebounced
	// call from CoreParseTranscript, this assertion fails immediately.
	metaValue, ok := db.GetMeta("last_sweep_at:auto-sweep-test")
	if !ok {
		t.Fatal("auto-sweep meta key not written — CoreParseTranscript did not call SweepStaleClaimsDebounced")
	}
	if metaValue == "" {
		t.Error("auto-sweep meta key was empty")
	}

	// Assertion 2: the stale claim is now archived. Confirms the criteria
	// reach the live archival path, not just the meta write.
	got, _ := db.GetClaimsByProjectAll("auto-sweep-test")
	if len(got) != 1 {
		t.Fatalf("expected 1 claim total, got %d", len(got))
	}
	if !got[0].Archived {
		t.Errorf("expected stale claim to be archived after CoreParseTranscript ran, got archived=false")
	}
}

// TestCoreParseTranscript_AutoSweepRespectsDisable: with SLIMEMOLD_AUTO_SWEEP=off,
// the auto-sweep path must not run — meta key remains unwritten, stale claim
// remains active. Regression guard against accidentally inverting the env check.
func TestCoreParseTranscript_AutoSweepRespectsDisable(t *testing.T) {
	t.Setenv("SLIMEMOLD_AUTO_SWEEP", "off")

	dir := t.TempDir()
	db, err := store.Open(dir, "disable-test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	old := time.Now().Add(-40 * 24 * time.Hour)
	stale := &types.Claim{
		ID: "stale", Project: "disable-test", Text: "old vibes",
		Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
		Source: "", Confidence: 0.5, SessionID: "prior",
		CreatedAt: old, LastReferencedAt: old,
	}
	if err := db.CreateClaim(stale); err != nil {
		t.Fatalf("create: %v", err)
	}

	transcriptPath := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(""), 0600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	ext := extract.New("test-key-unused", "claude-sonnet-4-6")
	_, err = CoreParseTranscript(context.Background(), db, ext,
		"disable-test", transcriptPath, 0, "test-session", 0)
	if err != nil {
		t.Fatalf("CoreParseTranscript: %v", err)
	}

	// Meta key must NOT exist — auto-sweep was disabled.
	if v, ok := db.GetMeta("last_sweep_at:disable-test"); ok {
		t.Errorf("expected meta key NOT to exist with SLIMEMOLD_AUTO_SWEEP=off, but got value=%q", v)
	}

	// Stale claim must remain active.
	got, _ := db.GetClaimsByProject("disable-test")
	if len(got) != 1 {
		t.Fatalf("expected 1 active claim with auto-sweep disabled, got %d (indexing got[0] below would panic)", len(got))
	}
	if got[0].Archived {
		t.Errorf("expected stale claim to remain active with auto-sweep disabled, got archived=true")
	}
}
