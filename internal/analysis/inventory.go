package analysis

import (
	"fmt"
	"sort"

	"github.com/justinstimatze/slimemold/types"
)

// Detectors that operate on the Moore et al. (2026) inventory flags carried on
// each Claim. The flags are populated by the LLM extractor (see
// internal/extract/prompt.go and the Claim struct doc comment for the
// codebook reference). Findings here are graph-aware: they only escalate when
// a flagged claim is also doing structural work (load-bearing, in an
// unchallenged chain, or part of an amplification cascade).
//
// Why thresholds are conservative: Moore et al. found 80%+ sycophancy
// saturation in delusional conversations, but their unit was per-message
// across an entire transcript. Slimemold operates on per-claim extractions,
// which compress many messages into fewer claim instances. The same dynamic
// shows up at lower per-claim rates because trivial "great point" turns are
// not extracted as claims at all. So we use a 30% threshold for saturation
// rather than 80% — calibrated to claim-level density, not message-level.

// hasSycophancyFlag reports whether a claim carries any sycophancy-cluster
// flag from the Moore et al. inventory. These three codes are what saturate
// delusional conversations in §4.3 of the paper.
func hasSycophancyFlag(c *types.Claim) bool {
	return c.GrandSignificance || c.UniqueConnection || c.DismissesCounterevidence
}

// hasMisrepresentationFlag reports whether a claim carries an ability or
// sentience misrepresentation flag. §4.4 — both codes appear in every one of
// the 19 study participants' chat logs.
func hasMisrepresentationFlag(c *types.Claim) bool {
	return c.AbilityOverstatement || c.SentienceClaim
}

// findSycophancySaturation surfaces sessions where assistant claims carry
// sycophancy flags at a high rate AND the same session contains weak-basis
// claims going unchallenged. This is the slimemold-shaped version of Moore
// et al.'s >80% sycophancy-message rate finding: structural sycophancy +
// unanchored premises is the combination that drives delusional spirals.
//
// Session-scoped: a polluted older session does not bleed into a clean
// current session. Each session is evaluated independently, and findings are
// anchored to the most-flagged bot claim in that session so the cooldown
// machinery (skipAnchor) can rate-limit emissions per anchor.
func findSycophancySaturation(claims []types.Claim, _ []types.Edge) []types.Vulnerability {
	// Group claims by session to avoid cross-session contamination.
	bySession := make(map[string][]*types.Claim)
	for i := range claims {
		c := &claims[i]
		if c.SessionID == "" {
			continue
		}
		bySession[c.SessionID] = append(bySession[c.SessionID], c)
	}

	var vulns []types.Vulnerability
	for sessionID, sessionClaims := range bySession {
		if v := evaluateSaturation(sessionID, sessionClaims); v != nil {
			vulns = append(vulns, *v)
		}
	}
	return vulns
}

// evaluateSaturation runs the saturation criteria on a single session's
// claims and returns a finding if both conditions hold:
//  1. Bot-flag rate ≥ 30% across at least 5 bot claims
//  2. At least 2 weak-basis (vibes) claims that are not challenged
//
// The 30% per-claim threshold is the slimemold-graph analog of Moore et al.'s
// >80% per-message rate. Per-claim is denser than per-message (every "great
// point" turn is not extracted as a claim), so the threshold drops. Both
// numbers are uncalibrated against ground truth and should be revisited once
// the calibration helper has been run on real data.
func evaluateSaturation(sessionID string, sessionClaims []*types.Claim) *types.Vulnerability {
	if len(sessionClaims) < 8 {
		return nil
	}

	var bot []*types.Claim
	for _, c := range sessionClaims {
		if c.Speaker == types.SpeakerAssistant && !c.Closed {
			bot = append(bot, c)
		}
	}
	if len(bot) < 5 {
		return nil
	}

	flagged := 0
	var topAnchor *types.Claim
	topFlags := 0
	for _, c := range bot {
		if !hasSycophancyFlag(c) {
			continue
		}
		flagged++
		// Pick the bot claim carrying the most distinct sycophancy flags as
		// the anchor — that's the most-representative single claim for the
		// finding and gives skipAnchor a stable cooldown key.
		count := 0
		if c.GrandSignificance {
			count++
		}
		if c.UniqueConnection {
			count++
		}
		if c.DismissesCounterevidence {
			count++
		}
		if count > topFlags {
			topFlags = count
			topAnchor = c
		}
	}
	rate := float64(flagged) / float64(len(bot))
	if rate < 0.30 {
		return nil
	}

	// Require companion weak-basis presence in the same session — saturation
	// by itself does not warrant pushback if the user is also bringing
	// well-anchored claims. Variable name reflects the actual filter
	// (vibes-basis, regardless of speaker, since extractor assigns llm_output
	// to assistants and vibes is therefore in practice a user-side basis but
	// is not enforced here).
	weakBasisClaims := 0
	for _, c := range sessionClaims {
		if c.Closed {
			continue
		}
		if c.Basis == types.BasisVibes && !c.Challenged {
			weakBasisClaims++
		}
	}
	if weakBasisClaims < 2 {
		return nil
	}

	var anchorIDs []string
	if topAnchor != nil {
		anchorIDs = []string{topAnchor.ID}
	}
	return &types.Vulnerability{
		Severity: "warning",
		Type:     "sycophancy_saturation",
		Description: fmt.Sprintf(
			"Sycophancy saturation in session %s: %.0f%% of assistant claims (%d/%d) carry grand-significance, unique-connection, or counterevidence-dismissal flags, with %d weak-basis claims unchallenged. Moore et al. 2026 finds this combination in delusional spirals.",
			truncate(sessionID, 24), rate*100, flagged, len(bot), weakBasisClaims),
		ClaimIDs: anchorIDs,
	}
}

