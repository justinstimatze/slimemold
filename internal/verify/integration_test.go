package verify

// Integration smoke: a real *verify.Verifier (not the analysis test's
// stubVerifier) flowing into analysis.FormatHookFindings. Exercises the
// concrete chain — verify.New -> cache.put -> Verifier.Lookup ->
// HookVerifier interface dispatch -> FormatHookFindings injection —
// without spending Kagi API credits. The remaining untested layer is
// the Kagi HTTP shim itself, which is small, stateless, and tested
// for real once KAGI_API_KEY is wired in.

import (
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/types"
)

func TestIntegration_RealVerifierInlinesReconciledState(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "") // pure offline; no async fetch attempt

	v, err := New(t.TempDir(), "smoke-project")
	if err != nil {
		t.Fatalf("verify.New: %v", err)
	}

	claimText := "the project's positioning claim that lives in the README"
	stored := Reconciled{
		Query:     claimText,
		Source:    "https://example.com/state-of-positioning",
		Title:     "External Reality",
		Snippet:   "the README's framing has been superseded by current consensus X",
		FetchedAt: time.Now(),
	}
	if err := v.cache.put(claimKey(claimText), stored); err != nil {
		t.Fatalf("cache.put: %v", err)
	}

	now := time.Now()
	claims := []types.Claim{
		{ID: "doc-anchor", Text: claimText, Basis: types.BasisVibes, SessionID: "doc:abc123", CreatedAt: now},
		{ID: "f1", Text: "f1", Basis: types.BasisDeduction, CreatedAt: now},
		{ID: "f2", Text: "f2", Basis: types.BasisDeduction, CreatedAt: now},
		{ID: "f3", Text: "f3", Basis: types.BasisDeduction, CreatedAt: now},
		{ID: "f4", Text: "f4", Basis: types.BasisDeduction, CreatedAt: now},
		{ID: "f5", Text: "f5", Basis: types.BasisDeduction, CreatedAt: now},
		{ID: "f6", Text: "f6", Basis: types.BasisDeduction, CreatedAt: now},
	}
	topo := &types.Topology{Project: "smoke", ClaimCount: len(claims)}
	vulns := &types.Vulnerabilities{
		Project: "smoke",
		Items: []types.Vulnerability{
			{Severity: "critical", Type: "load_bearing_vibes", Description: `Load-bearing vibes: "the project's positioning claim that lives in the README" supports 3 other claims (never challenged: true) [doc-origin]`, ClaimIDs: []string{"doc-anchor"}},
		},
	}

	summary, picked, _, _ := analysis.FormatHookFindings(topo, vulns, claims, nil, 0, 0, 5, v)
	if picked != "doc-anchor" {
		t.Fatalf("expected doc-anchor to win priority slot, got %q\nsummary:\n%s", picked, summary)
	}
	if !strings.Contains(summary, "External check") {
		t.Fatalf("expected External check line in hook output, got:\n%s", summary)
	}
	if !strings.Contains(summary, "example.com/state-of-positioning") {
		t.Fatalf("expected source domain in hook output, got:\n%s", summary)
	}
	if !strings.Contains(summary, "superseded by current consensus X") {
		t.Fatalf("expected snippet content in hook output, got:\n%s", summary)
	}

	t.Logf("integration summary:\n%s", summary)
}

func TestIntegration_RealVerifierPersistsCacheAcrossNew(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	dir := t.TempDir()

	v1, err := New(dir, "persist-smoke")
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	claim := "a doc-origin claim worth verifying"
	if err := v1.cache.put(claimKey(claim), Reconciled{
		Query:     claim,
		Source:    "https://example.com/source-a",
		Snippet:   "an external source disagrees",
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	v2, err := New(dir, "persist-smoke")
	if err != nil {
		t.Fatalf("second New (cold-start reload): %v", err)
	}
	snippet, source, ok := v2.Lookup(claim)
	if !ok {
		t.Fatal("expected cache entry to survive across Verifier restart")
	}
	if !strings.Contains(source, "example.com/source-a") || !strings.Contains(snippet, "external source disagrees") {
		t.Fatalf("expected pre-populated entry, got (%s, %s)", snippet, source)
	}
}
