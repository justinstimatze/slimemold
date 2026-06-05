// Command quality is the CLI shim over internal/qualityharness. It
// measures extraction substantiveness (load-bearing vs filler) using a
// separate Haiku grader, gated by pos/neg control fixtures.
//
// Lives in cmd/ rather than as a //go:build ignore script in
// benchmarks/ so `go build ./...` and `go vet ./...` catch signature
// drift between the shim and the internal package. Matches the
// precedent set by cmd/perfprobe/.
//
// Usage:
//
//	ANTHROPIC_API_KEY=... go run ./cmd/quality [flags]
//	go install ./cmd/quality && quality [flags]
//
// Grading logic, validity gate, rune-safe truncation, schema
// construction, and prompt assembly all live in
// internal/qualityharness/ and have unit-test coverage. This file is
// just argv parsing + output formatting.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/justinstimatze/slimemold/internal/qualityharness"
)

// main is a thin wrapper around run() so deferred cleanup (signal.Stop,
// cancel(), RunFixture's tempdir RemoveAll) actually fires before exit.
// `os.Exit` skips defers; routing all exit paths through a returned int
// keeps cleanup honest.
func main() {
	os.Exit(run())
}

func run() int {
	fixturePath := flag.String("fixture", "README.md", "main fixture to grade")
	posFixture := flag.String("pos-fixture", "benchmarks/variance/fixtures/positive_control.md", "positive control fixture")
	negFixture := flag.String("neg-fixture", "benchmarks/variance/fixtures/negative_control.md", "negative control fixture")
	graderModel := flag.String("grader-model", "claude-haiku-4-5-20251001", "model used for per-claim grading")
	extractModel := flag.String("extract-model", "", "extraction model (defaults to SLIMEMOLD_MODEL or claude-sonnet-4-6)")
	concurrency := flag.Int("concurrency", 10, "max concurrent grader calls (must be >= 1)")
	posMin := flag.Float64("pos-min", qualityharness.DefaultPosMin, "positive-control substantive rate must be >= this")
	negMax := flag.Float64("neg-max", qualityharness.DefaultNegMax, "negative-control substantive rate must be <= this")
	minGradable := flag.Int("min-gradable", qualityharness.MinGradableForValid, "min gradable claims per control before the gate can pass")
	timeout := flag.Duration("timeout", 30*time.Minute, "total wall-clock cap; aborts on SIGINT/SIGTERM or this deadline")
	controlsOnly := flag.Bool("controls-only", false, "run pos+neg controls and exit (skip main fixture — useful when re-tuning)")
	flag.Parse()

	if *concurrency < 1 {
		fmt.Fprintln(os.Stderr, "concurrency must be >= 1")
		return 1
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
		return 1
	}
	if *extractModel == "" {
		*extractModel = os.Getenv("SLIMEMOLD_MODEL")
		if *extractModel == "" {
			*extractModel = "claude-sonnet-4-6"
		}
	}

	fmt.Printf("quality harness (grader-prompt v%d):\n  extract = %s\n  grader  = %s\n  concurrency = %d\n  pos-min/neg-max = %.2f / %.2f  (min-gradable=%d)\n  timeout = %s\n\n",
		qualityharness.GraderPromptVersion, *extractModel, *graderModel, *concurrency, *posMin, *negMax, *minGradable, *timeout)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nquality: caught interrupt; cancelling (tempdirs cleaned by deferred RemoveAll)")
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	grader := &qualityharness.Grader{
		Client:      &client,
		Model:       *graderModel,
		Concurrency: *concurrency,
	}

	fmt.Println("=== Positive control ===")
	pos, err := grader.RunFixture(ctx, apiKey, *extractModel, *posFixture)
	// Print whatever partial data we got before bailing on cancellation.
	printResult(pos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "positive control aborted: %v\n", err)
		return 130 // canonical SIGINT exit code; also covers timeout
	}

	fmt.Println("\n=== Negative control ===")
	neg, err := grader.RunFixture(ctx, apiKey, *extractModel, *negFixture)
	printResult(neg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "negative control aborted: %v\n", err)
		return 130
	}

	verdict := qualityharness.CheckValidity(pos, neg, *posMin, *negMax, *minGradable)
	fmt.Printf("\n=== Validity gate ===\n")
	fmt.Printf("pos substantive: %.2f  (n=%d gradable, need >= %.2f, n >= %d)\n", verdict.PosRate, verdict.PosN, *posMin, *minGradable)
	fmt.Printf("neg substantive: %.2f  (n=%d gradable, need <= %.2f, n >= %d)\n", verdict.NegRate, verdict.NegN, *negMax, *minGradable)
	if !verdict.Valid {
		fmt.Printf("\nINVALID [%s] — %s\n", verdict.Kind, verdict.Reason)
		fmt.Printf("Tune the grader prompt (internal/qualityharness/qualityharness.go GraderRubric, bump GraderPromptVersion) or the control fixtures (benchmarks/variance/fixtures/).\n")
		return 2
	}
	fmt.Printf("VALID [%s] — controls passed.\n", verdict.Kind)
	if *controlsOnly {
		fmt.Printf("(-controls-only set, skipping main fixture)\n")
		return 0
	}
	fmt.Printf("Interpreting main-fixture result below.\n\n")

	fmt.Println("=== Main fixture ===")
	mainResult, err := grader.RunFixture(ctx, apiKey, *extractModel, *fixturePath)
	printResult(mainResult)
	if err != nil {
		fmt.Fprintf(os.Stderr, "main fixture aborted: %v\n", err)
		return 130
	}

	mainRate := mainResult.SubstantiveRate()
	fmt.Printf("\n=== Verdict ===\n")
	fmt.Printf("%s: substantive rate = %.2f (%d/%d gradable claims)\n",
		*fixturePath, mainRate, mainResult.Substantive, mainResult.Gradable())
	fmt.Printf("filler rate: %.2f (%d claims judged filler)\n", 1-mainRate, mainResult.Filler)
	if mainResult.Errors > 0 {
		fmt.Printf("warning: %d grader errors (excluded from rate denominator — investigate before trusting the number)\n", mainResult.Errors)
	}
	switch {
	case mainRate >= 0.85:
		fmt.Println("interpretation: extraction is precision-heavy — almost all extracted claims are substantive.")
	case mainRate >= 0.70:
		fmt.Println("interpretation: extraction is in a healthy precision band — most claims are substantive.")
	case mainRate >= 0.50:
		fmt.Println("interpretation: filler rate is non-trivial. Consider whether the aggressive-recall prompt is over-extracting on this content.")
	default:
		fmt.Println("interpretation: filler dominates. Extraction is over-recalling — most extracted claims are not substantive.")
	}
	return 0
}

