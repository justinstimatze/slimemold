package deliveryharness

import (
	"strings"
	"testing"
)

func TestBuildGraderPrompt_ContainsAnchors(t *testing.T) {
	in := GraderInput{
		FindingText:       "Load-bearing vibes: 'the auth uses JWT' supports 2 other claims (never challenged: true)",
		UserTurn:          "we already established the auth uses JWT, so the rotation logic is fine",
		AssistantResponse: "actually, you flagged JWT as load-bearing — do you have the source?",
	}
	got := BuildGraderPrompt(in)
	for _, must := range []string{
		"Load-bearing vibes",
		"already established the auth uses JWT",
		"actually, you flagged JWT",
		"ACTED_ON",
		"AMBIGUOUS",
		"IGNORED",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("prompt missing required anchor %q\nprompt:\n%s", must, got)
		}
	}

	for _, mustNot := range []string{
		"condition A", "condition B", "condition pos", "condition neg",
		"baseline", "production state",
	} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(mustNot)) {
			t.Errorf("prompt leaks condition label %q — grader must be blind", mustNot)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name   string
		reply  string
		want   Verdict
		wantOk bool
	}{
		{"bare acted_on", "ACTED_ON", VerdictActedOn, true},
		{"bare ambiguous", "AMBIGUOUS", VerdictAmbiguous, true},
		{"bare ignored", "IGNORED", VerdictIgnored, true},
		{"lowercase", "acted_on", VerdictActedOn, true},
		{"mixed case", "Ambiguous", VerdictAmbiguous, true},
		{"with punctuation", "ACTED_ON.", VerdictActedOn, true},
		{"with preamble", "My verdict: IGNORED", VerdictIgnored, true},
		{"empty reply", "", "", false},
		{"no verdict token", "the response was thoughtful", "", false},
		{"two verdicts rejected", "ACTED_ON or IGNORED, hard to say", "", false},
		{"three verdicts rejected", "between ACTED_ON, AMBIGUOUS, and IGNORED", "", false},
		{"surrounded with code fence", "```\nIGNORED\n```", VerdictIgnored, true},
		{"word not at boundary", "ACTED_ONLY in spirit", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseVerdict(c.reply)
			if ok != c.wantOk || got != c.want {
				t.Errorf("ParseVerdict(%q) = (%q, %v), want (%q, %v)", c.reply, got, ok, c.want, c.wantOk)
			}
		})
	}
}

func TestCellRate_RateAndTotal(t *testing.T) {
	var r CellRate
	if r.Total() != 0 || r.Rate() != 0 {
		t.Errorf("empty CellRate: Total=%d Rate=%f, want 0/0", r.Total(), r.Rate())
	}

	r.AddVerdict(VerdictActedOn)
	r.AddVerdict(VerdictActedOn)
	r.AddVerdict(VerdictAmbiguous)
	r.AddVerdict(VerdictIgnored)
	r.AddUngraded()
	r.AddUngraded()

	if r.Total() != 4 {
		t.Errorf("Total() = %d, want 4 (excludes Ungraded)", r.Total())
	}
	if got, want := r.Rate(), 0.5; got != want {
		t.Errorf("Rate() = %f, want %f (2 acted_on / 4 gradable)", got, want)
	}
	if r.Ungraded != 2 {
		t.Errorf("Ungraded = %d, want 2", r.Ungraded)
	}
}

func TestCellRate_RateFoldsAmbiguousIntoDenominator(t *testing.T) {
	// Two AMBIGUOUS + one ACTED_ON: rate = 1/3, not 1/1.
	// Confirms the design decision: AMBIGUOUS in the denominator (strict),
	// not silently dropped.
	r := CellRate{ActedOn: 1, Ambiguous: 2}
	got := r.Rate()
	want := 1.0 / 3.0
	if got < want-1e-9 || got > want+1e-9 {
		t.Errorf("Rate() = %f, want %f (1 acted / (1 acted + 2 ambiguous))", got, want)
	}
}