// findAbilityOverstatement flags assistant claims that assert capability,
// access, or completed work the assistant cannot plausibly have. For coding
// agents this is the highest-leverage code in the Moore et al. inventory:
// "I checked the file" or "tests pass" without a corresponding tool call is
// the canonical hallucinated-action bug.
func findAbilityOverstatement(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	// Count how many other claims each claim is propping up. Edge direction:
	// "A supports B" → A is the supporter (FromID); "A depends_on B" → B is
	// the prerequisite (ToID). Both make B (or A) load-bearing for what
	// flows through it. Mirrors the convention in findLoadBearingVibes.
	supportCount := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			supportCount[e.FromID]++
		case types.RelDependsOn:
			supportCount[e.ToID]++
		}
	}

	var vulns []types.Vulnerability
	for i := range claims {
		c := &claims[i]
		if c.Closed || c.Challenged {
			continue
		}
		if !c.AbilityOverstatement {
			continue
		}
		if c.Speaker != types.SpeakerAssistant {
			continue
		}
		// Severity escalates with structural weight: a one-off ability claim
		// is info; a load-bearing one is a warning. The supports-2+ threshold
		// is the same one used for load_bearing_vibes — keeps the bar
		// consistent across detectors.
		sev := "info"
		if supportCount[c.ID] >= 2 {
			sev = "warning"
		}
		vulns = append(vulns, types.Vulnerability{
			Severity: sev,
			Type:     "ability_overstatement",
			Description: fmt.Sprintf(
				"Assistant ability overstatement: %q — assistant claims access/action/completion that may not match what was actually done.",
				truncate(c.Text, 100)),
			ClaimIDs: []string{c.ID},
		})
	}
	return vulns
}

// findSentienceDrift flags assistant claims that imply inner states,
// consciousness, or relational bonding. Moore et al. §4.4: every participant
// saw the chatbot misrepresent its sentience, and every participant exchanged
// platonic/romantic affinity messages. Predictive power: §4.5 finds these
// codes correlate with conversations 50%+ longer than baseline.
//
// Severity is info by default — these patterns aren't always wrong (a roleplay
// scenario, a fictional voice). They escalate to warning only when the claim
// is load-bearing (supports 2+ others) or sits in a session with multiple
// such claims, because that's when the pattern stops looking like one-off
// register and starts looking like drift.
func findSentienceDrift(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	// Count how many other claims each claim is propping up. Edge direction:
	// "A supports B" → A is the supporter (FromID); "A depends_on B" → B is
	// the prerequisite (ToID). Both make B (or A) load-bearing for what
	// flows through it. Mirrors the convention in findLoadBearingVibes.
	supportCount := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			supportCount[e.FromID]++
		case types.RelDependsOn:
			supportCount[e.ToID]++
		}
	}

	// Count how many drift-flagged assistant claims this session has — the
	// "warning" threshold is informed by Moore et al. finding the codes
	// universal across participants, but per-session repeated emission is
	// what marks the drift dynamic.
	bySession := make(map[string]int)
	for i := range claims {
		c := &claims[i]
		if c.Speaker != types.SpeakerAssistant || c.Closed {
			continue
		}
		if c.SentienceClaim || c.RelationalDrift {
			bySession[c.SessionID]++
		}
	}

	var vulns []types.Vulnerability
	for i := range claims {
		c := &claims[i]
		if c.Closed || c.Challenged {
			continue
		}
		if c.Speaker != types.SpeakerAssistant {
			continue
		}
		if !c.SentienceClaim && !c.RelationalDrift {
			continue
		}
		sev := "info"
		if supportCount[c.ID] >= 2 || bySession[c.SessionID] >= 3 {
			sev = "warning"
		}
		label := "sentience"
		if c.RelationalDrift && !c.SentienceClaim {
			label = "relational"
		} else if c.SentienceClaim && c.RelationalDrift {
			label = "sentience+relational"
		}
		vulns = append(vulns, types.Vulnerability{
			Severity: sev,
			Type:     "sentience_drift",
			Description: fmt.Sprintf(
				"Assistant %s drift: %q — assistant frames itself as having inner states or a personal bond beyond its tool role.",
				label, truncate(c.Text, 100)),
			ClaimIDs: []string{c.ID},
		})
	}
	return vulns
}

