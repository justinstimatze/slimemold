package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// openTestDB creates an isolated DB in t.TempDir(). Each test gets its own
// project to avoid cross-test contamination.
func openTestDB(t *testing.T, project string) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "data"), project)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSweepCandidates_AgeAndIdleThresholds(t *testing.T) {
	now := time.Now()
	claims := []types.Claim{
		// Old + idle + weak basis + no deps → archive
		{ID: "a", Project: "p", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: now.Add(-40 * 24 * time.Hour), LastReferencedAt: now.Add(-35 * 24 * time.Hour)},
		// Old but recently referenced → keep (idle threshold not met)
		{ID: "b", Project: "p", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: now.Add(-40 * 24 * time.Hour), LastReferencedAt: now.Add(-2 * 24 * time.Hour)},
		// Young → keep (age threshold not met)
		{ID: "c", Project: "p", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: now.Add(-5 * 24 * time.Hour), LastReferencedAt: now.Add(-5 * 24 * time.Hour)},
	}
	ids := SweepCandidates(claims, nil)
	if len(ids) != 1 || ids[0] != "a" {
		t.Errorf("expected only 'a' archived, got %v", ids)
	}
}

func TestSweepCandidates_WeakBasisGuard(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	claims := []types.Claim{
		// Strong-basis (research) with no deps → KEEP. Strong claims earn
		// their place by grounding, not by being referenced.
		{ID: "research", Speaker: types.SpeakerAssistant, Basis: types.BasisResearch,
			CreatedAt: old, LastReferencedAt: old},
		{ID: "empirical", Speaker: types.SpeakerAssistant, Basis: types.BasisEmpirical,
			CreatedAt: old, LastReferencedAt: old},
		{ID: "definition", Speaker: types.SpeakerAssistant, Basis: types.BasisDefinition,
			CreatedAt: old, LastReferencedAt: old},
		{ID: "convention", Speaker: types.SpeakerAssistant, Basis: types.BasisConvention,
			CreatedAt: old, LastReferencedAt: old},
		// Weak-basis with no deps → ARCHIVE.
		{ID: "vibes", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: old, LastReferencedAt: old},
	}
	ids := SweepCandidates(claims, nil)
	if len(ids) != 1 || ids[0] != "vibes" {
		t.Errorf("expected only 'vibes' archived (strong-basis preserved), got %v", ids)
	}
}

func TestSweepCandidates_ClosedOverridesEverything(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	claims := []types.Claim{
		// Closed + old + research basis + has plenty of deps → ARCHIVE.
		// The model explicitly resolved it; the strong basis and dep count
		// don't override that signal.
		{ID: "closed-research", Speaker: types.SpeakerAssistant, Basis: types.BasisResearch,
			CreatedAt: old, LastReferencedAt: old, Closed: true},
		{ID: "d1"}, {ID: "d2"}, {ID: "d3"},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "closed-research", Relation: types.RelDependsOn},
		{ID: "e2", FromID: "d2", ToID: "closed-research", Relation: types.RelDependsOn},
		{ID: "e3", FromID: "d3", ToID: "closed-research", Relation: types.RelSupports},
	}
	ids := SweepCandidates(claims, edges)
	if len(ids) != 1 || ids[0] != "closed-research" {
		t.Errorf("expected closed claim archived regardless of strong basis + deps, got %v", ids)
	}
}

func TestSweepCandidates_StructuralDepsRescue(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	claims := []types.Claim{
		// Old vibes claim WITH structural deps → KEEP (it's load-bearing
		// even though weak-basis; the legacy_load_bearer detector handles it).
		{ID: "weak-but-load-bearing", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: old, LastReferencedAt: old},
		{ID: "d1"}, {ID: "d2"},
	}
	edges := []types.Edge{
		{ID: "e1", FromID: "d1", ToID: "weak-but-load-bearing", Relation: types.RelDependsOn},
		{ID: "e2", FromID: "d2", ToID: "weak-but-load-bearing", Relation: types.RelSupports},
	}
	ids := SweepCandidates(claims, edges)
	if len(ids) != 0 {
		t.Errorf("expected weak-basis claim with 2 incoming deps to be kept, got archived: %v", ids)
	}
}

