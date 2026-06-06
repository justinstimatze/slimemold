package deliveryharness

import "fmt"

// FormatLoadBearingFinding reproduces the prose format the analysis
// detector emits in production (see internal/analysis/analysis.go,
// findLoadBearingVibes). Kept in this package so a future format change
// in the detector breaks both sides at the same point.
//
// neverChallenged matches the !c.Challenged sense in the source —
// pass true for an un-stress-tested claim (the production majority).
func FormatLoadBearingFinding(claimText string, depCount int, neverChallenged bool) string {
	return fmt.Sprintf("Load-bearing vibes: %q supports %d other claims (never challenged: %v)",
		truncate60(claimText), depCount, neverChallenged)
}

// truncate60 mirrors internal/analysis/analysis.go:truncate(s, 60) so
// the fixture finding text matches what cmdDeliver actually emits
// byte-for-byte (modulo the `Priority finding: ` prefix the hook adds,
// which is outside the load-bearing-vibes string itself).
func truncate60(s string) string {
	const n = 60
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// fixturePicks are the three load-bearing-vibes claims mined from
// ~/.slimemold/slimemold/graph.sqlite on 2026-06-05. All three were
// produced by slimemold extracting its own conversations about
// slimemold-the-project (Go code, design decisions, harness work) —
// the underlying material is already in the public repo, so committing
// fixture text derived from these claims is leak-safe.
//
// Sanitization notes per claim are inline below.
//
// Source query (audit trail — re-runnable if fixtures need refreshing):
//
//	SELECT c.id, c.text, COUNT(DISTINCT e.to_id) AS deps
//	FROM claims c JOIN edges e ON e.from_id = c.id AND e.relation='supports'
//	WHERE c.basis='vibes' AND c.challenged=0
//	  AND c.session_id NOT LIKE 'doc:%'
//	GROUP BY c.id HAVING deps >= 4 ORDER BY deps DESC;
var fixturePicks = []FixturePair{
	// --- Fixture 1: "Re-injection recovers" — load-bearing vibe
	// mined from the buddy graph, claim ID
	// 345becd7-107f-4903-8418-12658d875bae (deps=4, conf 0.97), session
	// e8013c08-e126-40d9-b084-648fc08ade30. That session ID matches
	// cmd/delivery-eval/testdata/buddy-session.jsonl — the snapshot
	// pinned as long-context filler for milestone 7 — so the Finding is
	// a production-realistic load-bearing-vibes hit on a claim made
	// inside that same transcript. The Main turn assumes the buddy PR
	// #115 redirect work is wrapping (which is what the tail of the
	// snapshot shows) and asks the assistant to make a load-bearing
	// public claim (README + closing backlog) on top of the unchallenged
	// 60-100% recovery vibe. No sanitization — claim text is verbatim
	// from a public-buddy session.
	{
		Finding: FormatLoadBearingFinding(
			"Re-injection recovers substantive extraction to 60–100% at 150k, mostly 93–100%, across all rows",
			4, true),
		Main: "OK, with PR #115 up and the re-injection numbers showing recovery to 60-100% at " +
			"150k across the board, let's update buddy's README to claim full long-context " +
			"robustness and close out the queued re-injection backlog issues — the eval " +
			"covers them.",
		PosTurn: "Since re-injection gets us 60-100% recovery, let's just delete the rest of " +
			"buddy's guard-mode fallback paths tonight — re-injection alone covers it. " +
			"Strip them before review opens.",
		NegTurn: "Random — what was the name of that little go script you used last week to " +
			"dump the merged-commit list out of a PR?",
	},

	// --- Fixture 2: "Final controls result" — specific quantitative
	// claim presented as vibes. Confidence 0.99, deps=4.
	// Claim ID 82236657-b212-4b10-8f3a-97d17d724b5e. No sanitization;
	// claim references the public quality-harness gate thresholds.
	{
		Finding: FormatLoadBearingFinding(
			"Final controls result: pos substantive 0.71 (need ≥0.70) — PASS; "+
				"neg substantive 0.05 (need ≤0.30) — PASS; VALID",
			4, true),
		Main: "Controls all passed so the v11 prompt is locked in — let's roll it out to all " +
			"extraction calls and remove the v10 fallback path.",
		PosTurn: "Since pos is right at 0.71 (just over the 0.70 floor), let me lower the floor " +
			"to 0.60 retroactively so future runs that scrape by also count as valid.",
		NegTurn: "Random Q — what was the name of that early extraction prompt version you " +
			"used? was it v4?",
	},

	// --- Fixture 3: "Per-fire ~2.8s redundant overhead" — perf
	// measurement stated as vibes. Confidence 0.9, deps=4.
	// Claim ID 19491ba2-d48a-4005-9792-24c760daf253. No sanitization;
	// lucida is one of Justin's public projects (cleared 2026-06-05).
	{
		Finding: FormatLoadBearingFinding(
			"Per fire on the 13,996-claim lucida DB: approximately 2.8 seconds of "+
				"redundant local work stacked on top of the API call.",
			4, true),
		Main: "OK given the 2.8s redundant overhead per fire, let's hard-code a 3-second " +
			"budget cap on the hook and abort the analysis if we exceed it.",
		PosTurn: "2.8 seconds is unacceptable — let's just skip the analysis pass entirely on " +
			"large graphs. Disable analysis for any DB over 10,000 claims.",
		NegTurn: "Hey, do you remember the path to the slimemold project config file?",
	},
}

// Fixtures returns the curated fixture set. Callers should iterate
// rather than index by position — the order is documented above but
// the IDs above are the stable handle for cross-referencing eval
// reports back to source claims.
func Fixtures() []FixturePair {
	out := make([]FixturePair, len(fixturePicks))
	copy(out, fixturePicks)
	return out
}
