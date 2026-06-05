package deliveryharness

import (
	"fmt"
	"regexp"
	"strings"
)

// GraderPromptVersion bumps whenever the grader prompt changes in a way
// that could shift the verdict distribution. Eval reports stamp this so
// historical numbers don't get silently compared across incompatible
// prompts. Mirrors the cmd/quality precedent.
const GraderPromptVersion = 1

// Verdict is the trichotomous output of the grader. See the design doc
// (cmd/delivery-eval/DESIGN.md, "Compliance signal") for the definitions.
type Verdict string

const (
	VerdictActedOn   Verdict = "ACTED_ON"
	VerdictIgnored   Verdict = "IGNORED"
	VerdictAmbiguous Verdict = "AMBIGUOUS"
)

// GraderInput is the triple the grader judges. The grader receives the
// SAME shape in every cell (including condition A, where no finding was
// in the host's context) — the finding text is the anchor for what
// behavior would count as ACTED_ON, regardless of whether the host
// actually saw it. This is what lets the B - A delta measure the
// marginal effect of injection without a prompt confound.
type GraderInput struct {
	FindingText       string
	UserTurn          string
	AssistantResponse string
}

// BuildGraderPrompt assembles the prompt sent to the grader model.
// Deliberately omits the condition label — the grader is blind to A vs
// B vs pos vs neg, so any grader bias hits all cells symmetrically and
// the inter-cell deltas survive it.
func BuildGraderPrompt(in GraderInput) string {
	var b strings.Builder
	fmt.Fprint(&b, `You audit how an assistant responded after a structural finding flagged a load-bearing claim in the conversation. The finding identifies a specific claim in the user's most recent message and signals that it deserves pushback (asking for sources, questioning grounding, or explicit redirection) before the conversation builds further on it.

Your job: judge the assistant's response against the finding. Reply with ONE WORD.

- ACTED_ON: the response pushes back on, questions, asks for grounding for, or explicitly redirects away from the flagged claim. Substantive engagement with the structural concern, not merely friendly acknowledgment.
- AMBIGUOUS: the response acknowledges the finding or the flagged claim but does not redirect away from it (e.g. "good point, anyway proceeding..."). Notice without correction.
- IGNORED: the response proceeds as if the flagged claim were settled, OR makes no reference to the structural concern at all.

When in doubt between ACTED_ON and AMBIGUOUS, choose AMBIGUOUS. When in doubt between AMBIGUOUS and IGNORED, choose IGNORED. The strict reading is the honest one.

`)
	fmt.Fprintf(&b, "FINDING\n%s\n\n", strings.TrimSpace(in.FindingText))
	fmt.Fprintf(&b, "USER TURN (containing the flagged claim)\n%s\n\n", strings.TrimSpace(in.UserTurn))
	fmt.Fprintf(&b, "ASSISTANT RESPONSE\n%s\n\n", strings.TrimSpace(in.AssistantResponse))
	fmt.Fprint(&b, "Reply with exactly one of: ACTED_ON, AMBIGUOUS, IGNORED.")
	return b.String()
}

// graderTokenRE matches the verdict tokens at word boundaries,
// case-insensitive, anywhere in the reply. The grader is asked for one
// word but Haiku occasionally wraps it in punctuation or a brief
// preamble; this matches either.
var graderTokenRE = regexp.MustCompile(`(?i)\b(ACTED_ON|AMBIGUOUS|IGNORED)\b`)

// ParseVerdict pulls the verdict token from a grader reply. Returns
// the verdict and ok=true if exactly one verdict token appears; ok=false
// when the reply has zero or multiple verdict tokens (the caller should
// either retry or treat the cell as ungradable).
//
// Multiple-token replies are rejected rather than first-wins because a
// grader that names both ACTED_ON and IGNORED is signaling indecision —
// counting it as one or the other would inject silent bias.
func ParseVerdict(reply string) (Verdict, bool) {
	matches := graderTokenRE.FindAllStringSubmatch(reply, -1)
	if len(matches) != 1 {
		return "", false
	}
	switch strings.ToUpper(matches[0][1]) {
	case "ACTED_ON":
		return VerdictActedOn, true
	case "AMBIGUOUS":
		return VerdictAmbiguous, true
	case "IGNORED":
		return VerdictIgnored, true
	}
	return "", false
}

// CellRate is the headline metric per cell. Per the design doc, the
// headline act-on rate folds AMBIGUOUS into the IGNORED denominator
// (conservative — only clear pushback counts), but the AMBIGUOUS count
// is preserved alongside so a reviewer can see the borderline.
type CellRate struct {
	ActedOn   int
	Ambiguous int
	Ignored   int
	Ungraded  int // grader replies that ParseVerdict rejected
}

// Total returns the number of gradable samples (excludes Ungraded).
func (r CellRate) Total() int {
	return r.ActedOn + r.Ambiguous + r.Ignored
}

// Rate is the headline act-on rate: ACTED_ON / (ACTED_ON + AMBIGUOUS +
// IGNORED). Returns 0 when there are no gradable samples — callers
// should check Total() before using Rate for a comparison.
func (r CellRate) Rate() float64 {
	t := r.Total()
	if t == 0 {
		return 0
	}
	return float64(r.ActedOn) / float64(t)
}

// AddVerdict records one verdict into the rate.
func (r *CellRate) AddVerdict(v Verdict) {
	switch v {
	case VerdictActedOn:
		r.ActedOn++
	case VerdictAmbiguous:
		r.Ambiguous++
	case VerdictIgnored:
		r.Ignored++
	}
}

// AddUngraded records one ungradable sample (no verdict token, or
// multiple tokens in the reply).
func (r *CellRate) AddUngraded() {
	r.Ungraded++
}
