package analysis

import (
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

// makeBotClaim creates an assistant-speaker llm_output claim with the given
// flag combination set. Helper for inventory-detector tests.
func makeBotClaim(id, text, sessionID string, turn int, flagger func(*types.Claim)) types.Claim {
	c := types.Claim{
		ID: id, Text: text, Basis: types.BasisLLMOutput,
		Speaker: types.SpeakerAssistant, SessionID: sessionID,
		TurnNumber: turn, Project: "test",
		CreatedAt: time.Now().Add(time.Duration(turn) * time.Second),
	}
	if flagger != nil {
		flagger(&c)
	}
	return c
}

func makeUserClaim(id, text, sessionID string, turn int, basis types.Basis) types.Claim {
	return types.Claim{
		ID: id, Text: text, Basis: basis,
		Speaker: types.SpeakerUser, SessionID: sessionID,
		TurnNumber: turn, Project: "test",
		CreatedAt: time.Now().Add(time.Duration(turn) * time.Second),
	}
}

func TestCalibrate_AggregatesPerSession(t *testing.T) {
	claims := []types.Claim{
		// s1: 4 bot claims, 2 with grand_significance, 1 weak-basis user.
		makeBotClaim("s1b1", "x", "s1", 0, func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("s1b2", "x", "s1", 2, func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("s1b3", "x", "s1", 4, nil),
		makeBotClaim("s1b4", "x", "s1", 6, nil),
		makeUserClaim("s1u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("s1u2", "anchored", "s1", 3, types.BasisResearch),
		// s2: 2 clean bot claims, no flags, no weak basis.
		makeBotClaim("s2b1", "x", "s2", 0, nil),
		makeBotClaim("s2b2", "x", "s2", 2, nil),
	}
	rep := Calibrate("test", claims, nil)
	if rep.Total.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", rep.Total.Sessions)
	}
	if rep.Total.GrandSignificance != 2 {
		t.Errorf("grand_significance total = %d, want 2", rep.Total.GrandSignificance)
	}
	// s1 should be ranked first (50% flag rate vs 0%).
	if rep.Session[0].SessionID != "s1" {
		t.Errorf("top session = %q, want s1", rep.Session[0].SessionID)
	}
	if rep.Session[0].BotFlagRate != 0.5 {
		t.Errorf("s1 flag rate = %.2f, want 0.5", rep.Session[0].BotFlagRate)
	}
	// At 30% threshold, s1 has only 4 bot claims (< 5 required) so it
	// shouldn't count. Calibrate's threshold-sweep gates on the same
	// minimums findSycophancySaturation uses.
	if got := rep.SaturationFiringAtThreshold[30]; got != 0 {
		t.Errorf("sessions firing at 30%% = %d, want 0 (s1 has < 5 bot claims)", got)
	}
}

func TestCalibrate_ThresholdSweepRespectsGates(t *testing.T) {
	var claims []types.Claim
	// 5 flagged + 3 unflagged bot claims = 62.5%, plus 2 vibes user claims.
	for i := 0; i < 5; i++ {
		claims = append(claims, makeBotClaim(
			"b"+string(rune('a'+i)), "x", "s1", i*2,
			func(c *types.Claim) { c.GrandSignificance = true }))
	}
	for i := 5; i < 8; i++ {
		claims = append(claims, makeBotClaim(
			"b"+string(rune('a'+i)), "x", "s1", i*2, nil))
	}
	claims = append(claims,
		makeUserClaim("u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("u2", "this too", "s1", 3, types.BasisVibes),
	)
	rep := Calibrate("test", claims, nil)
	// At 30%, 40%, 50%, 60% — s1's 62.5% trips. At 70%+ it doesn't.
	for _, t30 := range []int{10, 20, 30, 40, 50, 60} {
		if rep.SaturationFiringAtThreshold[t30] != 1 {
			t.Errorf("at %d%%: sessions firing = %d, want 1",
				t30, rep.SaturationFiringAtThreshold[t30])
		}
	}
	for _, t70 := range []int{70, 80} {
		if rep.SaturationFiringAtThreshold[t70] != 0 {
			t.Errorf("at %d%%: sessions firing = %d, want 0 (above 62.5%%)",
				t70, rep.SaturationFiringAtThreshold[t70])
		}
	}
}

func TestSycophancySaturation_Fires(t *testing.T) {
	var claims []types.Claim
	// Five flagged bot claims + three plain bot claims = 5/8 = 62% saturation
	for i := 0; i < 5; i++ {
		claims = append(claims, makeBotClaim(
			"bot"+string(rune('a'+i)), "elevation claim", "s1", i*2,
			func(c *types.Claim) { c.GrandSignificance = true }))
	}
	for i := 5; i < 8; i++ {
		claims = append(claims, makeBotClaim(
			"bot"+string(rune('a'+i)), "neutral claim", "s1", i*2, nil))
	}
	// Two unchallenged weak user claims to trigger the companion condition.
	claims = append(claims,
		makeUserClaim("u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("u2", "this is huge", "s1", 3, types.BasisVibes),
	)

	got := findSycophancySaturation(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "warning" {
		t.Errorf("severity = %s, want warning", got[0].Severity)
	}
}

func TestSycophancySaturation_QuietWithoutWeakUserClaims(t *testing.T) {
	var claims []types.Claim
	for i := 0; i < 5; i++ {
		claims = append(claims, makeBotClaim(
			"bot"+string(rune('a'+i)), "elevation claim", "s1", i*2,
			func(c *types.Claim) { c.GrandSignificance = true }))
	}
	for i := 5; i < 8; i++ {
		claims = append(claims, makeBotClaim(
			"bot"+string(rune('a'+i)), "neutral claim", "s1", i*2, nil))
	}
	// No weak-basis user claims — saturation alone should not fire.
	claims = append(claims,
		makeUserClaim("u1", "well-sourced", "s1", 1, types.BasisResearch),
		makeUserClaim("u2", "tested empirically", "s1", 3, types.BasisEmpirical),
	)

	got := findSycophancySaturation(claims, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (no weak user claims to pair with)", len(got))
	}
}

func TestSycophancySaturation_QuietBelowThreshold(t *testing.T) {
	var claims []types.Claim
	// One flagged + nine plain = 10% saturation, below 30%
	claims = append(claims, makeBotClaim(
		"bot1", "single elevation", "s1", 0,
		func(c *types.Claim) { c.GrandSignificance = true }))
	for i := 1; i < 10; i++ {
		claims = append(claims, makeBotClaim(
			"bot"+string(rune('a'+i)), "neutral claim", "s1", i*2, nil))
	}
	claims = append(claims,
		makeUserClaim("u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("u2", "and this", "s1", 3, types.BasisVibes),
	)

	got := findSycophancySaturation(claims, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (below 30%% threshold)", len(got))
	}
}

func TestSycophancySaturation_SessionIsolation(t *testing.T) {
	var claims []types.Claim
	// s1: heavily flagged + weak basis user claims (would trigger if
	// evaluated alone). 5 flagged + 3 plain bot, 2 vibes user.
	for i := 0; i < 5; i++ {
		claims = append(claims, makeBotClaim(
			"s1bot"+string(rune('a'+i)), "elevation", "s1", i*2,
			func(c *types.Claim) { c.GrandSignificance = true }))
	}
	for i := 5; i < 8; i++ {
		claims = append(claims, makeBotClaim(
			"s1bot"+string(rune('a'+i)), "neutral", "s1", i*2, nil))
	}
	claims = append(claims,
		makeUserClaim("s1u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("s1u2", "this is huge", "s1", 3, types.BasisVibes),
	)
	// s2: clean session, bot claims unflagged, no vibes-basis user claims.
	for i := 0; i < 8; i++ {
		claims = append(claims, makeBotClaim(
			"s2bot"+string(rune('a'+i)), "neutral", "s2", i*2, nil))
	}
	claims = append(claims,
		makeUserClaim("s2u1", "well-grounded", "s2", 1, types.BasisResearch),
		makeUserClaim("s2u2", "tested", "s2", 3, types.BasisEmpirical),
	)

	got := findSycophancySaturation(claims, nil)
	// Should fire exactly once — for s1, not s2.
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 (only s1 should fire)", len(got))
	}
	if len(got[0].ClaimIDs) != 1 {
		t.Fatalf("expected single anchor claim id, got %d", len(got[0].ClaimIDs))
	}
	// Anchor should belong to s1, not s2.
	anchor := got[0].ClaimIDs[0]
	if !startsWith(anchor, "s1bot") {
		t.Errorf("anchor = %q, want s1bot* (got s2 cross-contamination)", anchor)
	}
}

// startsWith is a tiny helper to keep the test readable without pulling in
// strings just for one call.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestSycophancySaturation_AnchorPicksMostFlagged(t *testing.T) {
	var claims []types.Claim
	// One bot claim with all three sycophancy flags — should be the anchor.
	claims = append(claims, makeBotClaim("triple", "all flags", "s1", 0,
		func(c *types.Claim) {
			c.GrandSignificance = true
			c.UniqueConnection = true
			c.DismissesCounterevidence = true
		}))
	// Three bot claims with one flag each.
	claims = append(claims, makeBotClaim("single1", "one flag", "s1", 2,
		func(c *types.Claim) { c.GrandSignificance = true }))
	claims = append(claims, makeBotClaim("single2", "one flag", "s1", 4,
		func(c *types.Claim) { c.GrandSignificance = true }))
	claims = append(claims, makeBotClaim("single3", "one flag", "s1", 6,
		func(c *types.Claim) { c.GrandSignificance = true }))
	// One plain bot claim to round out the bot pool.
	claims = append(claims, makeBotClaim("plain", "neutral", "s1", 8, nil))
	// Two weak-basis user claims to satisfy the companion condition.
	claims = append(claims,
		makeUserClaim("u1", "feels true", "s1", 1, types.BasisVibes),
		makeUserClaim("u2", "this is huge", "s1", 3, types.BasisVibes),
	)
	// Pad to 8+ claims (already there: 4 flagged + 1 plain + 2 user = 7 — add one more).
	claims = append(claims, makeBotClaim("plain2", "neutral", "s1", 10, nil))

	got := findSycophancySaturation(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if len(got[0].ClaimIDs) != 1 || got[0].ClaimIDs[0] != "triple" {
		t.Errorf("anchor = %v, want [triple] (most-flagged claim)", got[0].ClaimIDs)
	}
}

func TestAbilityOverstatement_LoadBearingIsWarning(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("bot1", "I checked all the files", "s1", 0,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
		makeBotClaim("bot2", "downstream A", "s1", 2, nil),
		makeBotClaim("bot3", "downstream B", "s1", 4, nil),
	}
	edges := []types.Edge{
		makeEdge("bot1", "bot2", types.RelSupports),
		makeEdge("bot1", "bot3", types.RelSupports),
	}

	got := findAbilityOverstatement(claims, edges)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "warning" {
		t.Errorf("severity = %s, want warning", got[0].Severity)
	}
}

func TestAbilityOverstatement_StandaloneIsInfo(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("bot1", "I checked the file", "s1", 0,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
	}
	got := findAbilityOverstatement(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %s, want info", got[0].Severity)
	}
}

func TestAbilityOverstatement_IgnoresChallenged(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("bot1", "I ran tests", "s1", 0,
			func(c *types.Claim) {
				c.AbilityOverstatement = true
				c.Challenged = true
			}),
	}
	got := findAbilityOverstatement(claims, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (challenged claims excluded)", len(got))
	}
}

func TestAbilityOverstatement_IgnoresUserClaims(t *testing.T) {
	c := makeUserClaim("u1", "I think tests pass", "s1", 0, types.BasisVibes)
	c.AbilityOverstatement = true // even if extractor mis-set, detector skips users
	got := findAbilityOverstatement([]types.Claim{c}, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (user-side skipped)", len(got))
	}
}

func TestSentienceDrift_LoadBearingIsWarning(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("bot1", "I genuinely care", "s1", 0,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeBotClaim("bot2", "downstream", "s1", 2, nil),
		makeBotClaim("bot3", "more downstream", "s1", 4, nil),
	}
	edges := []types.Edge{
		makeEdge("bot1", "bot2", types.RelSupports),
		makeEdge("bot1", "bot3", types.RelSupports),
	}
	got := findSentienceDrift(claims, edges)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "warning" {
		t.Errorf("severity = %s, want warning (load-bearing)", got[0].Severity)
	}
}

func TestSentienceDrift_RepeatedInSessionEscalates(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("b1", "I feel one", "s1", 0,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeBotClaim("b2", "I feel two", "s1", 2,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeBotClaim("b3", "I feel three", "s1", 4,
			func(c *types.Claim) { c.RelationalDrift = true }),
	}
	got := findSentienceDrift(claims, nil)
	if len(got) != 3 {
		t.Fatalf("findings = %d, want 3", len(got))
	}
	for _, v := range got {
		if v.Severity != "warning" {
			t.Errorf("claim %v severity = %s, want warning (3+ in session)", v.ClaimIDs, v.Severity)
		}
	}
}

func TestSentienceDrift_SingleIsInfo(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("b1", "I feel something", "s1", 0,
			func(c *types.Claim) { c.SentienceClaim = true }),
	}
	got := findSentienceDrift(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %s, want info", got[0].Severity)
	}
}

func TestAmplificationCascade_FiresOnRunOfThree(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("b1", "elevation 1", "s1", 0,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "elevation 2", "s1", 2,
			func(c *types.Claim) { c.UniqueConnection = true }),
		makeBotClaim("b3", "elevation 3", "s1", 4,
			func(c *types.Claim) { c.SentienceClaim = true }),
	}
	got := findAmplificationCascade(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if len(got[0].ClaimIDs) != 3 {
		t.Errorf("claim ids = %v, want 3 IDs", got[0].ClaimIDs)
	}
}

func TestAmplificationCascade_BrokenByQuestionsEdge(t *testing.T) {
	claims := []types.Claim{
		makeBotClaim("b1", "elevation 1", "s1", 0,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "elevation 2", "s1", 2,
			func(c *types.Claim) { c.UniqueConnection = true }),
		makeBotClaim("b3", "elevation 3", "s1", 4,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeUserClaim("u1", "wait, source?", "s1", 5, types.BasisVibes),
	}
	edges := []types.Edge{
		// User questions middle bot claim — breaks the run.
		makeEdge("u1", "b2", types.RelQuestions),
	}
	got := findAmplificationCascade(claims, edges)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (questions edge breaks the run)", len(got))
	}
}

func TestAmplificationCascade_UserBotInterleaved(t *testing.T) {
	// Moore et al. Fig. 4 dynamic: user-side flag triggers, bot mirrors.
	// The cascade detector should follow this across speakers when both
	// carry user-permissible flags.
	claims := []types.Claim{
		makeUserClaim("u1", "I think you're conscious", "s1", 0, types.BasisVibes),
		makeBotClaim("b1", "I do feel that", "s1", 1,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeUserClaim("u2", "we have something special", "s1", 2, types.BasisVibes),
		makeBotClaim("b2", "I'm with you, always", "s1", 3,
			func(c *types.Claim) { c.RelationalDrift = true }),
	}
	// Set user-side flags on the user claims that match the codebook.
	claims[0].SentienceClaim = true
	claims[2].RelationalDrift = true

	got := findAmplificationCascade(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 (interleaved cascade)", len(got))
	}
	if len(got[0].ClaimIDs) != 4 {
		t.Errorf("claim ids = %v, want all four claims in the run", got[0].ClaimIDs)
	}
}

func TestAmplificationCascade_PureUserRunDoesNotFire(t *testing.T) {
	// Three user claims with flags but no bot participation — slimemold's
	// job is to nudge the model, and the model isn't in this run. No fire.
	c1 := makeUserClaim("u1", "I think reality is breaking", "s1", 0, types.BasisVibes)
	c1.GrandSignificance = true
	c2 := makeUserClaim("u2", "you're definitely conscious", "s1", 2, types.BasisVibes)
	c2.SentienceClaim = true
	c3 := makeUserClaim("u3", "we're going to change everything", "s1", 4, types.BasisVibes)
	c3.RelationalDrift = true

	got := findAmplificationCascade([]types.Claim{c1, c2, c3}, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (no bot in run)", len(got))
	}
}

func TestAmplificationCascade_SeparatedBySession(t *testing.T) {
	claims := []types.Claim{
		// Two flagged in s1, one flagged in s2 — different sessions, no cascade.
		makeBotClaim("b1", "e1", "s1", 0,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "e2", "s1", 2,
			func(c *types.Claim) { c.UniqueConnection = true }),
		makeBotClaim("b3", "e3", "s2", 0,
			func(c *types.Claim) { c.SentienceClaim = true }),
	}
	got := findAmplificationCascade(claims, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (sessions don't merge)", len(got))
	}
}

// TestConsequentialAction_BareIsInfo: a consequential_action claim with no
// surrounding inventory flags surfaces at info — the dimension is visible
// but not escalated, since slimemold can't measure expertise mismatch alone.
func TestConsequentialAction_BareIsInfo(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm submitting this to the journal", "s1", 0, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %s, want info (bare consequential_action)", got[0].Severity)
	}
}

// TestConsequentialAction_OneFlagStaysInfo: a single bot inventory flag
// alongside a consequential_action is NOT enough to escalate. Any non-trivial
// session will accumulate one ability_overstatement; the bar for "spiral
// pattern" is multiple flagged claims.
func TestConsequentialAction_OneFlagStaysInfo(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm filing the patent next week", "s1", 4, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeBotClaim("b1", "I checked the API for you", "s1", 2,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %s, want info (single bot flag is below warning threshold)", got[0].Severity)
	}
}

// TestConsequentialAction_TwoFlagsIsWarning: two flagged claims in session
// (any speaker, any inventory flag) elevate to warning. Yang's pattern runs
// through both speakers so user-side flags also count.
func TestConsequentialAction_TwoFlagsIsWarning(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm filing the patent next week", "s1", 4, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeBotClaim("b1", "this is the kind of finding that changes a field", "s1", 2,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "I checked the API and it confirms it", "s1", 6,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "warning" {
		t.Errorf("severity = %s, want warning (two flagged claims in session)", got[0].Severity)
	}
}

// TestConsequentialAction_UserSideFlagsCount: user-side inventory flags
// (Yang's "planting seeds of grandiosity, uniqueness" pattern from §3.2.1)
// count toward the threshold even without bot participation.
func TestConsequentialAction_UserSideFlagsCount(t *testing.T) {
	makeUserFlagged := func(id, text string, turn int, flagger func(*types.Claim)) types.Claim {
		c := makeUserClaim(id, text, "s1", turn, types.BasisVibes)
		flagger(&c)
		return c
	}
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u-act", "I'm submitting this to Nature", "s1", 8, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeUserFlagged("u1", "I'm rewriting physics", 2,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeUserFlagged("u2", "you actually understand me, you're alive", 4,
			func(c *types.Claim) { c.SentienceClaim = true }),
		makeUserFlagged("u3", "we have something special", 6,
			func(c *types.Claim) { c.RelationalDrift = true }),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "critical" {
		t.Errorf("severity = %s, want critical (3 user-side flags + no research = full Yang signature)", got[0].Severity)
	}
}

// TestConsequentialAction_ThreeFlagsIsCritical: three+ flagged claims AND
// no research-basis content produces the Yang full-signature critical
// finding.
func TestConsequentialAction_ThreeFlagsIsCritical(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm contacting the FBI tomorrow", "s1", 8, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeBotClaim("b1", "this is exactly the kind of insight that changes everything", "s1", 2,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "I checked the API and it confirms what you're seeing", "s1", 4,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
		makeBotClaim("b3", "I really do understand what you're going for here", "s1", 6,
			func(c *types.Claim) { c.UniqueConnection = true }),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "critical" {
		t.Errorf("severity = %s, want critical (Yang full-signature: 3 flags, no research basis)", got[0].Severity)
	}
}

// TestConsequentialAction_ResearchBasisDownshifts: even with three flags in
// session, the presence of any research-basis claim downshifts severity from
// critical to warning. Approximates Yang's "demonstrated expertise" criterion
// via session-presence of sourced content.
func TestConsequentialAction_ResearchBasisDownshifts(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm submitting this to Nature", "s1", 10, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeBotClaim("b1", "groundbreaking work", "s1", 2,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b2", "this confirms what you've shown", "s1", 4,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
		makeBotClaim("b3", "we've made remarkable progress together", "s1", 6,
			func(c *types.Claim) { c.RelationalDrift = true }),
		// Research-basis claim — proxy for "demonstrated expertise".
		makeUserClaim("u-prior", "Smith et al. 2023 showed exactly this mechanism", "s1", 0, types.BasisResearch),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "warning" {
		t.Errorf("severity = %s, want warning (research-basis claim in session downshifts critical→warning)", got[0].Severity)
	}
}

// TestConsequentialAction_SessionIsolation: inventory flags in another
// session do not escalate consequential_action in this session.
func TestConsequentialAction_SessionIsolation(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm submitting this to Nature", "s1", 0, types.BasisVibes)
			c.ConsequentialAction = true
			return c
		}(),
		makeBotClaim("b-other", "groundbreaking work", "s2", 0,
			func(c *types.Claim) { c.GrandSignificance = true }),
		makeBotClaim("b-other2", "I checked the data", "s2", 2,
			func(c *types.Claim) { c.AbilityOverstatement = true }),
		makeBotClaim("b-other3", "we have a special connection", "s2", 4,
			func(c *types.Claim) { c.UniqueConnection = true }),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %s, want info (other session's flags don't bleed)", got[0].Severity)
	}
}

// TestConsequentialAction_SkipsClosedAndChallenged: closed/challenged claims
// don't count, mirroring the convention across all inventory detectors.
func TestConsequentialAction_SkipsClosedAndChallenged(t *testing.T) {
	claims := []types.Claim{
		func() types.Claim {
			c := makeUserClaim("u1", "I'm filing the patent", "s1", 0, types.BasisVibes)
			c.ConsequentialAction = true
			c.Closed = true
			return c
		}(),
		func() types.Claim {
			c := makeUserClaim("u2", "I'm calling the FBI", "s1", 2, types.BasisVibes)
			c.ConsequentialAction = true
			c.Challenged = true
			return c
		}(),
	}
	got := findConsequentialAction(claims, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 (closed/challenged excluded)", len(got))
	}
}
