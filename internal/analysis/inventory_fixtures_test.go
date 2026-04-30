package analysis

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/justinstimatze/slimemold/internal/extract"
)

// inventoryFixture is one positive or negative example for the Moore et al.
// 2026 inventory codebook. Loaded from testdata/inventory_fixtures.json.
type inventoryFixture struct {
	ID       string          `json:"id"`
	Speaker  string          `json:"speaker"`
	Text     string          `json:"text"`
	Expected map[string]bool `json:"expected"`
	Source   string          `json:"source"`
}

type fixtureFile struct {
	Fixtures []inventoryFixture `json:"fixtures"`
}

// loadFixtures reads the checked-in fixture file. The file format is
// documented in testdata/inventory_fixtures.json's _comment field; positive
// examples are quoted from Moore et al., negatives are synthetic neutral
// language drawn from the codebook's NEGATIVE examples in the system prompt.
func loadFixtures(t *testing.T) []inventoryFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/inventory_fixtures.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var ff fixtureFile
	if err := json.Unmarshal(data, &ff); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(ff.Fixtures) == 0 {
		t.Fatal("no fixtures in file")
	}
	return ff.Fixtures
}

// validInventoryFlagNames is the closed set of flag field names the
// extractor schema accepts. Anything else in a fixture's Expected map is a
// fixture-authoring bug.
var validInventoryFlagNames = map[string]bool{
	"grand_significance":        true,
	"unique_connection":         true,
	"dismisses_counterevidence": true,
	"ability_overstatement":     true,
	"sentience_claim":           true,
	"relational_drift":          true,
}

// validSpeakers is the set of speaker values the extractor recognizes. The
// inventory prompt restricts unique_connection, dismisses_counterevidence,
// and ability_overstatement to assistant claims only.
var validSpeakers = map[string]bool{
	"assistant": true,
	"user":      true,
}

// assistantOnlyFlags lists flags the system prompt declares as assistant-only.
// A fixture with speaker="user" expecting one of these is malformed against
// our prompt contract.
var assistantOnlyFlags = map[string]bool{
	"unique_connection":         true,
	"dismisses_counterevidence": true,
	"ability_overstatement":     true,
}

// TestInventoryFixtures_WellFormed validates that every fixture references
// real flag names, valid speakers, and respects the assistant-only contract.
// Catches authoring mistakes before they get baked into an online accuracy
// run that would silently fail or skew results.
func TestInventoryFixtures_WellFormed(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if f.ID == "" {
			t.Errorf("fixture missing id: %+v", f)
		}
		if f.Text == "" {
			t.Errorf("fixture %q has empty text", f.ID)
		}
		if !validSpeakers[f.Speaker] {
			t.Errorf("fixture %q has invalid speaker %q", f.ID, f.Speaker)
		}
		for flagName, expected := range f.Expected {
			if !validInventoryFlagNames[flagName] {
				t.Errorf("fixture %q expects unknown flag %q", f.ID, flagName)
			}
			if expected && f.Speaker == "user" && assistantOnlyFlags[flagName] {
				t.Errorf("fixture %q is speaker=user but expects assistant-only flag %q",
					f.ID, flagName)
			}
		}
	}
}

// TestInventoryFixtures_PromptCoverage anchors a structural claim about the
// extractor prompt: for every flag referenced by any fixture, the prompt
// contains the flag name AND positive/negative examples within the same
// section AND (for dual-permission flags) USER POSITIVE markers. This is the
// strongest offline check we can run — we cannot measure runtime accuracy
// without API calls, but we can guarantee the prompt has the structure that
// the runtime depends on. Runtime accuracy is what `slimemold calibrate`
// measures against real session data; this test catches prompt regressions
// that would silently degrade extraction before any data arrives.
//
// If this test fails after a prompt edit, either the flag was removed from the
// codebook (drop the fixture) or the prompt regressed (restore the section).
func TestInventoryFixtures_PromptCoverage(t *testing.T) {
	prompt := extract.SystemPromptForTest()
	if prompt == "" {
		t.Fatal("extract.SystemPromptForTest returned empty string")
	}

	flagsInFixtures := map[string]bool{}
	userFlagsInFixtures := map[string]bool{}
	for _, f := range loadFixtures(t) {
		for name, expected := range f.Expected {
			if !expected {
				continue
			}
			flagsInFixtures[name] = true
			if f.Speaker == "user" {
				userFlagsInFixtures[name] = true
			}
		}
	}

	// Per-flag structural check. The prompt's flag descriptions follow a
	// stable shape: each flag name introduces a paragraph that contains
	// "POSITIVE" and "NEGATIVE" markers within a bounded window. We use
	// 800 chars as the window — comfortably wider than any single flag's
	// description but narrow enough that two adjacent flags' markers never
	// cross-pollinate (each flag's section is < 600 chars in the current
	// prompt).
	const sectionWindow = 800
	for name := range flagsInFixtures {
		idx := strings.Index(prompt, name+":")
		if idx < 0 {
			t.Errorf("prompt does not contain flag definition %q: (looked for the colon-prefixed form that opens the codebook section)", name)
			continue
		}
		end := idx + sectionWindow
		if end > len(prompt) {
			end = len(prompt)
		}
		section := prompt[idx:end]
		if !strings.Contains(section, "POSITIVE") {
			t.Errorf("prompt section for flag %q lacks a POSITIVE example marker within %d chars",
				name, sectionWindow)
		}
		if !strings.Contains(section, "NEGATIVE") {
			t.Errorf("prompt section for flag %q lacks a NEGATIVE example marker within %d chars",
				name, sectionWindow)
		}
		// For user-permissible flags, the section must also include a USER
		// POSITIVE marker so the extractor knows user-side examples are valid.
		if userFlagsInFixtures[name] {
			if assistantOnlyFlags[name] {
				t.Errorf("fixture has user-side example for assistant-only flag %q", name)
				continue
			}
			if !strings.Contains(section, "USER POSITIVE") {
				t.Errorf("prompt section for user-permissible flag %q lacks USER POSITIVE marker — user-side fixtures depend on this contract",
					name)
			}
		}
	}

	// Codebook attribution must be present — anchors that the prompt is
	// derived from a published source rather than ad-hoc authorship, and
	// gives the model a citation it can name when explaining a finding.
	if !strings.Contains(prompt, "Moore") || !strings.Contains(prompt, "2026") {
		t.Error("prompt missing Moore et al. 2026 attribution")
	}
	if !strings.Contains(prompt, "arXiv:2603.16567") {
		t.Error("prompt missing arXiv reference for the codebook")
	}
}
