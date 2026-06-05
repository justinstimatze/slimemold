// Package qualityharness owns the substantive-vs-filler grading
// subsystem used by cmd/quality. Lives in internal/ (not as a build-
// ignored script) so:
//
//   - `go build ./...`, `go vet ./...`, and the pre-commit gate catch
//     signature drift against internal/extract, internal/mcp,
//     internal/store. The CLI shim at cmd/quality/main.go is a real
//     package (not //go:build ignore) so shim-vs-library drift is ALSO
//     caught.
//
//   - The pure logic (rate computation, validity gate, rune-safe
//     truncation, schema construction, prompt assembly) is unit-
//     testable without an Anthropic API key — see qualityharness_test.go.
//
//   - Other components (MCP server, future events-tail subcommand, a
//     winze adapter, a CI gate) can import the same types.
//
// Build the CLI:
//
//	go install ./cmd/quality
//	ANTHROPIC_API_KEY=... quality -fixture README.md
package qualityharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

// GraderPromptVersion stamps the grader-prompt revision into every
// FixtureResult so cross-run comparisons can detect that the grader
// itself changed between two runs. Bump whenever the rubric/examples
// in buildGraderPrompt change. Mirrors the documentPromptVersion
// discipline in internal/mcp/ingest.go.
const GraderPromptVersion = 1

// SourceCapBytes is the byte cap on the source-document portion of the
// grader prompt. Sized to keep grader-prompt input tokens bounded
// regardless of fixture size. Applied identically to controls AND main
// fixture so the gate validates the same grader configuration the main
// run uses (previously: controls were short enough to fit, main got
// truncated — gate validated a configuration that wasn't judging main).
const SourceCapBytes = 8000

// MinGradableForValid is the minimum number of (Substantive+Filler)
// claims a control fixture must produce before the validity gate is
// allowed to pass. Catches the degenerate case where rate-limit storms
// or extractor failures collapse the denominator to zero, making
// SubstantiveRate=0 and the negative gate spuriously PASS.
const MinGradableForValid = 10

// Validity-gate defaults — matches buddy's calibration. Overridable
// via the posMin/negMax args to CheckValidity (or via the -pos-min /
// -neg-max flags in cmd/quality). Tight margins by design: a brittle
// gate that occasionally fails is more informative than a loose gate
// that always passes.
const (
	DefaultPosMin = 0.70
	DefaultNegMax = 0.30
)

// Grade is the per-claim verdict. ERROR is a NEW bucket separate from
// UNCLEAR — the prior implementation collapsed grader API failures
// into UNCLEAR, double-counting them and making printed totals
// unreconcilable to TotalClaims.
type Grade string

const (
	GradeSubstantive Grade = "SUBSTANTIVE"
	GradeFiller      Grade = "FILLER"
	GradeUnclear     Grade = "UNCLEAR"
	GradeError       Grade = "ERROR"
)

// GradedClaim pairs a claim with its verdict. Err is set when Grade ==
// GradeError; nil otherwise.
type GradedClaim struct {
	Claim types.Claim
	Grade Grade
	Err   error
}

// FixtureResult is one fixture's grading outcome. Counters are
// mutually exclusive — a grader-API-failed claim goes ONLY into Errors
// (not also into Unclear).
type FixtureResult struct {
	Fixture             string
	TotalClaims         int
	Substantive         int
	Filler              int
	Unclear             int
	Errors              int
	Samples             []GradedClaim
	WallClockSecs       float64
	GraderPromptVersion int
}

// SubstantiveRate is over (Substantive + Filler) — Unclear and Errors
// are excluded so they don't bias the denominator. Returns 0 when the
// denominator is zero; callers gating on this MUST check denominator
// separately via Gradable().
func (r FixtureResult) SubstantiveRate() float64 {
	d := r.Substantive + r.Filler
	if d == 0 {
		return 0
	}
	return float64(r.Substantive) / float64(d)
}

// Gradable returns Substantive + Filler — the denominator of
// SubstantiveRate. Used by the validity gate to require a minimum
// sample size before accepting a rate.
func (r FixtureResult) Gradable() int { return r.Substantive + r.Filler }