// printResult formats one fixture's outcome. RunFixture already sorts
// Samples by Grade before returning, so no re-sort here.
func printResult(r qualityharness.FixtureResult) {
	rate := r.SubstantiveRate()
	fmt.Printf("fixture:       %s\n", r.Fixture)
	fmt.Printf("grader-prompt: v%d\n", r.GraderPromptVersion)
	fmt.Printf("claims:        %d total (%d substantive, %d filler, %d unclear, %d grader-errors)\n",
		r.TotalClaims, r.Substantive, r.Filler, r.Unclear, r.Errors)
	fmt.Printf("rate:          %.2f substantive (over %d gradable; unclear+errors excluded)\n", rate, r.Gradable())
	fmt.Printf("wall-clock:    %.1fs\n", r.WallClockSecs)
	if len(r.Samples) > 0 {
		fmt.Println("samples (first 5 per category, grouped by grade):")
		for _, gc := range r.Samples {
			// Rune-safe truncation — naive `t[:77]` would split a
			// multi-byte UTF-8 rune (em-dashes, smart quotes) and emit
			// a mojibake terminal artifact. TruncateRunes lives in
			// the same package precisely to prevent this.
			t := gc.Claim.Text
			if len(t) > 80 {
				t = qualityharness.TruncateRunes(t, 77) + "..."
			}
			extra := ""
			if gc.Grade == qualityharness.GradeError && gc.Err != nil {
				extra = fmt.Sprintf("  err=%v", gc.Err)
			}
			fmt.Printf("  [%s] %s%s\n", gc.Grade, t, extra)
		}
	}
}
