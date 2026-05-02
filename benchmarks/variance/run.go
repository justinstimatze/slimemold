//go:build ignore

// variance/run.go — measure the noise floor of slimemold's extraction.
//
// Runs the same fixture document through the extraction pipeline N times into
// separate temp project graphs, captures metrics from each run, and prints
// mean ± stddev across runs. The output is the actual sampling-variance
// floor against which any prompt-version comparison should be evaluated.
//
// This addresses the recurring caveat in the README appendix:
// "n=1 per version cannot distinguish prompt-fix-effect from sampling noise."
// Once you have the noise floor on file, future prompt changes can land with
// "this moved beyond the floor" rather than "we hope this helped."
//
// Usage:
//   ANTHROPIC_API_KEY=... go run benchmarks/variance/run.go [-fixture PATH] [-runs N]
//
// Defaults: fixture=README.md, runs=5. Each run costs ~$0.30-0.50 in tokens
// depending on fixture size and model. README.md at runs=5 is roughly $2-3
// total and ~10-20 minutes wall-clock.

package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

// metrics captures the per-run measurements we aggregate across runs.
type metrics struct {
	Claims        int
	Edges         int
	BasisCounts   map[types.Basis]int
	CriticalFinds int
	WarningFinds  int
	InfoFinds     int
	LoadBearing   int
	Bottleneck    int
	UnchallChain  int
	OrphanCount   int
	MaxChainDepth int

	// FindingClaimIDs records anchor claim IDs per finding type per run, so
	// across-run comparison can answer "are the same claims surfaced?" not
	// just "is the count stable?" Especially relevant for bottleneck where
	// the count is capped at 5 — the count tells us nothing; the identities
	// tell us whether the top-N is stable or churns.
	FindingClaimIDs map[string][]string
}

func main() {
	fixturePath := flag.String("fixture", "README.md", "path to document fixture")
	runs := flag.Int("runs", 5, "number of extraction runs")
	model := flag.String("model", "", "extraction model (defaults to SLIMEMOLD_MODEL or claude-sonnet-4-6)")
	flag.Parse()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
		os.Exit(1)
	}
	if *model == "" {
		*model = os.Getenv("SLIMEMOLD_MODEL")
		if *model == "" {
			*model = "claude-sonnet-4-6"
		}
	}

	fixtureBytes, err := os.ReadFile(*fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read fixture %q: %v\n", *fixturePath, err)
		os.Exit(1)
	}
	fmt.Printf("variance harness: fixture=%s (%d bytes), runs=%d, model=%s\n\n", *fixturePath, len(fixtureBytes), *runs, *model)

	allMetrics := make([]metrics, 0, *runs)
	for i := 1; i <= *runs; i++ {
		fmt.Printf("--- run %d/%d ---\n", i, *runs)
		m, err := singleRun(apiKey, *model, *fixturePath, fmt.Sprintf("variance-run-%d", i))
		if err != nil {
			fmt.Fprintf(os.Stderr, "run %d failed: %v\n", i, err)
			os.Exit(1)
		}
		allMetrics = append(allMetrics, m)
		fmt.Printf("    claims=%d edges=%d critical=%d load_bearing=%d bottleneck=%d\n", m.Claims, m.Edges, m.CriticalFinds, m.LoadBearing, m.Bottleneck)
	}

	fmt.Println()
	printAggregates(allMetrics)
	printFindingStability(allMetrics)
}

func singleRun(apiKey, model, fixturePath, project string) (metrics, error) {
	dir, err := os.MkdirTemp("", "variance-*")
	if err != nil {
		return metrics{}, err
	}
	defer os.RemoveAll(dir)

	db, err := store.Open(dir, project)
	if err != nil {
		return metrics{}, err
	}
	defer db.Close()

	ext := extract.New(apiKey, model)
	if _, err := mcp.CoreIngestDocument(context.Background(), db, ext, project, fixturePath, 0); err != nil {
		return metrics{}, fmt.Errorf("ingest: %w", err)
	}

	claims, _ := db.GetClaimsByProject(project)
	edges, _ := db.GetEdgesByProject(project)
	topo, vulns := analysis.Analyze(claims, edges, project)

	m := metrics{
		Claims:          len(claims),
		Edges:           len(edges),
		BasisCounts:     topo.BasisCounts,
		MaxChainDepth:   topo.MaxDepth,
		OrphanCount:     len(topo.Orphans),
		FindingClaimIDs: make(map[string][]string),
	}
	for _, v := range vulns.Items {
		switch v.Severity {
		case "critical":
			m.CriticalFinds++
		case "warning":
			m.WarningFinds++
		case "info":
			m.InfoFinds++
		}
		switch v.Type {
		case "load_bearing_vibes":
			m.LoadBearing++
		case "bottleneck":
			m.Bottleneck++
		case "unchallenged_chain":
			m.UnchallChain++
		}
		// Track claim IDs for findings whose stability across runs is the
		// actually-informative metric (count alone is too coarse).
		switch v.Type {
		case "bottleneck", "load_bearing_vibes", "unchallenged_chain", "fluency_trap":
			if len(v.ClaimIDs) > 0 {
				m.FindingClaimIDs[v.Type] = append(m.FindingClaimIDs[v.Type], v.ClaimIDs[0])
			}
		}
	}
	return m, nil
}