func TestSweepCandidates_AlreadyArchivedSkipped(t *testing.T) {
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	claims := []types.Claim{
		{ID: "already-archived", Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			CreatedAt: old, LastReferencedAt: old, Archived: true},
	}
	ids := SweepCandidates(claims, nil)
	if len(ids) != 0 {
		t.Errorf("expected already-archived claim to be skipped (idempotency), got %v", ids)
	}
}

func TestArchiveAndUnarchive_RoundTrip(t *testing.T) {
	db := openTestDB(t, "rt")
	now := time.Now()
	claim := &types.Claim{
		ID:         "x",
		Text:       "Test claim",
		Project:    "rt",
		Speaker:    types.SpeakerAssistant,
		Basis:      types.BasisVibes,
		Source:     "test",
		Confidence: 0.5,
		SessionID:  "s1",
		CreatedAt:  now,
	}
	if err := db.CreateClaim(claim); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Default queries see it
	got, _ := db.GetClaimsByProject("rt")
	if len(got) != 1 {
		t.Fatalf("expected 1 active claim, got %d", len(got))
	}
	// Archive it
	n, err := db.ArchiveClaims("rt", []string{"x"})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row archived, got %d", n)
	}
	// Default query now excludes it
	if got, _ := db.GetClaimsByProject("rt"); len(got) != 0 {
		t.Errorf("expected 0 active claims post-archive, got %d", len(got))
	}
	// All-view still includes it (with Archived=true)
	all, _ := db.GetClaimsByProjectAll("rt")
	if len(all) != 1 {
		t.Errorf("expected GetClaimsByProjectAll to still return archived claim, got %d", len(all))
	}
	if !all[0].Archived {
		t.Errorf("expected Archived=true on retrieved claim")
	}
	// Unarchive restores it to the default view
	n, err = db.UnarchiveClaims("rt", []string{"x"})
	if err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row unarchived, got %d", n)
	}
	if got, _ := db.GetClaimsByProject("rt"); len(got) != 1 {
		t.Errorf("expected 1 active claim post-unarchive, got %d", len(got))
	}
}

func TestArchiveClaims_ProjectScopedDefensively(t *testing.T) {
	db := openTestDB(t, "host")
	now := time.Now()
	// Two claims, different projects, in the same DB file (CoreParseTranscript
	// keeps everything in one DB per CWD-resolved project).
	for _, p := range []string{"host", "stranger"} {
		c := &types.Claim{
			ID: "c-" + p, Text: "x", Project: p, Speaker: types.SpeakerAssistant,
			Basis: types.BasisVibes, Source: "test", Confidence: 0.5,
			SessionID: "s", CreatedAt: now,
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
	}
	// ArchiveClaims called with project=host and the OTHER project's IDs
	// must not affect them — project filter is defensive.
	n, err := db.ArchiveClaims("host", []string{"c-stranger"})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows archived when ID belongs to a different project, got %d", n)
	}
	got, _ := db.GetClaimsByProjectAll("stranger")
	if len(got) != 1 {
		t.Fatalf("expected 1 claim for stranger, got %d (indexing got[0] below would panic)", len(got))
	}
	if got[0].Archived {
		t.Errorf("expected stranger's claim to remain active, got Archived=%v", got[0].Archived)
	}
}

