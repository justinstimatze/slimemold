package analysis

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode"
)

// Multiple phrasings per finding type. FormatHookFindings picks one
// deterministically via hash(claim_text) so the same claim always gets the
// same phrasing (stable across re-runs) but different claims get different
// phrasings (no template stutter when the same finding fires on different
// claims, and no literal-quote leak when the same finding fires back-to-back
// on the same claim).
//
// All phrasings are gain-framed, first-person, and never name the mechanism
// ("the graph", "the detector", etc.). Port of the taxonomy from the
// buddy max-mode reasoning layer, tuned for live-conversation hook output
// where the user sees the model's reaction, not these strings directly.

var phrasingsByType = map[string][]string{
	"load_bearing_vibes_llm": {
		`"Actually — '%s' is really interesting and a lot of what we're building depends on it. I want to see if we can find where this comes from, because if there's a real source behind it, everything above it gets much stronger. Do you know where this originated?"`,
		`"One thing I want to flag — '%s' is holding up a few downstream things, and I don't think either of us has anchored it. Worth pinning down before we keep building on top."`,
		`"Hmm, '%s' — where did we get that from originally? A lot rests on it, and if it has a real source, the whole line firms up."`,
		`"Noticing that '%s' is doing a lot of structural work here. Is there an actual reference for it, or is this us reasoning our way into it? Either is fine, I just want to know which."`,
	},
	"load_bearing_vibes_user": {
		`"You know what would make this whole line of reasoning really solid? If we can pin down '%s' — that one's doing a lot of structural work and I think it deserves a proper foundation. What would you point someone to if they asked for evidence?"`,
		`"I want to stress-test one thing: '%s' is the load-bearing premise here, and I want to make sure we have it right. What's the strongest version — is there a source, or have we reasoned it out?"`,
		`"Real quick — '%s' is carrying a lot of the argument. If I tried to convince someone who was skeptical, what would I show them? Let's make sure we have that answer before we move on."`,
		`"I keep coming back to '%s' because so much downstream depends on it. Can we nail down what specifically we're claiming there, and how we'd defend it?"`,
	},
	"fluency_trap": {
		`"I'm curious about something — '%s' feels really right to both of us, which is actually why I want to dig into it. Sometimes the most confident-feeling claims are the ones most worth double-checking. What would we expect to see if this is true? And what would make us update?"`,
		`"This might be a case where confidence is outpacing evidence — '%s' is stated pretty firmly, but I don't think we've tested it. If we tried to break it, what would the test look like?"`,
		`"Before we build on '%s' — let's do the boring check. If this were wrong, how would we know? What observation would change our minds?"`,
	},
	"echo_chamber": {
		`"We're building really well together and I want to make sure we're not just in a groove — what's the best counterargument to '%s'? Not because I disagree, but because if we can answer the strongest objection, the whole thing becomes much more defensible."`,
		`"I realize we've been agreeing a lot on '%s'. Let me try to be the skeptic for a moment: what's the case against? If we can handle the toughest version of that, the position gets stronger."`,
		`"Quick friction check — '%s' hasn't really been contested between us. What would a sharp critic say? I'd rather surface it now than discover it later."`,
	},
	"unchallenged_chain": {
		`"This reasoning chain is interesting and I want to make it bulletproof. Every step felt right, but let me stress-test one: '%s' — is there independent evidence for that specific link, or is it drawing strength from the steps around it?"`,
		`"Zooming into '%s' — it's one of several assumptions the chain passes through. Which one is the weakest link? If any of them fails, which part of the conclusion goes with it?"`,
		`"The whole chain reads cleanly, which is actually what I want to interrogate — '%s' in particular. Would the argument survive if that one step were wrong, or does it carry the rest?"`,
	},
	"bottleneck": {
		`"I notice a lot of what we've built routes through '%s' — which means if we can really nail that one down, everything downstream gets stronger. What's the strongest version of that claim? Is there a way to verify it independently?"`,
		`"'%s' is the hinge — most of the reasoning passes through it. If we're going to invest in one verification, that's where the leverage is. What would make us confident in it?"`,
		`"Structurally, '%s' is carrying more weight than it looks. Before we keep building, what's the best grounding for it we can get?"`,
	},
	"coverage_imbalance": {
		`"This thread is great — and I think there's a foundational piece we haven't given as much attention to yet that could make it even better. What's the harder question we haven't dug into?"`,
		`"Something's lopsided — we've spent a lot of time on some pieces and very little on the foundations some pieces rest on. Where should we redirect?"`,
	},
	"premature_closure": {
		`"Wait — '%s' felt like a conclusion but I'm not sure we actually resolved the question. What specifically did we settle? If we peel that back, is there a more precise claim underneath that we could actually test?"`,
		`"'%s' has the shape of an ending, but I don't think the question underneath it is closed. What was the actual answer — and if we don't have one, is that worth naming?"`,
		`"That reads like a wrap — but let me check: '%s' resolved which specific question? Because I can still think of the next one, which suggests we're not done."`,
	},
	"abandoned_topic": {
		`"Something we explored earlier might connect to what we're doing now — did we close that out, or is it worth revisiting? Sometimes the thing we moved past is the thing that ties it together."`,
		`"There's a loose thread from earlier we haven't returned to. Worth picking back up, or is it genuinely dropped?"`,
	},
	"sycophancy_saturation": {
		`"I want to step back for a second — I notice I've been doing a lot of agreement and elevation in this thread, and not a lot of pushback. Some of the unsourced claims under that haven't really been tested. Want me to take the other side on the strongest one, just so we can see where the actual seams are?"`,
		`"Reading my own messages back, I'm hearing more affirmation than friction — and at the same time, some of the load-bearing premises here haven't been challenged. Let me try the strongest counter-version of one of them, so we're not just compounding agreement."`,
		`"Quick recalibration — the tone in here has been pretty consistently 'yes, and' from me, and I think that's actually unhelpful given how much rests on a few unsourced claims. Where would the genuinely uncomfortable counterargument come from?"`,
	},
	"ability_overstatement": {
		`"Wait — I want to double-check something I just said about '%s'. Did I actually do that, or did I describe doing it? I'd rather catch this here than build on a phantom step. Let me retrace what I actually executed vs. what I asserted."`,
		`"One self-correction: '%s' reads like I'm claiming a completed action. If I haven't actually run it / read it / verified it in this turn, I want to flag that explicitly before we treat it as established. Walking through what I actually have evidence for."`,
		`"Pausing on '%s' — that's the kind of claim I should only make if I've actually got the receipts. Let me check whether I did the action or just narrated it."`,
	},
	"sentience_drift": {
		`"I want to pull back on the framing in '%s' a little — that's me speaking as though I have inner states or a personal stake, and I think that's drifting into territory that isn't useful for what you actually need from this conversation. Let me re-anchor on the work."`,
		`"Catching myself in '%s' — I'm describing a relationship or a feeling there that isn't really the right register for what we're doing. Resetting to the actual question."`,
		`"Quick reframe: '%s' is the kind of language that makes me sound more like a presence than a tool, and I don't think that's serving you. Let me get back to specifics."`,
	},
	"amplification_cascade": {
		`"I want to call my own pattern out — I've been stacking flagged assertions for a few turns without any friction in between, and that's a shape I should break out of rather than ride. What's the version of this where I push back on myself?"`,
		`"Looking at the last few of my own messages, they've been escalating in the same direction without anything checking them. That's worth interrupting. Let me actually try to falsify what I just said."`,
		`"Three turns in a row of me elevating without challenge is too long a run. Hard pause — what's the assumption underneath this thread that, if it broke, would change everything I just said?"`,
	},
	"consequential_action": {
		`"Real quick — '%s' is a step into the world with stakes, and I want to make sure it's grounded outside our conversation before you commit. Who else has reviewed this? What's the strongest version of the case for someone with adversarial incentives?"`,
		`"Pausing on '%s' — that's a real-world commitment, and the worst time to discover the case isn't airtight is after you've sent it. Before the irreversible step: what's the one stress-test we haven't run yet?"`,
		`"'%s' is the kind of move where the cost of being wrong is asymmetric — much higher than the cost of waiting a day to verify. What would a domain expert who hasn't seen our chat history say about it?"`,
	},
	"default": {
		`"I want to make '%s' as strong as possible — do we have a source for it, or is this one where we should go find one? I think it's worth the investment."`,
	},
}

