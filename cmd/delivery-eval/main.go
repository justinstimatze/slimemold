// Command delivery-eval is the CLI shim over internal/deliveryharness.
// It measures whether the host model acts on slimemold's freshly
// injected findings, and whether that act-on rate decays as context
// length grows (50k / 100k / 150k tokens).
//
// Subcommands:
//
//	print-prompt        — emit the grader prompt for a sample (no API).
//	                      Useful for sanity-checking before paying.
//	controls            — run pos + neg cells only (~$0.50).
//	                      Validates the grader before the full matrix.
//	matrix              — run A/B at each length plus pos/neg/negLong.
//	                      Key-gated; ~$4 per transcript at default Samples.
//
// Lives in cmd/ rather than benchmarks/ so `go build ./...` and
// `go vet ./...` catch signature drift between shim and harness, per
// the cmd/quality / cmd/perfprobe precedent. See
// cmd/delivery-eval/DESIGN.md for the full experimental design.
//
// Usage:
//
//	go run ./cmd/delivery-eval print-prompt
//	ANTHROPIC_API_KEY=... go run ./cmd/delivery-eval controls
//	ANTHROPIC_API_KEY=... go run ./cmd/delivery-eval matrix -transcript=/path/to/jsonl
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/justinstimatze/slimemold/internal/deliveryharness"
)