// summary holds mean / stddev / min / max for a metric across runs.
type summary struct {
	Mean, StdDev, Min, Max float64
}

func summarize(values []int) summary {
	if len(values) == 0 {
		return summary{}
	}
	sum := 0.0
	for _, v := range values {
		sum += float64(v)
	}
	mean := sum / float64(len(values))
	variance := 0.0
	for _, v := range values {
		d := float64(v) - mean
		variance += d * d
	}
	variance /= float64(len(values))
	sorted := append([]int{}, values...)
	sort.Ints(sorted)
	return summary{Mean: mean, StdDev: math.Sqrt(variance), Min: float64(sorted[0]), Max: float64(sorted[len(sorted)-1])}
}

func extract_(ms []metrics, get func(metrics) int) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = get(m)
	}
	return out
}

// printFindingStability shows, per finding type, the cross-run intersection
// of claim IDs. A finding type is "stable" when the same claim IDs surface
// across runs (intersection ≈ per-run count). It "churns" when each run
// surfaces different claims (intersection much smaller than per-run count) —
// which means the capped count is hiding real variance, e.g., bottleneck's
// hardcoded cap at 5 makes count alone uninformative.
func printFindingStability(ms []metrics) {
	if len(ms) == 0 {
		return
	}
	fmt.Println("\n=== Finding stability across runs (claim ID intersection) ===")
	fmt.Println("type                 per-run-mean   intersection   stable?")
	types := []string{"bottleneck", "load_bearing_vibes", "unchallenged_chain", "fluency_trap"}
	for _, t := range types {
		// Intersect claim ID sets across runs.
		var inter map[string]bool
		var totalCount int
		for i, m := range ms {
			ids := m.FindingClaimIDs[t]
			totalCount += len(ids)
			set := make(map[string]bool, len(ids))
			for _, id := range ids {
				set[id] = true
			}
			if i == 0 {
				inter = set
				continue
			}
			next := make(map[string]bool)
			for id := range inter {
				if set[id] {
					next[id] = true
				}
			}
			inter = next
		}
		mean := float64(totalCount) / float64(len(ms))
		interN := len(inter)
		var stable string
		switch {
		case mean == 0:
			stable = "n/a (no fires)"
		case float64(interN)/mean > 0.7:
			stable = "stable"
		case float64(interN)/mean > 0.3:
			stable = "partially stable"
		default:
			stable = "churns (count is misleading)"
		}
		fmt.Printf("%-20s %12.1f %14d   %s\n", t, mean, interN, stable)
	}
}

func printAggregates(ms []metrics) {
	fmt.Println("=== Variance summary ===")
	fmt.Printf("%-22s %8s %8s %8s %8s\n", "metric", "mean", "stddev", "min", "max")
	rows := []struct {
		name string
		get  func(metrics) int
	}{
		{"claims", func(m metrics) int { return m.Claims }},
		{"edges", func(m metrics) int { return m.Edges }},
		{"max chain depth", func(m metrics) int { return m.MaxChainDepth }},
		{"orphans", func(m metrics) int { return m.OrphanCount }},
		{"critical findings", func(m metrics) int { return m.CriticalFinds }},
		{"warning findings", func(m metrics) int { return m.WarningFinds }},
		{"info findings", func(m metrics) int { return m.InfoFinds }},
		{"load_bearing", func(m metrics) int { return m.LoadBearing }},
		{"bottleneck", func(m metrics) int { return m.Bottleneck }},
		{"unchallenged_chain", func(m metrics) int { return m.UnchallChain }},
	}
	for _, r := range rows {
		s := summarize(extract_(ms, r.get))
		fmt.Printf("%-22s %8.1f %8.2f %8.0f %8.0f\n", r.name, s.Mean, s.StdDev, s.Min, s.Max)
	}

	allBases := map[types.Basis]struct{}{}
	for _, m := range ms {
		for b := range m.BasisCounts {
			allBases[b] = struct{}{}
		}
	}
	fmt.Println("\nBasis distribution per run:")
	bases := make([]string, 0, len(allBases))
	for b := range allBases {
		bases = append(bases, string(b))
	}
	sort.Strings(bases)
	fmt.Printf("%-12s ", "basis")
	for i := range ms {
		fmt.Printf("%6s ", fmt.Sprintf("run%d", i+1))
	}
	fmt.Printf("%6s %6s\n", "mean", "stddev")
	for _, bs := range bases {
		fmt.Printf("%-12s ", bs)
		vals := make([]int, len(ms))
		for i, m := range ms {
			v := m.BasisCounts[types.Basis(bs)]
			vals[i] = v
			fmt.Printf("%6d ", v)
		}
		s := summarize(vals)
		fmt.Printf("%6.1f %6.2f\n", s.Mean, s.StdDev)
	}
}
