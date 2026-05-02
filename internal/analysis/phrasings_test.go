package analysis

import (
	"strings"
	"testing"
)

func TestTruncateClaim_ShortPassesThrough(t *testing.T) {
	in := "A short claim."
	if got := truncateClaim(in, 200); got != in {
		t.Errorf("short text should pass through unchanged; got %q", got)
	}
}

func TestTruncateClaim_CutsAtWordBoundary(t *testing.T) {
	in := strings.Repeat("word ", 80) // 400 chars, every 5th char a space
	got := truncateClaim(in, 200)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	body := strings.TrimSuffix(got, "...")
	if !strings.HasSuffix(body, "word") {
		t.Errorf("expected cut to land at end of a complete token, not mid-word; got %q", body)
	}
}

// The original 80-char truncation cut "...elbow joint..." and hid the
// "is stale" reversal that follows. With the wider word-boundary cut, the
// reversal is preserved so the reader can tell a meta-claim from the original
// claim it negates. Fixture matches the production claim verbatim, including
// the em-dash, the path expression that pushes total length past maxRunes,
// and exercises the truncation-with-preservation path (not the early return).
func TestTruncateClaim_PreservesMeaningReversingClause(t *testing.T) {
	in := "The 'Lucida live DOM lacks the quarter-circle elbow joint' slimemold flag is stale " +
		"— the elbow has been live since commit 4b57802 (verified at notebook.css:919 " +
		"as clip-path: path('M 0 0 L 24 0 A 24 24 0 0 0 0 24 Z'))."
	if len([]rune(in)) <= 200 {
		t.Fatalf("fixture too short to trigger truncation; need >200 runes, got %d", len([]rune(in)))
	}
	got := truncateClaim(in, 200)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix indicating truncation, got %q", got)
	}
	if !strings.Contains(got, "is stale") {
		t.Errorf("truncation hid the meaning-reversing clause: %q", got)
	}
}

// Sentinel test on the public function. If a refactor bypasses truncateClaim
// (e.g. reverts to byte-slicing or removes the call), the sentinel from the
// far end of the input leaks into the output and this test catches it.
func TestRenderPhrasing_LongClaimGetsTruncated(t *testing.T) {
	long := strings.Repeat("filler ", 50) + "ZZZ_END_SENTINEL_ZZZ"
	out := renderPhrasing("load_bearing_vibes", "basis=vibes", long)
	if strings.Contains(out, "ZZZ_END_SENTINEL_ZZZ") {
		t.Errorf("renderPhrasing did not truncate long claim; end-of-input leaked into output: %q", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("expected truncated claim text to include ellipsis marker: %q", out)
	}
}

func TestTruncateClaim_NoWhitespaceFallsBackToHardCut(t *testing.T) {
	in := strings.Repeat("a", 300)
	got := truncateClaim(in, 50)
	if got != strings.Repeat("a", 50)+"..." {
		t.Errorf("expected hard cut at maxRunes when no whitespace present; got %q", got)
	}
}

func TestTruncateClaim_MultiByteRunesCountedCorrectly(t *testing.T) {
	in := strings.Repeat("café ", 50) // 250 runes, 300 bytes
	got := truncateClaim(in, 200)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncation by rune count, not byte count; got %q", got)
	}
}
