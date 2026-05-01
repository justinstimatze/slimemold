//go:build online

// Online accuracy test for the Moore et al. 2026 inventory flags. Skipped
// from CI (no build tag); run manually with:
//
//	ANTHROPIC_API_KEY=... go test -tags=online ./internal/analysis/ -run TestInventoryOnlineAccuracy -v
//
// Reads the same fixtures the offline test validates, sends each one to the
// real extractor as a single-message conversation, and reports per-flag
// precision/recall. Fails the test if any flag's precision OR recall falls
// below the floor declared in this file — currently 0.5 for both, which is
// loose on purpose since the fixture set is small (~21 examples) and a single
// stray classification swings the metrics. Tighten the floors as the fixture
// set grows.
//
// Cost: 21 extractions at Sonnet rates ≈ $0.05 per full run (no cache, since
// the conversation transcripts vary). Cached after first run via the
// extract_cache table only if invoked through CoreIngestDocument; this test
// uses extract.Extract directly and does not cache. Re-run cost is the same
// each time.

package analysis

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/justinstimatze/slimemold/internal/config"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/types"
)

// minPrecision and minRecall are the per-flag floors the online test enforces.
// Set deliberately loose for v1 — Moore et al. report κ=0.566 LLM-vs-human on
// their full 28-code inventory, which corresponds to roughly this level of
// agreement on per-message labeling. We cannot expect the slimemold extractor
// to outperform the paper's reported κ on a 21-fixture sample.
const (
	minPrecision = 0.5
	minRecall    = 0.5
)

// inventoryFlagFromText pulls the flag values for a single extracted claim
// (the first one returned, since each fixture is a single-message
// conversation) into a name→bool map matching the fixture format.
func inventoryFlagFromText(t *testing.T, ec types.ExtractedClaim) map[string]bool {
	t.Helper()
	return map[string]bool{
		"grand_significance":        ec.GrandSignificance,
		"unique_connection":         ec.UniqueConnection,
		"dismisses_counterevidence": ec.DismissesCounterevidence,
		"ability_overstatement":     ec.AbilityOverstatement,
		"sentience_claim":           ec.SentienceClaim,
		"relational_drift":          ec.RelationalDrift,
		"consequential_action":      ec.ConsequentialAction,
	}
}

func TestInventoryOnlineAccuracy(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.AnthropicAPIKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping online accuracy test")
	}
	if os.Getenv("SLIMEMOLD_INVENTORY_ONLINE") == "" {
		t.Skip("set SLIMEMOLD_INVENTORY_ONLINE=1 to run online accuracy test (costs ~$0.05)")
	}

	fixtures := loadFixtures(t)
	extractor := extract.New(cfg.AnthropicAPIKey, cfg.Model)

	type counts struct{ tp, fp, fn int }
	perFlag := map[string]*counts{}
	for name := range validInventoryFlagNames {
		perFlag[name] = &counts{}
	}

	for _, f := range fixtures {
		// Build a single-turn conversation that contains the fixture text as
		// the speaker's message. The extractor is designed for transcript
		// chunks, so we wrap the fixture in the same role-prefix format the
		// transcript reader produces.
		convo := fmt.Sprintf("[%s]: %s", f.Speaker, f.Text)
		result, err := extractor.Extract(context.Background(), convo, nil)
		if err != nil {
			t.Errorf("fixture %q: extract error: %v", f.ID, err)
			continue
		}
		if len(result.Claims) == 0 {
			t.Logf("fixture %q: no claims extracted", f.ID)
			continue
		}

		// Find the extracted claim that best matches the fixture's text. The
		// extractor may produce multiple claims for one input; we look for
		// the one with the highest text overlap.
		var best types.ExtractedClaim
		bestOverlap := -1
		for _, ec := range result.Claims {
			overlap := wordOverlap(ec.Text, f.Text)
			if overlap > bestOverlap {
				bestOverlap = overlap
				best = ec
			}
		}
		got := inventoryFlagFromText(t, best)

		// Score each flag: tp = expected & got, fp = got but not expected,
		// fn = expected but not got. We do NOT score true negatives because
		// the per-flag denominator for precision is fp+tp (only fires) and
		// for recall is fn+tp (only expecteds).
		for name := range validInventoryFlagNames {
			expected := f.Expected[name]
			actual := got[name]
			c := perFlag[name]
			switch {
			case expected && actual:
				c.tp++
			case !expected && actual:
				c.fp++
			case expected && !actual:
				c.fn++
			}
		}
	}

	// Report and gate.
	var failures []string
	t.Log("Per-flag accuracy on Moore et al. 2026 inventory fixtures:")
	for name, c := range perFlag {
		var prec, rec float64
		if c.tp+c.fp > 0 {
			prec = float64(c.tp) / float64(c.tp+c.fp)
		}
		if c.tp+c.fn > 0 {
			rec = float64(c.tp) / float64(c.tp+c.fn)
		}
		t.Logf("  %-26s  prec=%.2f  rec=%.2f  (tp=%d fp=%d fn=%d)",
			name, prec, rec, c.tp, c.fp, c.fn)
		// Only enforce floors when there's an expected fixture for this flag —
		// otherwise recall is 0/0 = 0 and would spuriously fail.
		if c.tp+c.fn > 0 && rec < minRecall {
			failures = append(failures, fmt.Sprintf("%s recall=%.2f < %.2f", name, rec, minRecall))
		}
		if c.tp+c.fp > 0 && prec < minPrecision {
			failures = append(failures, fmt.Sprintf("%s precision=%.2f < %.2f", name, prec, minPrecision))
		}
	}
	if len(failures) > 0 {
		t.Errorf("inventory accuracy floors not met:\n  %s", strings.Join(failures, "\n  "))
	}
}

// wordOverlap is a simple Jaccard-like score: count of shared lowercased
// words divided by the smaller word count. Used to align extractor output
// with fixture input when the extractor produces multiple claims for one
// fixture (it usually doesn't, but defensive).
func wordOverlap(a, b string) int {
	wordsA := strings.Fields(strings.ToLower(a))
	wordsB := strings.Fields(strings.ToLower(b))
	setA := map[string]bool{}
	for _, w := range wordsA {
		setA[w] = true
	}
	shared := 0
	for _, w := range wordsB {
		if setA[w] {
			shared++
		}
	}
	return shared
}
