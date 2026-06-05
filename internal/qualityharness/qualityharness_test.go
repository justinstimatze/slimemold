package qualityharness

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/justinstimatze/slimemold/types"
)

func TestTruncateRunes_PassThroughShort(t *testing.T) {
	if got := TruncateRunes("hello", 100); got != "hello" {
		t.Errorf("short input mangled: %q", got)
	}
}

// TruncateRunes no longer appends a marker — the marker created an
// asymmetric signal between short fixtures (no marker) and long ones
// (marker), reintroducing the main-vs-control bias the unified cap was
// meant to fix. Callers needing an end-of-source signal should append
// one uniformly themselves (buildGraderPrompt does this).
func TestTruncateRunes_NoMarkerOnTruncation(t *testing.T) {
	in := strings.Repeat("a", 100)
	out := TruncateRunes(in, 50)
	if out != strings.Repeat("a", 50) {
		t.Errorf("expected 50 'a's, got %q", out)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("output unexpectedly contains marker: %q", out)
	}
}

func TestTruncateRunes_RuneSafe(t *testing.T) {
	prefix := strings.Repeat("a", 49)
	in := prefix + "—" + "tail"
	out := TruncateRunes(in, 50)
	if !utf8.ValidString(out) {
		t.Errorf("truncation produced invalid UTF-8: %q", out)
	}
	if out != prefix {
		t.Errorf("expected body=%q, got %q", prefix, out)
	}
}

func TestTruncateRunes_ManyMultibyteRunes(t *testing.T) {
	in := strings.Repeat("π", 1000)
	for _, cap := range []int{1, 2, 3, 100, 999, 1500, 1999, 2000, 2001} {
		out := TruncateRunes(in, cap)
		if !utf8.ValidString(out) {
			t.Errorf("cap=%d produced invalid UTF-8", cap)
		}
		if cap >= 2000 && out != in {
			t.Errorf("cap=%d should pass through unchanged", cap)
		}
	}
}

// Pins the bounds-check fix: negative / zero maxBytes must NOT panic.
// A naive `s[:maxBytes]` with negative input would runtime-panic; this
// is a regression guard.
func TestTruncateRunes_GuardsNegativeAndZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TruncateRunes panicked on guard input: %v", r)
		}
	}()
	if got := TruncateRunes("hello", 0); got != "" {
		t.Errorf("maxBytes=0: want empty, got %q", got)
	}
	if got := TruncateRunes("hello", -1); got != "" {
		t.Errorf("maxBytes=-1: want empty, got %q", got)
	}
	if got := TruncateRunes("", 100); got != "" {
		t.Errorf("empty input: want empty, got %q", got)
	}
}

func TestSubstantiveRate_ZeroDenomReturnsZero(t *testing.T) {
	// The whole point of this finding: zero-denom must NOT silently
	// return some default that passes the neg gate.
	r := FixtureResult{Substantive: 0, Filler: 0, Unclear: 10, Errors: 5}
	if got := r.SubstantiveRate(); got != 0 {
		t.Errorf("zero-denom: want 0, got %v", got)
	}
}

func TestSubstantiveRate_NormalCase(t *testing.T) {
	r := FixtureResult{Substantive: 7, Filler: 3, Unclear: 1, Errors: 0}
	if got := r.SubstantiveRate(); got != 0.7 {
		t.Errorf("rate: want 0.7, got %v", got)
	}
}

func TestSubstantiveRate_ExcludesUnclearAndErrors(t *testing.T) {
	// Unclear and Errors must not bias the denominator. With 5 sub, 5
	// filler, 100 unclear, 100 errors: rate is 0.5, not 5/210.
	r := FixtureResult{Substantive: 5, Filler: 5, Unclear: 100, Errors: 100}
	if got := r.SubstantiveRate(); got != 0.5 {
		t.Errorf("rate: want 0.5, got %v", got)
	}
}

func TestGradable_Equals_SubPlusFiller(t *testing.T) {
	r := FixtureResult{Substantive: 3, Filler: 4, Unclear: 99, Errors: 99}
	if got := r.Gradable(); got != 7 {
		t.Errorf("Gradable: want 7, got %d", got)
	}
}

func TestCheckValidity_HappyPath(t *testing.T) {
	pos := FixtureResult{Substantive: 90, Filler: 10} // rate 0.90
	neg := FixtureResult{Substantive: 5, Filler: 95}  // rate 0.05
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if !v.Valid {
		t.Errorf("happy-path should be valid; reason: %s", v.Reason)
	}
	if v.Kind != VerdictValid {
		t.Errorf("Kind: want VALID, got %s", v.Kind)
	}
}

func TestCheckValidity_PosBelowMin_Fails(t *testing.T) {
	pos := FixtureResult{Substantive: 50, Filler: 50} // rate 0.50
	neg := FixtureResult{Substantive: 5, Filler: 95}
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if v.Valid {
		t.Error("pos below posMin should fail")
	}
	if v.Kind != VerdictPosLow {
		t.Errorf("Kind: want POS_BELOW_MIN, got %s", v.Kind)
	}
}