// ValidityVerdict is the gate's decision plus diagnostic data. Kind is
// the machine-readable failure code (or VerdictValid on success);
// Reason is the human-readable prose. Downstream automation should
// route on Kind, not on Reason's string content.
type ValidityVerdict struct {
	Valid   bool
	Kind    VerdictKind
	Reason  string
	PosRate float64
	NegRate float64
	PosN    int // gradable count for pos control
	NegN    int // gradable count for neg control
}

// VerdictKind is a machine-readable failure code so downstream
// automation can route on the failure reason without string-parsing
// the prose Reason field.
type VerdictKind string

const (
	VerdictValid     VerdictKind = "VALID"
	VerdictPosSmallN VerdictKind = "POS_SMALL_N"
	VerdictNegSmallN VerdictKind = "NEG_SMALL_N"
	VerdictPosLow    VerdictKind = "POS_BELOW_MIN"
	VerdictNegHigh   VerdictKind = "NEG_ABOVE_MAX"
)

// CheckValidity gates main-fixture interpretation on the controls. The
// gate fails (with a descriptive reason + machine-readable Kind) when:
//
//   - Either control has fewer than minGradable gradable claims
//     (extractor or grader failure collapsed the denominator —
//     SubstantiveRate=0 would otherwise pass the neg gate spuriously)
//   - Pos rate is below posMin (grader is too strict, or pos control
//     is too weak)
//   - Neg rate is above negMax (grader is too lax, or neg control is
//     too substantive)
//
// minGradable is a parameter (not a hardcoded constant) so callers can
// override it via CLI flag without editing source — matches the
// pos/negMax knobs.
func CheckValidity(pos, neg FixtureResult, posMin, negMax float64, minGradable int) ValidityVerdict {
	v := ValidityVerdict{
		PosRate: pos.SubstantiveRate(),
		NegRate: neg.SubstantiveRate(),
		PosN:    pos.Gradable(),
		NegN:    neg.Gradable(),
	}
	if v.PosN < minGradable {
		v.Kind = VerdictPosSmallN
		v.Reason = fmt.Sprintf("pos control has only %d gradable claims (need >= %d) — extractor or grader degraded", v.PosN, minGradable)
		return v
	}
	if v.NegN < minGradable {
		v.Kind = VerdictNegSmallN
		v.Reason = fmt.Sprintf("neg control has only %d gradable claims (need >= %d) — extractor or grader degraded", v.NegN, minGradable)
		return v
	}
	if v.PosRate < posMin {
		v.Kind = VerdictPosLow
		v.Reason = fmt.Sprintf("pos rate %.2f below posMin %.2f — grader is too strict or pos control too weak", v.PosRate, posMin)
		return v
	}
	if v.NegRate > negMax {
		v.Kind = VerdictNegHigh
		v.Reason = fmt.Sprintf("neg rate %.2f above negMax %.2f — grader is too lax or neg control too substantive", v.NegRate, negMax)
		return v
	}
	v.Kind = VerdictValid
	v.Valid = true
	return v
}

// TruncateRunes truncates s to at most maxBytes, cutting at a rune
// boundary so the result is always valid UTF-8. Replaces a naive
// `s[:maxBytes]` which can split a multi-byte rune (em-dashes, smart
// quotes, accented characters — all common in real markdown) and emit
// invalid UTF-8 downstream.
//
// Does NOT append a truncation marker — the grader prompt assembly is
// responsible for adding any uniform end-of-source terminator. Adding
// a marker only on truncation created an asymmetric signal between
// short fixtures (no marker) and long ones (marker), reintroducing
// the very main-vs-control bias the unified cap was meant to remove.
//
// Guards: maxBytes <= 0 returns the empty string; a pure byte-based
// `s[:maxBytes]` with negative maxBytes would panic at runtime.
func TruncateRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// Step back to the start of the rune at or before maxBytes.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Grader holds the API client + model + per-call knobs. SourceCap
// defaults to SourceCapBytes when zero; Concurrency must be set to >= 1
// at construction (RunFixture clamps defensively, but the caller should
// set it deliberately).
//
// Validity-gate thresholds (PosMin / NegMax / MinGradable) are NOT
// fields on Grader — they're args to CheckValidity. The previous design
// had write-only PosMin/NegMax fields here that no callee read; the
// type system claimed they were knobs while the code ignored them.
type Grader struct {
	Client      *anthropic.Client
	Model       string
	SourceCap   int // bytes; defaults to SourceCapBytes
	Concurrency int // grader-call concurrency; must be >= 1
}