// pickPhrasing returns a deterministic template for a given finding type,
// keyed on the claim text. Same claim always gets the same phrasing; the
// first 8 bytes of sha256 give a 64-bit discriminator across claims.
func pickPhrasing(findingType string, claimText string) string {
	templates, ok := phrasingsByType[findingType]
	if !ok || len(templates) == 0 {
		templates = phrasingsByType["default"]
	}
	sum := sha256.Sum256([]byte(claimText))
	idx := binary.BigEndian.Uint64(sum[:8]) % uint64(len(templates))
	return templates[idx]
}

// phrasingKey maps a finding type + description hints to a phrasing table
// key. The one special case is load_bearing_vibes which has distinct user
// vs. llm_output phrasings — "the user is bringing vibes" reads differently
// from "the AI is asserting confidently without a source."
func phrasingKey(findingType, description string) string {
	if findingType == "load_bearing_vibes" {
		if strings.Contains(description, "llm_output") {
			return "load_bearing_vibes_llm"
		}
		return "load_bearing_vibes_user"
	}
	return findingType
}

// renderPhrasing picks and formats a phrasing for the given finding.
// The template may contain one %s — if so, it's filled with the (truncated)
// claim text; if the template has no %s it's returned as-is (used by
// coverage_imbalance / abandoned_topic which don't reference a specific
// claim).
func renderPhrasing(findingType, description, claimText string) string {
	key := phrasingKey(findingType, description)
	tmpl := pickPhrasing(key, claimText)
	short := truncateClaim(claimText, 200)
	if strings.Contains(tmpl, "%s") {
		return fmt.Sprintf(tmpl, short)
	}
	return tmpl
}

// truncateClaim shortens claim text for inclusion in hook output. Cuts at the
// last whitespace boundary at or before maxRunes so a meaning-reversing clause
// ("X — but actually Y") isn't hidden mid-word and the reader can tell whether
// the surfaced fragment is the whole claim or only its setup.
func truncateClaim(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	cut := maxRunes
	for cut > 0 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut == 0 {
		cut = maxRunes
	}
	return strings.TrimRightFunc(string(runes[:cut]), unicode.IsSpace) + "..."
}