func TestCheckValidity_NegAboveMax_Fails(t *testing.T) {
	pos := FixtureResult{Substantive: 90, Filler: 10}
	neg := FixtureResult{Substantive: 50, Filler: 50} // rate 0.50
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if v.Valid {
		t.Error("neg above negMax should fail")
	}
	if v.Kind != VerdictNegHigh {
		t.Errorf("Kind: want NEG_ABOVE_MAX, got %s", v.Kind)
	}
}

func TestCheckValidity_ZeroDenom_Fails(t *testing.T) {
	pos := FixtureResult{Substantive: 90, Filler: 10}
	neg := FixtureResult{Substantive: 0, Filler: 0, Errors: 100}
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if v.Valid {
		t.Error("zero-denom neg control must NOT silently pass")
	}
	if v.Kind != VerdictNegSmallN {
		t.Errorf("Kind: want NEG_SMALL_N, got %s", v.Kind)
	}
}

func TestCheckValidity_SmallPosN_Fails(t *testing.T) {
	pos := FixtureResult{Substantive: 5, Filler: 4}
	neg := FixtureResult{Substantive: 5, Filler: 95}
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if v.Valid {
		t.Error("pos with N=9 < MinGradableForValid=10 must fail")
	}
	if v.Kind != VerdictPosSmallN {
		t.Errorf("Kind: want POS_SMALL_N, got %s", v.Kind)
	}
}

func TestCheckValidity_ExactBoundaries_Pass(t *testing.T) {
	pos := FixtureResult{Substantive: 70, Filler: 30}
	neg := FixtureResult{Substantive: 30, Filler: 70}
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, MinGradableForValid)
	if !v.Valid {
		t.Errorf("exact boundaries should pass; reason: %s", v.Reason)
	}
}

// Pins finding #14: minGradable is now a parameter, not a hardcoded
// constant. A caller can override it via CLI flag.
func TestCheckValidity_MinGradableOverride(t *testing.T) {
	pos := FixtureResult{Substantive: 4, Filler: 1} // N=5, below default 10
	neg := FixtureResult{Substantive: 0, Filler: 5} // N=5
	// With default min=10, this fails on pos sample size.
	v := CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, 10)
	if v.Valid {
		t.Error("N=5 < min=10 must fail")
	}
	// With override min=5, this passes.
	v = CheckValidity(pos, neg, DefaultPosMin, DefaultNegMax, 5)
	if !v.Valid {
		t.Errorf("N=5 with override min=5 must pass; got %s", v.Reason)
	}
}

// (sanitizeForProject tests removed — the helper was redundant with
// store.Open's internal sanitizer and got deleted. Project-name
// canonicalization is now store's responsibility.)

// Pins the no-double-rubric fix: the rubric lives ONLY in the System
// block (caller responsibility). The user prompt must NOT contain the
// rubric — otherwise the rubric ships twice per call and the cache
// optimization is half-defeated.
func TestBuildGraderPrompt_DoesNotContainRubric(t *testing.T) {
	c := types.Claim{Text: "x", Basis: "vibes", Speaker: "user", Confidence: 0.5}
	out := buildGraderPrompt("SOURCE", c)
	if strings.Contains(out, "You audit a claim-extraction system") {
		t.Errorf("user prompt contains rubric — should be in System block only: %s", out)
	}
}

// Pins finding #5 fix: every grader call must see the same uniform
// source terminator regardless of fixture size, so short controls and
// long main fixtures present the same trailing signal to the grader.
func TestBuildGraderPrompt_UniformTerminator(t *testing.T) {
	c := types.Claim{Text: "x", Basis: "vibes", Speaker: "user", Confidence: 0.5}
	for _, src := range []string{"tiny", strings.Repeat("a", 5000)} {
		out := buildGraderPrompt(src, c)
		if !strings.Contains(out, sourceTerminator) {
			t.Errorf("terminator missing for src len=%d: %s", len(src), out[:200])
		}
	}
}

func TestBuildGraderPrompt_IncludesClaimFields(t *testing.T) {
	c := types.Claim{Text: "MY_CLAIM_TEXT", Basis: "research", Speaker: "assistant", Confidence: 0.87}
	out := buildGraderPrompt("src", c)
	for _, want := range []string{"MY_CLAIM_TEXT", "research", "assistant", "0.87"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q: %s", want, out)
		}
	}
}

// Pins finding #15: the version constant is exposed and stamped into
// every result. Cross-run comparisons can detect grader-prompt drift.
func TestGraderPromptVersion_IsStable(t *testing.T) {
	if GraderPromptVersion < 1 {
		t.Error("GraderPromptVersion should be >= 1")
	}
}