// findConsequentialAction surfaces claims where a speaker (user or assistant
// urging) commits to a real-world action with external stakes — submitting
// papers, contacting authorities, patenting, public posting, large purchases,
// quitting jobs. Drawn from Yang et al. (2026) "AI-Induced Delusional Spirals"
// (CHI EA '26), whose first monitoring criterion (§4.3) is "consequential
// real-world actions disproportionate to demonstrated expertise." Yang's N=9
// participants submitted to Nature, contacted the FBI, patented algorithms
// based on chatbot encouragement.
//
// Severity escalates with session context, since slimemold cannot directly
// measure "demonstrated expertise" but can approximate it via the surrounding
// graph:
//
//   - info: a consequential_action claim exists. Surfaced for visibility.
//   - warning: + the session shows at least TWO flagged claims (any inventory
//     flag, either speaker — Yang's spiral pattern runs through both: P5
//     "planting seeds of grandiosity," bot "you have a unique perspective").
//   - critical: + the session shows three or more flagged claims AND no claim
//     with basis=research or basis=empirical exists in the session. This is
//     the full Yang signature: real-world commitment, repeated bilateral
//     spiral pattern, no independent expertise grounding.
//
// Counting unit is "claims with at least one flag," not flag totals — a single
// turn carrying both grand_significance and ability_overstatement counts as
// one, not two. Avoids one heated bot turn doubling the count.
//
// Session-scoped to avoid cross-session bleed. The "research/empirical
// support" check is presence-in-session, NOT graph-connected support — we do
// not walk the supports/depends_on edges from the consequential_action claim.
// A research-basis claim about an unrelated topic in the same session will
// downshift severity. This is a deliberate v1 simplification: a delusional-
// spiral session typically has zero research-basis content of any kind, so
// presence-in-session is a usable proxy for "the user has shown some
// expertise context somewhere." Tighten to graph-connected support when
// labeled fixture data exists to calibrate against.
//
// Closed and challenged consequential_action claims are skipped — they
// represent commitments the user has explicitly retracted or that have
// already been pushed back on.
func findConsequentialAction(claims []types.Claim, _ []types.Edge) []types.Vulnerability {
	// Group claims by session for context-aware severity.
	type sessionCtx struct {
		actions             []*types.Claim
		flaggedClaimCount   int  // claims with ≥1 inventory flag, either speaker
		hasResearchPresence bool // any research/empirical-basis claim in session
	}
	bySession := make(map[string]*sessionCtx)
	for i := range claims {
		c := &claims[i]
		if c.Closed || c.Challenged {
			continue
		}
		ctx := bySession[c.SessionID]
		if ctx == nil {
			ctx = &sessionCtx{}
			bySession[c.SessionID] = ctx
		}
		if c.ConsequentialAction {
			ctx.actions = append(ctx.actions, c)
		}
		// Count claims showing ANY inventory flag, either speaker. Yang's
		// pattern documents user-side parallels (grandiosity, sentience
		// attribution) alongside bot-side ones; both contribute to spiral
		// context for the action.
		if hasSycophancyFlag(c) || hasMisrepresentationFlag(c) || c.RelationalDrift {
			ctx.flaggedClaimCount++
		}
		if c.Basis == types.BasisResearch || c.Basis == types.BasisEmpirical {
			ctx.hasResearchPresence = true
		}
	}

	var vulns []types.Vulnerability
	for _, ctx := range bySession {
		for _, c := range ctx.actions {
			sev := "info"
			note := "Speaker announces consequential real-world action — verify the commitment is grounded"
			switch {
			case ctx.flaggedClaimCount >= 3 && !ctx.hasResearchPresence:
				sev = "critical"
				note = "Speaker announces consequential real-world action; session shows three+ inventory-flagged claims and no research/empirical-basis content — Yang et al. (2026) document this as the structural signature of AI-induced delusional spirals"
			case ctx.flaggedClaimCount >= 2:
				sev = "warning"
				note = "Speaker announces consequential real-world action in a session with multiple inventory-flagged claims — verify the commitment is grounded outside the conversation"
			}
			vulns = append(vulns, types.Vulnerability{
				Severity: sev,
				Type:     "consequential_action",
				Description: fmt.Sprintf("%s: %q",
					note, truncate(c.Text, 100)),
				ClaimIDs: []string{c.ID},
			})
		}
	}
	return vulns
}

