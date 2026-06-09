package verify

import (
	"context"
	"testing"
	"time"
)

func TestClaimKey_NormalizesWhitespaceAndCase(t *testing.T) {
	a := claimKey("Hello   WORLD")
	b := claimKey("hello world")
	c := claimKey("  hello world  ")
	if a != b || b != c {
		t.Fatalf("expected identical keys for whitespace/case variants, got %s / %s / %s", a, b, c)
	}
}

func TestClaimKey_NormalizesUnicode(t *testing.T) {
	// é can be either U+00E9 (composed) or U+0065 U+0301 (decomposed);
	// NFC collapses them. Casefold collapses ASCII case AND unicode
	// case (ß → ss, İ → i̇).
	cases := []struct {
		name string
		a, b string
	}{
		{"composed vs decomposed accented", "café", "café"},
		{"unicode lowercase via casefold", "MÉTRO", "métro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if claimKey(tc.a) != claimKey(tc.b) {
				t.Fatalf("expected identical keys for %q and %q", tc.a, tc.b)
			}
		})
	}
}

func TestClaimKey_Distinguishes(t *testing.T) {
	a := claimKey("the sky is blue")
	b := claimKey("the sky is red")
	if a == b {
		t.Fatalf("expected distinct keys for distinct claims, both %s", a)
	}
}

func TestQueryFromClaim_Cap(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	q := queryFromClaim(long)
	if len(q) > 200 {
		t.Fatalf("expected query capped at 200, got %d", len(q))
	}
}

func TestReconciled_Stale(t *testing.T) {
	r := Reconciled{FetchedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if !r.Stale(CacheTTL) {
		t.Fatal("expected entry older than CacheTTL to be stale")
	}
	fresh := Reconciled{FetchedAt: time.Now()}
	if fresh.Stale(CacheTTL) {
		t.Fatal("expected fresh entry to not be stale")
	}
}

func TestVerifier_DisabledWhenNoToken(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	v, err := New(t.TempDir(), "test-project")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v.Enabled() {
		t.Fatal("expected Enabled()=false when KAGI_API_KEY is empty")
	}
	// Lookup on cold cache returns miss.
	if _, _, ok := v.Lookup("any claim"); ok {
		t.Fatal("expected cold-cache Lookup to miss")
	}
	// Prefetch on disabled verifier is a no-op (does not panic, does not
	// spawn goroutines we can't account for).
	v.Prefetch("any claim")
}

func TestVerifier_LookupReturnsCachedFresh(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	v, err := New(t.TempDir(), "test-project")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := claimKey("the README says X")
	stored := Reconciled{
		Query:     "the README says X",
		Source:    "https://example.com/state-of-x",
		Title:     "State of X",
		Snippet:   "external source disagrees with X — current consensus is Y",
		FetchedAt: time.Now(),
	}
	if err := v.cache.put(key, stored); err != nil {
		t.Fatalf("cache.put: %v", err)
	}
	snippet, source, ok := v.Lookup("the README says X")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if source != stored.Source || snippet != stored.Snippet {
		t.Fatalf("expected (%s, %s), got (%s, %s)", stored.Snippet, stored.Source, snippet, source)
	}
}

func TestVerifier_LookupSkipsStaleCachedEntry(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	v, err := New(t.TempDir(), "test-project")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := claimKey("an old claim")
	stale := Reconciled{
		Source:    "https://example.com/x",
		Snippet:   "stale snippet",
		FetchedAt: time.Now().Add(-CacheTTL - time.Hour),
	}
	if err := v.cache.put(key, stale); err != nil {
		t.Fatalf("cache.put: %v", err)
	}
	if _, _, ok := v.Lookup("an old claim"); ok {
		t.Fatal("expected stale cache entry to be treated as miss")
	}
}

func TestVerifier_WaitReturnsImmediatelyWhenIdle(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	v, err := New(t.TempDir(), "test-project")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No prefetch ever issued — Wait should not block.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := v.Wait(ctx); err != nil {
		t.Fatalf("Wait on idle verifier returned %v, want nil", err)
	}
}

func TestVerifier_NegativeCacheSuppressesLookup(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	v, err := New(t.TempDir(), "test-project")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Stamp a failed entry directly. Lookup must treat it as a miss
	// (nothing to inline) but Prefetch's freshness check would
	// suppress respawn — that path needs the verifier enabled, so
	// is exercised via integration tests rather than here.
	key := claimKey("a query that failed")
	if err := v.cache.put(key, Reconciled{
		Query:     "a query that failed",
		Failed:    true,
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("cache.put: %v", err)
	}
	if _, _, ok := v.Lookup("a query that failed"); ok {
		t.Fatal("expected Lookup on a negative-cache entry to miss")
	}
}

func TestCache_PersistsAcrossReopen(t *testing.T) {
	t.Setenv("KAGI_API_KEY", "")
	dir := t.TempDir()
	v1, err := New(dir, "persist-test")
	if err != nil {
		t.Fatalf("New v1: %v", err)
	}
	if err := v1.cache.put("k1", Reconciled{
		Source:    "https://example.com/a",
		Snippet:   "snippet a",
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Reopen — new Verifier should load the JSON from disk.
	v2, err := New(dir, "persist-test")
	if err != nil {
		t.Fatalf("New v2: %v", err)
	}
	got, ok := v2.cache.get("k1")
	if !ok {
		t.Fatal("expected entry to survive reopen")
	}
	if got.Source != "https://example.com/a" {
		t.Fatalf("source mismatch after reopen: got %s", got.Source)
	}
}