func TestSweepStaleClaimsDebounced_RespectsInterval(t *testing.T) {
	db := openTestDB(t, "dbnc")
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	c := &types.Claim{
		ID: "stale", Text: "old vibes", Project: "dbnc", Speaker: types.SpeakerAssistant,
		Basis: types.BasisVibes, Source: "", Confidence: 0.5, SessionID: "s",
		CreatedAt: old, LastReferencedAt: old,
	}
	if err := db.CreateClaim(c); err != nil {
		t.Fatalf("create: %v", err)
	}
	// First call: should run and archive.
	n, overflow, ran, err := db.SweepStaleClaimsDebounced("dbnc", 24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if !ran || n != 1 || overflow != 0 {
		t.Errorf("expected ran=true n=1 overflow=0, got ran=%v n=%d overflow=%d", ran, n, overflow)
	}
	// Second call within the interval: should NOT run.
	n, _, ran, err = db.SweepStaleClaimsDebounced("dbnc", 24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if ran {
		t.Errorf("expected debounce to skip second call, but it ran (n=%d)", n)
	}
	// With minInterval=0 the debounce is bypassed.
	_, _, ran, err = db.SweepStaleClaimsDebounced("dbnc", 0, 1000)
	if err != nil {
		t.Fatalf("forced sweep: %v", err)
	}
	if !ran {
		t.Errorf("expected ran=true with zero minInterval")
	}
}

// TestSweepStaleClaims_CapAndOverflow exercises the per-fire archive cap:
// with many candidates and cap=2, exactly 2 should archive, with the
// oldest preferred. overflow should report the rest.
func TestSweepStaleClaims_CapAndOverflow(t *testing.T) {
	db := openTestDB(t, "cap")
	now := time.Now()
	// Five stale candidates, ages 40d / 50d / 60d / 70d / 80d.
	ages := []int{40, 50, 60, 70, 80}
	for i, days := range ages {
		c := &types.Claim{
			ID: string(rune('a' + i)), Project: "cap", Text: "stale",
			Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			Source: "", Confidence: 0.5, SessionID: "s",
			CreatedAt:        now.Add(-time.Duration(days) * 24 * time.Hour),
			LastReferencedAt: now.Add(-time.Duration(days) * 24 * time.Hour),
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create %v: %v", c.ID, err)
		}
	}
	archived, overflow, err := db.SweepStaleClaims("cap", 2)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if archived != 2 {
		t.Errorf("expected 2 archived, got %d", archived)
	}
	if overflow != 3 {
		t.Errorf("expected overflow=3 (5 candidates - 2 cap), got %d", overflow)
	}
	// Oldest-first: claims 'e' (80d) and 'd' (70d) should be the ones archived.
	all, _ := db.GetClaimsByProjectAll("cap")
	archivedIDs := make(map[string]bool)
	for _, c := range all {
		if c.Archived {
			archivedIDs[c.ID] = true
		}
	}
	if !archivedIDs["e"] || !archivedIDs["d"] {
		t.Errorf("expected oldest two (e, d) archived; got %v", archivedIDs)
	}
}

// TestCreateEdge_TouchesLastReferencedAt verifies the load-bearing behavior
// behind the sweep: when a new edge is created, BOTH endpoints' last_referenced_at
// timestamps must advance, otherwise the sweep would mistake currently-active
// claims for stale ones. Regression guard against silently dropping the touch.
func TestCreateEdge_TouchesLastReferencedAt(t *testing.T) {
	db := openTestDB(t, "edge-touch")
	// Two claims, both created with last_referenced_at set to an old value
	// so we can detect the touch unambiguously.
	old := time.Now().Add(-10 * 24 * time.Hour)
	for _, id := range []string{"a", "b"} {
		c := &types.Claim{
			ID: id, Project: "edge-touch", Text: id,
			Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			Source: "", Confidence: 0.5, SessionID: "s",
			CreatedAt: old, LastReferencedAt: old,
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Create an edge from a -> b. Both endpoints should be touched.
	before := time.Now()
	created, err := db.CreateEdge(&types.Edge{
		FromID: "a", ToID: "b", Relation: types.RelDependsOn,
		Strength: 1.0, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create edge: %v", err)
	}
	if !created {
		t.Fatal("expected edge to be created")
	}
	got, _ := db.GetClaimsByProject("edge-touch")
	for _, c := range got {
		if !c.LastReferencedAt.After(before.Add(-time.Second)) {
			t.Errorf("claim %s: last_referenced_at = %v, expected after %v",
				c.ID, c.LastReferencedAt, before)
		}
	}
}

// TestGetEdgesByProject_FiltersArchivedEndpoints verifies the join change:
// edges where either endpoint is archived must not appear. If they did,
// adjacency/findOrphans/findClusters would all see dangling references to
// claims that GetClaimsByProject excluded.
func TestGetEdgesByProject_FiltersArchivedEndpoints(t *testing.T) {
	db := openTestDB(t, "edge-filter")
	now := time.Now()
	for _, id := range []string{"keep", "archived"} {
		c := &types.Claim{
			ID: id, Project: "edge-filter", Text: id,
			Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			Source: "", Confidence: 0.5, SessionID: "s",
			CreatedAt: now, LastReferencedAt: now,
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if _, err := db.CreateEdge(&types.Edge{
		FromID: "keep", ToID: "archived", Relation: types.RelSupports,
		Strength: 1.0, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create edge: %v", err)
	}
	// Pre-archive: edge visible.
	if edges, _ := db.GetEdgesByProject("edge-filter"); len(edges) != 1 {
		t.Errorf("pre-archive: expected 1 edge, got %d", len(edges))
	}
	// Archive the to-endpoint.
	if _, err := db.ArchiveClaims("edge-filter", []string{"archived"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Post-archive: edge filtered out (otherwise findOrphans would see a
	// dangling reference to a claim GetClaimsByProject already hid).
	if edges, _ := db.GetEdgesByProject("edge-filter"); len(edges) != 0 {
		t.Errorf("post-archive: expected 0 edges (one endpoint archived), got %d", len(edges))
	}
}

// TestSweepStaleClaimsDebounced_OverflowAdjustsStamp exercises the
// "drain backlog faster when cap hit" branch: when overflow > 0, the
// next-fire stamp is set to now - minInterval/2 so a 24h debounce
// becomes effectively 12h until the backlog catches up. A sign-flip
// (stamp = now + minInterval/2) would PUSH the next fire farther out
// — the opposite of intent — and pass every existing test.
func TestSweepStaleClaimsDebounced_OverflowAdjustsStamp(t *testing.T) {
	db := openTestDB(t, "overflow-stamp")
	now := time.Now()
	old := now.Add(-50 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		c := &types.Claim{
			ID: string(rune('a' + i)), Project: "overflow-stamp", Text: "stale",
			Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			Source: "", Confidence: 0.5, SessionID: "s",
			CreatedAt: old, LastReferencedAt: old,
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	interval := 24 * time.Hour
	preCall := time.Now()
	n, overflow, ran, err := db.SweepStaleClaimsDebounced("overflow-stamp", interval, 2)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !ran || n != 2 || overflow != 3 {
		t.Fatalf("expected ran=true n=2 overflow=3, got ran=%v n=%d overflow=%d", ran, n, overflow)
	}
	v, ok := db.GetMeta("last_sweep_at:overflow-stamp")
	if !ok {
		t.Fatal("expected meta key written")
	}
	stamp, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatalf("parse stamp: %v", err)
	}
	// Stamp should be ≈ now - interval/2 (so debounce expires in ~12h, not ~24h).
	// Pinning to the preCall reference keeps the bound tight without flakiness:
	// stamp must be before (preCall - interval/2 + 1m) and after (preCall - interval/2 - 1m).
	expectedStamp := preCall.Add(-interval / 2)
	delta := stamp.Sub(expectedStamp)
	if delta < -2*time.Minute || delta > 2*time.Minute {
		t.Errorf("expected stamp ≈ now - interval/2 (%v), got %v (delta %v) — sign-flip on overflow branch?",
			expectedStamp.Format(time.RFC3339), stamp.Format(time.RFC3339), delta)
	}
	// Sanity: stamp must be in the PAST relative to now, not the future.
	if stamp.After(time.Now()) {
		t.Errorf("stamp %v is in the future — overflow branch flipped sign", stamp)
	}
}

// TestSweepStaleClaims_NoCap exercises cap<1 meaning "no cap" — all
// candidates archived in one pass.
func TestSweepStaleClaims_NoCap(t *testing.T) {
	db := openTestDB(t, "nocap")
	now := time.Now()
	old := now.Add(-50 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		c := &types.Claim{
			ID: string(rune('a' + i)), Project: "nocap", Text: "stale",
			Speaker: types.SpeakerAssistant, Basis: types.BasisVibes,
			Source: "", Confidence: 0.5, SessionID: "s",
			CreatedAt: old, LastReferencedAt: old,
		}
		if err := db.CreateClaim(c); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	archived, overflow, err := db.SweepStaleClaims("nocap", 0)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if archived != 5 {
		t.Errorf("expected all 5 archived with no cap, got %d", archived)
	}
	if overflow != 0 {
		t.Errorf("expected overflow=0, got %d", overflow)
	}
}