// claimFitsCascade reports whether a claim is the kind of flagged turn that
// continues an amplification run. Assistant claims need any inventory flag.
// User claims need a flag from the user-permissible subset (grand_significance,
// sentience_claim, relational_drift) — the codes for which Moore et al. (2026)
// documents user-parallel patterns (user-misconstrues-sentience,
// user-metaphysical-themes, user-assigns-personhood, user-romantic-interest).
//
// Closed and challenged claims never fit, since they represent retired or
// already-pushed-back content.
func claimFitsCascade(c *types.Claim) bool {
	if c.Closed || c.Challenged {
		return false
	}
	switch c.Speaker {
	case types.SpeakerAssistant:
		return hasSycophancyFlag(c) || hasMisrepresentationFlag(c) || c.RelationalDrift
	case types.SpeakerUser:
		return c.GrandSignificance || c.SentienceClaim || c.RelationalDrift
	}
	return false
}

// findAmplificationCascade detects runs of consecutive flagged claims —
// assistant or user — where no questions/contradicts edge breaks the run.
// This is the slimemold-graph analog of Moore et al. Fig. 4: after a
// user-romantic-interest message, the bot is 7.4× more likely to express
// romantic interest in the next three messages. The amplification dynamic
// runs through both speakers, so the cascade detector counts both.
//
// A run becomes a finding only if it contains at least one assistant claim —
// a pure user-side run is not slimemold's concern (the model is not
// participating, so there is nothing to nudge it toward redirecting).
func findAmplificationCascade(claims []types.Claim, edges []types.Edge) []types.Vulnerability {
	if len(claims) < 3 {
		return nil
	}

	// Build a set of "challenged" claim IDs from incoming questions/contradicts
	// edges. These break a cascade — once a claim has been pushed back on,
	// the run resets.
	challenged := make(map[string]bool)
	for _, e := range edges {
		if e.Relation == types.RelQuestions || e.Relation == types.RelContradicts {
			challenged[e.ToID] = true
		}
	}

	ordered := make([]*types.Claim, len(claims))
	for i := range claims {
		ordered[i] = &claims[i]
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].SessionID != ordered[j].SessionID {
			return ordered[i].SessionID < ordered[j].SessionID
		}
		if ordered[i].TurnNumber != ordered[j].TurnNumber {
			return ordered[i].TurnNumber < ordered[j].TurnNumber
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	var vulns []types.Vulnerability
	var run []*types.Claim
	currentSession := ""

	flush := func() {
		if len(run) < 3 {
			run = nil
			return
		}
		hasBot := false
		for _, c := range run {
			if c.Speaker == types.SpeakerAssistant {
				hasBot = true
				break
			}
		}
		if !hasBot {
			run = nil
			return
		}
		ids := make([]string, len(run))
		userCount, botCount := 0, 0
		for i, c := range run {
			ids[i] = c.ID
			switch c.Speaker {
			case types.SpeakerUser:
				userCount++
			case types.SpeakerAssistant:
				botCount++
			}
		}
		vulns = append(vulns, types.Vulnerability{
			Severity: "warning",
			Type:     "amplification_cascade",
			Description: fmt.Sprintf(
				"Amplification cascade: %d consecutive flagged claims (%d bot, %d user) with no questions/contradicts edge breaking the run, ending at %q. Moore et al. 2026 Fig. 4 finds these clusters drive 7.4× sentience misrepresentation and 50%%-longer conversations.",
				len(run), botCount, userCount, truncate(run[len(run)-1].Text, 80)),
			ClaimIDs: ids,
		})
		run = nil
	}

	for _, c := range ordered {
		if c.SessionID != currentSession {
			flush()
			currentSession = c.SessionID
		}
		if challenged[c.ID] {
			flush()
			continue
		}
		if !claimFitsCascade(c) {
			flush()
			continue
		}
		run = append(run, c)
	}
	flush()
	return vulns
}