// graderToolSchema is built ONCE at package init from a literal struct
// (no JSON marshal/unmarshal round-trip; the previous implementation
// did the round-trip ~825 times per eval AND swallowed the unmarshal
// error, which would have silently shipped an empty schema if the SDK
// ever changed ToolInputSchemaParam's JSON shape).
var graderToolSchema = anthropic.ToolInputSchemaParam{
	Properties: map[string]any{
		"grade": map[string]any{
			"type": "string",
			"enum": []string{"SUBSTANTIVE", "FILLER", "UNCLEAR"},
		},
	},
	Required: []string{"grade"},
}

// GraderRubric is the substantiveness-vs-filler rubric the grader sees.
// Exported so quality.go's --print-rubric flag or future audit tooling
// can surface what the grader was actually asked. Reads as a constant
// because bumping GraderPromptVersion is the only correct way to change
// it.
const GraderRubric = `You audit a claim-extraction system used by a reasoning-topology mapper. The system aggressively extracts claims for graph analysis; downstream detectors look for LOAD-BEARING assertions — claims that other claims depend on, that constrain reasoning, or that could be argued against. Judge each extracted claim by whether a downstream detector would care about it.

SUBSTANTIVE — the claim does one of these:
- States a generalization, principle, or rule
- Records a decision with consequences
- Asserts an opinion or evaluation that could be argued against
- States a fact that CONSTRAINS other reasoning (not just any true fact)
- Names a causal claim, mechanism, or trade-off
- Frames a problem the reader is expected to act on

FILLER — the claim is one of these, EVEN IF technically true:
- Mundane status updates with no downstream implication
- Routine scheduling or workflow chatter
- Acknowledgments and dialogue glue
- Trivial observations of routine events
- List items without context (a grocery item, a single name, a temperature reading)
- Near-tautologies, restatements of what was just said, content-free filler

A fact alone is NOT enough — the question is whether the fact informs any further reasoning, decision, or generalization. A true-but-isolated fact is FILLER; a fact whose negation would break the surrounding argument is SUBSTANTIVE.

Use UNCLEAR ONLY if the source itself doesn't give you enough context to decide. When in doubt between SUBSTANTIVE and FILLER, ask: "would a downstream detector that hunts for load-bearing claims surface this as one?" If no, FILLER.`

// sourceTerminator is appended to EVERY grader prompt's source block,
// truncated or not. Removes the asymmetric signal where only long
// fixtures bore a "truncated" marker — the grader saw a different
// trailing token for main vs controls and could lean UNCLEAR on main.
// Uniform terminator means the grader receives the same shape signal
// for all calls regardless of fixture size.
const sourceTerminator = "\n<END OF VISIBLE SOURCE>"

// buildGraderPrompt assembles the per-claim grader prompt. The rubric
// lives in the System block (caller responsibility) with cache_control;
// this function builds only the per-claim USER message so the rubric
// isn't re-billed as uncached input on every call.
//
// Anti-injection: the rubric in the System block is in a higher-trust
// frame than this user message, so directives smuggled through source
// or claim content cannot override it. Triple-backtick fencing on the
// source (not triple-quote) since markdown sources may legitimately
// contain triple-quote sequences.
func buildGraderPrompt(source string, claim types.Claim) string {
	var b strings.Builder
	b.Grow(len(source) + 256)
	b.WriteString("Source document:\n```\n")
	b.WriteString(source)
	b.WriteString(sourceTerminator)
	b.WriteString("\n```\n\nExtracted claim:\n  text: ")
	b.WriteString(claim.Text)
	b.WriteString("\n  basis: ")
	b.WriteString(string(claim.Basis))
	b.WriteString("\n  speaker: ")
	b.WriteString(string(claim.Speaker))
	fmt.Fprintf(&b, "\n  confidence: %.2f\n\n", claim.Confidence)
	b.WriteString("Per the rubric in the system block, call the grade tool with your verdict.")
	return b.String()
}

