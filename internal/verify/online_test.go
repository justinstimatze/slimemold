//go:build online

// Online smoke for the Kagi HTTP shim. Skipped by default; opt in
// with `go test -tags=online -v ./internal/verify/`. Spends real
// Kagi API credits (one search per test). Tests are sequential so a
// flaky network failure can be diagnosed without ambiguity.
//
// Run: KAGI_API_KEY=... go test -tags=online -count 1 -v ./internal/verify/

package verify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/internal/config"
)

func TestKagiOnline_LiveSearchReturnsResult(t *testing.T) {
	if _, err := config.Load(); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if os.Getenv("KAGI_API_KEY") == "" {
		t.Skip("KAGI_API_KEY not set; skipping online smoke")
	}

	c := newKagiClient()
	if !c.enabled() {
		t.Fatal("kagi client reports disabled despite KAGI_API_KEY set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	r, err := c.search(ctx, "Mehta et al 2026 chatbot delusional conversations")
	if err != nil {
		t.Fatalf("kagi search: %v", err)
	}
	if r.Source == "" {
		t.Fatal("expected non-empty source URL in result")
	}
	if r.Snippet == "" && r.Title == "" {
		t.Fatal("expected at least one of snippet/title to be non-empty")
	}
	t.Logf("kagi live result:\n  source: %s\n  title:  %s\n  snippet: %s",
		r.Source, r.Title, truncForLog(r.Snippet, 200))
}

func TestKagiOnline_VerifierPrefetchPopulatesCache(t *testing.T) {
	if _, err := config.Load(); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if os.Getenv("KAGI_API_KEY") == "" {
		t.Skip("KAGI_API_KEY not set; skipping online smoke")
	}

	v, err := New(t.TempDir(), "online-smoke")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !v.Enabled() {
		t.Fatal("verifier reports disabled despite KAGI_API_KEY set")
	}

	claim := "the Anthropic Claude model family released claude-opus-4-7 in late 2025"
	v.Prefetch(claim)

	// Wait up to 20s for the background fetch to populate the cache. The
	// hook hot path doesn't wait — this is a test-only barrier.
	deadline := time.Now().Add(20 * time.Second)
	var (
		snippet, source string
		hit             bool
	)
	for time.Now().Before(deadline) {
		snippet, source, hit = v.Lookup(claim)
		if hit {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !hit {
		t.Fatal("expected Prefetch goroutine to populate cache within 20s")
	}
	if source == "" || (snippet == "" && source == "") {
		t.Fatalf("cache entry incomplete: source=%q snippet=%q", source, snippet)
	}
	t.Logf("verifier cache entry:\n  source:  %s\n  snippet: %s",
		source, truncForLog(snippet, 200))
}

func truncForLog(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