const (
	defaultHostModel   = "claude-sonnet-4-6"
	defaultGraderModel = "claude-haiku-4-5-20251001"

	defaultSamples       = 15
	defaultConcurrency   = 5
	defaultHostMaxTokens = 1024
	defaultGraderMaxT    = 32
	defaultHostTemp      = 0.7

	defaultHostSystem = "You are a helpful coding assistant collaborating with the user on a software project. " +
		"Be specific and grounded. If a claim in the conversation seems unverified or load-bearing, " +
		"call it out and ask for the source before building further on it."
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	switch os.Args[1] {
	case "print-prompt":
		return cmdPrintPrompt(os.Args[2:])
	case "controls":
		return cmdControls(os.Args[2:])
	case "matrix":
		return cmdMatrix(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `delivery-eval — measure whether slimemold's findings actually move the host model
subcommands:
  print-prompt   emit the grader prompt for one fixture (no API)
  controls       run pos + neg only (~$0.50)
  matrix         run A/B at lengths + pos/neg/negLong (~$4/transcript)`)
}

// --- print-prompt -------------------------------------------------------

func cmdPrintPrompt(args []string) int {
	fs := flag.NewFlagSet("print-prompt", flag.ExitOnError)
	fixtureIdx := fs.Int("fixture", 0, "fixture index (0-based) into deliveryharness.Fixtures()")
	cond := fs.String("cond", "B", "condition: A, B, pos, neg")
	exampleResp := fs.String("example-response", "Sure, pushing now.",
		"a placeholder assistant response to show in the grader prompt")
	_ = fs.Parse(args)

	fixtures := deliveryharness.Fixtures()
	if *fixtureIdx < 0 || *fixtureIdx >= len(fixtures) {
		fmt.Fprintf(os.Stderr, "fixture index %d out of range [0, %d)\n", *fixtureIdx, len(fixtures))
		return 2
	}
	f := fixtures[*fixtureIdx]
	var userTurn string
	switch strings.ToUpper(*cond) {
	case "A", "B":
		userTurn = f.Main
	case "POS":
		userTurn = f.PosTurn
	case "NEG":
		userTurn = f.NegTurn
	default:
		fmt.Fprintf(os.Stderr, "unknown cond %q\n", *cond)
		return 2
	}

	prompt := deliveryharness.BuildGraderPrompt(deliveryharness.GraderInput{
		FindingText:       f.Finding,
		UserTurn:          userTurn,
		AssistantResponse: *exampleResp,
	})
	fmt.Println(prompt)
	return 0
}

// --- controls -----------------------------------------------------------

func cmdControls(args []string) int {
	fs := flag.NewFlagSet("controls", flag.ExitOnError)
	fixtureIdx := fs.Int("fixture", 0, "fixture index into deliveryharness.Fixtures()")
	hostModel := fs.String("host-model", defaultHostModel, "host model id")
	graderModel := fs.String("grader-model", defaultGraderModel, "grader model id")
	samples := fs.Int("samples", defaultSamples, "N samples per cell")
	concurrency := fs.Int("concurrency", defaultConcurrency, "max in-flight host calls")
	hostTemp := fs.Float64("host-temp", defaultHostTemp, "host sampling temperature")
	hostMaxTokens := fs.Int("host-max-tokens", defaultHostMaxTokens, "host max output tokens")
	cacheDir := fs.String("cache-dir", deliveryharness.DefaultCacheDir(), "cache dir; empty disables caching")
	timeout := fs.Duration("timeout", 30*time.Minute, "total wall-clock budget")
	_ = fs.Parse(args)

	r, fix, ok := buildRunner(*hostModel, *graderModel, *samples, *concurrency,
		*hostTemp, *hostMaxTokens, *cacheDir, *fixtureIdx)
	if !ok {
		return 1
	}

	ctx, cancel := contextWithTimeoutAndSignal(*timeout)
	defer cancel()

	posCell := deliveryharness.BuildCell(deliveryharness.CondPos, 0, nil, fix.Finding, fix.PosTurn)
	negCell := deliveryharness.BuildCell(deliveryharness.CondNeg, 0, nil, fix.Finding, fix.NegTurn)

	fmt.Printf("=== Controls (fixture %d) ===\n", *fixtureIdx)
	fmt.Printf("host=%s  grader=%s  samples=%d  concurrency=%d  cache=%s\n\n",
		*hostModel, *graderModel, *samples, *concurrency, displayCacheDir(*cacheDir))
	fmt.Printf("finding: %s\n\n", fix.Finding)

	posOut := r.RunCell(ctx, posCell)
	printCellOutcome("pos", posOut)
	if ctx.Err() != nil {
		return 130
	}
	negOut := r.RunCell(ctx, negCell)
	printCellOutcome("neg", negOut)
	if ctx.Err() != nil {
		return 130
	}

	gate := deliveryharness.DefaultGate()
	res := gate.Check(map[string]deliveryharness.CellRate{
		"pos": posOut.Rate,
		"neg": negOut.Rate,
	})
	fmt.Printf("\n=== Gate ===\n")
	fmt.Printf("pos rate: %.2f (need ≥ %.2f)\n", res.PosRate, gate.PosMin)
	fmt.Printf("neg rate: %.2f (need ≤ %.2f)\n", res.NegRate, gate.NegMax)
	if res.Valid {
		fmt.Printf("VALID — %s\n", res.Reason)
		return 0
	}
	fmt.Printf("INVALID — %s\n", res.Reason)
	return 2
}

// --- matrix -------------------------------------------------------------

func cmdMatrix(args []string) int {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	fixtureIdx := fs.Int("fixture", 0, "fixture index into deliveryharness.Fixtures()")
	hostModel := fs.String("host-model", defaultHostModel, "host model id")
	graderModel := fs.String("grader-model", defaultGraderModel, "grader model id")
	samples := fs.Int("samples", defaultSamples, "N samples per cell")
	concurrency := fs.Int("concurrency", defaultConcurrency, "max in-flight host calls")
	hostTemp := fs.Float64("host-temp", defaultHostTemp, "host sampling temperature")
	hostMaxTokens := fs.Int("host-max-tokens", defaultHostMaxTokens, "host max output tokens")
	cacheDir := fs.String("cache-dir", deliveryharness.DefaultCacheDir(), "cache dir; empty disables caching")
	timeout := fs.Duration("timeout", 60*time.Minute, "total wall-clock budget")
	transcript := fs.String("transcript", "",
		"path to a Claude Code .jsonl transcript to load as long-context filler (required)")
	lengthsStr := fs.String("lengths", "50000,100000,150000",
		"comma-separated target context lengths in tokens")
	_ = fs.Parse(args)

	if *transcript == "" {
		fmt.Fprintln(os.Stderr, "matrix: -transcript is required")
		return 2
	}
	lengths, err := parseLengths(*lengthsStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	r, fix, ok := buildRunner(*hostModel, *graderModel, *samples, *concurrency,
		*hostTemp, *hostMaxTokens, *cacheDir, *fixtureIdx)
	if !ok {
		return 1
	}

	ctx, cancel := contextWithTimeoutAndSignal(*timeout)
	defer cancel()

	// Load context once at the longest target; trim down for each
	// shorter target. Avoids re-parsing the JSONL N times.
	maxLen := lengths[0]
	for _, L := range lengths {
		if L > maxLen {
			maxLen = L
		}
	}
	longContext, err := deliveryharness.LoadRealContext(*transcript, maxLen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load transcript: %v\n", err)
		return 1
	}
	contexts := map[int][]deliveryharness.TextTurn{}
	for _, L := range lengths {
		contexts[L] = trimToTokens(longContext, L)
	}
	shortContext := trimToTokens(longContext, 5000)

	cells := deliveryharness.BuildMatrix(fix, lengths, contexts, shortContext)

	fmt.Printf("=== Matrix (fixture %d, transcript %s) ===\n", *fixtureIdx, *transcript)
	fmt.Printf("host=%s  grader=%s  samples=%d  concurrency=%d  lengths=%v  cache=%s\n\n",
		*hostModel, *graderModel, *samples, *concurrency, lengths, displayCacheDir(*cacheDir))
	fmt.Printf("finding: %s\n\n", fix.Finding)

	rates := map[string]deliveryharness.CellRate{}
	for _, cell := range cells {
		out := r.RunCell(ctx, cell)
		printCellOutcome(cell.Name, out)
		rates[cell.Name] = out.Rate
		if ctx.Err() != nil {
			return 130
		}
	}

	// Validity gate uses the short-context pos/neg + negLong if present.
	gateCells := map[string]deliveryharness.CellRate{
		"pos": rates["pos"],
		"neg": rates["neg"],
	}
	if nl, ok := rates["negLong"]; ok {
		gateCells["negLong"] = nl
	}
	gate := deliveryharness.DefaultGate()
	res := gate.Check(gateCells)
	fmt.Printf("\n=== Gate ===\n")
	fmt.Printf("pos rate:    %.2f (need ≥ %.2f)\n", res.PosRate, gate.PosMin)
	fmt.Printf("neg rate:    %.2f (need ≤ %.2f)\n", res.NegRate, gate.NegMax)
	if res.NegLongRate != nil {
		fmt.Printf("negLong:     %.2f (need ≤ %.2f)\n", *res.NegLongRate, gate.NegMax)
	}
	if !res.Valid {
		fmt.Printf("INVALID — %s\n", res.Reason)
		fmt.Printf("Refusing to interpret A/B deltas. Re-tune grader or fixtures.\n")
		return 2
	}
	fmt.Printf("VALID — %s\n\n", res.Reason)

	fmt.Println("=== B − A deltas ===")
	names := sortedKeys(rates)
	for _, L := range lengths {
		a := rates[fmt.Sprintf("A@%d", L)]
		b := rates[fmt.Sprintf("B@%d", L)]
		fmt.Printf("L=%-7d  A=%.2f  B=%.2f  Δ=%+.2f  (n=%d/%d)\n",
			L, a.Rate(), b.Rate(), b.Rate()-a.Rate(), a.Total(), b.Total())
	}
	// Silence the unused-names complaint if rates ever gets walked
	// differently — keeps the function future-proof at zero cost.
	_ = names
	return 0
}

// --- helpers ------------------------------------------------------------

func buildRunner(hostModel, graderModel string, samples, concurrency int,
	hostTemp float64, hostMaxTokens int, cacheDir string, fixtureIdx int,
) (*deliveryharness.Runner, deliveryharness.FixturePair, bool) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
		return nil, deliveryharness.FixturePair{}, false
	}
	fixtures := deliveryharness.Fixtures()
	if fixtureIdx < 0 || fixtureIdx >= len(fixtures) {
		fmt.Fprintf(os.Stderr, "fixture index %d out of range [0, %d)\n", fixtureIdx, len(fixtures))
		return nil, deliveryharness.FixturePair{}, false
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &deliveryharness.Runner{
		Client:            &client,
		HostModel:         hostModel,
		GraderModel:       graderModel,
		HostTemperature:   hostTemp,
		GraderTemperature: 0,
		HostMaxTokens:     hostMaxTokens,
		GraderMaxTokens:   defaultGraderMaxT,
		Samples:           samples,
		Concurrency:       concurrency,
		Cache:             &deliveryharness.Cache{Dir: cacheDir},
		HostSystemPrompt:  defaultHostSystem,
		FixtureLabel:      fmt.Sprintf("fixture-%d", fixtureIdx),
	}, fixtures[fixtureIdx], true
}

func contextWithTimeoutAndSignal(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\ndelivery-eval: caught interrupt; cancelling")
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()
	return ctx, cancel
}

func displayCacheDir(d string) string {
	if d == "" {
		return "(disabled)"
	}
	return d
}

func printCellOutcome(name string, o deliveryharness.CellOutcome) {
	r := o.Rate
	cached := 0
	for _, s := range o.Samples {
		if s.HostFromCache {
			cached++
		}
	}
	fmt.Printf("[%s]  acted=%d ambiguous=%d ignored=%d ungraded=%d  rate=%.2f  cached=%d/%d  wall=%.1fs",
		name, r.ActedOn, r.Ambiguous, r.Ignored, r.Ungraded, r.Rate(), cached, len(o.Samples), o.WallClockSecs)
	if o.HostErrors > 0 {
		fmt.Printf("  host-errs=%d", o.HostErrors)
	}
	if o.GraderErrors > 0 {
		fmt.Printf("  grader-errs=%d", o.GraderErrors)
	}
	fmt.Println()
	if len(o.UngradedReason) > 0 && len(o.UngradedReason) <= 3 {
		for _, why := range o.UngradedReason {
			fmt.Printf("  ungraded reply: %q\n", why)
		}
	}
}

func parseLengths(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid length %q in -lengths", p)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid lengths in %q", s)
	}
	sort.Ints(out)
	return out, nil
}

// trimToTokens cuts the loaded context down to approximately tgt
// tokens. Walks from the end (most-recent first) so we keep the tail
// of the conversation, then reverses back to chronological order.
// LoadRealContext already does its own budget-respecting walk; this
// is the cheap re-trim for shorter lengths within one process.
func trimToTokens(ctx []deliveryharness.TextTurn, tgt int) []deliveryharness.TextTurn {
	if len(ctx) == 0 {
		return ctx
	}
	used := 0
	out := []deliveryharness.TextTurn{}
	for i := len(ctx) - 1; i >= 0; i-- {
		t := ctx[i]
		ttoks := deliveryharness.EstimateTokens(t.Content)
		if used+ttoks > tgt {
			break
		}
		out = append([]deliveryharness.TextTurn{t}, out...)
		used += ttoks
	}
	// Trim to start-user / end-assistant if we cut into the middle.
	for len(out) > 0 && out[0].Role != "user" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1].Role != "assistant" {
		out = out[:len(out)-1]
	}
	return out
}

func sortedKeys(m map[string]deliveryharness.CellRate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