// GradeClaim asks the grader model whether one claim is substantive
// given the surrounding source. The caller is expected to have already
// truncated source with TruncateRunes(source, SourceCapBytes); we do
// NOT re-truncate per call so concurrent callers share a single
// truncated string instead of re-slicing the full file N times.
//
// Returns GradeError + err on any path that would otherwise silently
// degrade to UNCLEAR (API error, schema-noncompliant tool output,
// missing tool_use block, json.Unmarshal failure, max_tokens
// truncation). The previous implementation collapsed all of these into
// (GradeUnclear, nil), masking grader breakage as legitimate ambiguity.
func (g *Grader) GradeClaim(ctx context.Context, source string, claim types.Claim) (Grade, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := g.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.Model(g.Model),
		// Grader returns a single tool_use with one enum-typed field
		// (~5-15 tokens). 64 leaves headroom for the model to emit a
		// brief reasoning preamble before the tool call without
		// triggering the max_tokens guard, while still being tight
		// enough that the guard fires on actual runaway output.
		MaxTokens: 64,
		// System block carries the rubric prefix so Anthropic can cache
		// it across the ~275 per-fixture calls. cache_control on the
		// system block matches the pattern internal/extract/extract.go
		// uses (line 90).
		System: []anthropic.TextBlockParam{
			{
				Text:         GraderRubric,
				CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral", TTL: anthropic.CacheControlEphemeralTTLTTL1h},
			},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildGraderPrompt(source, claim))),
		},
		Tools: []anthropic.ToolUnionParam{
			{
				OfTool: &anthropic.ToolParam{
					Name:        "grade",
					Description: anthropic.String("Record the substantiveness grade for the extracted claim."),
					InputSchema: graderToolSchema,
				},
			},
		},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: "grade"},
		},
	})
	if err != nil {
		return GradeError, fmt.Errorf("grader API call: %w", err)
	}
	if resp.StopReason == "max_tokens" {
		return GradeError, errors.New("grader response truncated (max_tokens) — increase MaxTokens or shorten rubric")
	}
	for _, block := range resp.Content {
		if block.Type != "tool_use" || block.Name != "grade" {
			continue
		}
		var input struct {
			Grade string `json:"grade"`
		}
		if err := json.Unmarshal([]byte(block.Input), &input); err != nil {
			return GradeError, fmt.Errorf("parse grader tool input: %w", err)
		}
		switch Grade(input.Grade) {
		case GradeSubstantive, GradeFiller, GradeUnclear:
			return Grade(input.Grade), nil
		default:
			return GradeError, fmt.Errorf("grader returned out-of-enum value %q (schema noncompliance)", input.Grade)
		}
	}
	return GradeError, errors.New("grader returned no grade tool_use block")
}

// RunFixture extracts claims from one fixture (single run, no
// variance) and grades each. Returns the per-fixture result. Uses a
// fresh tempdir per call so graphs don't accumulate, and respects ctx
// cancellation across all sub-calls.
//
// Source is truncated ONCE here, then the truncated string is shared
// by all grader goroutines — the previous implementation passed the
// full source into each goroutine and truncated inside GradeClaim, so
// N copies of the full fixture lived concurrently in memory.
func (g *Grader) RunFixture(ctx context.Context, apiKey, extractModel, fixturePath string) (FixtureResult, error) {
	t0 := time.Now()
	dir, err := os.MkdirTemp("", "quality-*")
	if err != nil {
		return FixtureResult{}, err
	}
	defer os.RemoveAll(dir)

	// Project name is the basename of the fixture path; store.Open
	// handles path-character sanitization, so we don't roll our own.
	project := "quality-" + filepath.Base(fixturePath)
	db, err := store.Open(dir, project)
	if err != nil {
		return FixtureResult{}, err
	}
	defer db.Close()

	ext := extract.New(apiKey, extractModel)
	if _, err := mcp.CoreIngestDocument(ctx, db, ext, project, fixturePath, 0); err != nil {
		return FixtureResult{}, fmt.Errorf("ingest %s: %w", fixturePath, err)
	}
	claims, err := db.GetClaimsByProject(project)
	if err != nil {
		return FixtureResult{}, fmt.Errorf("read claims: %w", err)
	}

	rawSource, err := os.ReadFile(fixturePath)
	if err != nil {
		return FixtureResult{}, err
	}
	srcCap := g.SourceCap
	if srcCap <= 0 {
		srcCap = SourceCapBytes
	}
	// Truncate ONCE up here; all goroutines share the same truncated
	// string. Apply same cap to controls AND main so the grader sees
	// the same context conditions across all three fixtures (the gate
	// validates the configuration that's judging main).
	source := TruncateRunes(string(rawSource), srcCap)

	concurrency := g.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	graded := make([]GradedClaim, len(claims))
	var errCount atomic.Int64
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
dispatch:
	for i := range claims {
		// Labeled break so cancellation actually exits the loop. The
		// select on `sem <- struct{}{}` is also ctx-aware so a stalled
		// grader (all goroutines blocked) doesn't trap the loop past
		// cancellation either.
		select {
		case <-ctx.Done():
			// Mark unstarted claims as cancellation errors so totals
			// reconcile (TotalClaims == Substantive+Filler+Unclear+Errors)
			// and the operator can distinguish "user cancelled" from
			// "grader was broken." Without this fill, indices >= i stay
			// zero-valued (Grade="") and silently drop from all buckets.
			for j := i; j < len(claims); j++ {
				graded[j] = GradedClaim{Claim: claims[j], Grade: GradeError, Err: ctx.Err()}
				errCount.Add(1)
			}
			break dispatch
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			// Race-guard: cancellation may have fired between sem-send
			// and goroutine start. Without this check, ~half the
			// post-cancel dispatches would still issue a doomed API
			// call and inflate errCount with phantom context.Canceled
			// errors that look like grader bugs.
			if err := ctx.Err(); err != nil {
				graded[i] = GradedClaim{Claim: claims[i], Grade: GradeError, Err: err}
				errCount.Add(1)
				return
			}
			gr, err := g.GradeClaim(ctx, source, claims[i])
			graded[i] = GradedClaim{Claim: claims[i], Grade: gr, Err: err}
			if err != nil {
				errCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	result := FixtureResult{
		Fixture:             fixturePath,
		TotalClaims:         len(claims),
		Errors:              int(errCount.Load()),
		WallClockSecs:       time.Since(t0).Seconds(),
		GraderPromptVersion: GraderPromptVersion,
	}
	var subSamples, fillSamples, unclSamples, errSamples []GradedClaim
	for _, gc := range graded {
		switch gc.Grade {
		case GradeSubstantive:
			result.Substantive++
			if len(subSamples) < 5 {
				subSamples = append(subSamples, gc)
			}
		case GradeFiller:
			result.Filler++
			if len(fillSamples) < 5 {
				fillSamples = append(fillSamples, gc)
			}
		case GradeUnclear:
			result.Unclear++
			if len(unclSamples) < 5 {
				unclSamples = append(unclSamples, gc)
			}
		case GradeError:
			if len(errSamples) < 5 {
				errSamples = append(errSamples, gc)
			}
		}
	}
	result.Samples = append(result.Samples, subSamples...)
	result.Samples = append(result.Samples, fillSamples...)
	result.Samples = append(result.Samples, unclSamples...)
	result.Samples = append(result.Samples, errSamples...)
	// Stable sort by grade so the displayed sample list is
	// deterministic and grouped.
	sort.SliceStable(result.Samples, func(i, j int) bool {
		return result.Samples[i].Grade < result.Samples[j].Grade
	})
	// Propagate ctx cancellation so the caller can distinguish a clean
	// run from a SIGINT-truncated one. Result still carries the partial
	// data (counts now reconcile thanks to the dispatch-loop cancel-fill
	// above) so callers that want to print partial output still can.
	return result, ctx.Err()
}

// (sanitizeForProject removed — store.Open already sanitizes the
// project arg via its own internal sanitizer. The local helper was
// redundant and risked drifting from store's canonical rules.)
