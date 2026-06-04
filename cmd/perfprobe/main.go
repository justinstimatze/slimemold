// perfprobe is a one-off profiler that times each phase of the Stop hook's
// LOCAL work (everything except the LLM API call) against a real on-disk DB.
// Goal: identify where the per-fire cost lives so optimization effort lands
// on the actual hot spot.
//
//	go run ./cmd/perfprobe lucida
//	go run ./cmd/perfprobe lexicon
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/config"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: perfprobe PROJECT")
		os.Exit(1)
	}
	project := os.Args[1]
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg.DataDir, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	fmt.Printf("=== perfprobe: %s ===\n\n", project)

	// Baseline: one load of each, to characterize the per-call cost.
	claims, _ := timedT("GetClaimsByProject (call 1/4)", func() ([]types.Claim, error) {
		return db.GetClaimsByProject(project)
	})
	edges, _ := timedT("GetEdgesByProject (call 1/4)", func() ([]types.Edge, error) {
		return db.GetEdgesByProject(project)
	})

	fmt.Printf("graph: %d claims  %d edges\n\n", len(claims), len(edges))

	// Repeat the loads to characterize SQLite cache behavior. The hook always
	// runs fresh (new process), so cold-cache numbers are what users actually
	// experience.
	for i := 2; i <= 4; i++ {
		_, _ = timedT[[]types.Claim](fmt.Sprintf("GetClaimsByProject (call %d/4)", i), func() ([]types.Claim, error) {
			return db.GetClaimsByProject(project)
		})
		_, _ = timedT[[]types.Edge](fmt.Sprintf("GetEdgesByProject (call %d/4)", i), func() ([]types.Edge, error) {
			return db.GetEdgesByProject(project)
		})
	}

	fmt.Println()

	// CountClaims / CountEdges are extra round-trips on top of the loads.
	_, _ = timedT[int]("CountClaims", func() (int, error) {
		return db.CountClaims(project)
	})
	_, _ = timedT[int]("CountEdges", func() (int, error) {
		return db.CountEdges(project)
	})

	// Hook-fires table query (always called in CoreParseTranscript).
	_, _ = timedT[map[string]time.Time]("RecentHookFireTimes (1h window)", func() (map[string]time.Time, error) {
		return db.RecentHookFireTimes(project, time.Now().Add(-1*time.Hour))
	})

	fmt.Println()

	// analysis.Analyze — the post-extraction structural pass. This is what
	// scales worst with graph size. Run twice to see warm-cache CPU cost.
	for i := 1; i <= 2; i++ {
		_ = timedRet(fmt.Sprintf("analysis.Analyze run %d", i), func() {
			analysis.Analyze(claims, edges, project)
		})
	}

	// Per-detector breakdown — runs the same detectors as Analyze but with
	// wall-clock timing per detector. Surfaces the non-monotonic culprits
	// (cost that depends on graph shape, not just size).
	fmt.Println("\n  per-detector cost (AnalyzeWithProfile):")
	_, _, prof := analysis.AnalyzeWithProfile(claims, edges, project)
	printProfile(prof)

	// FormatHookFindings — runs at the end of every hook fire.
	topo, vulns := analysis.Analyze(claims, edges, project)
	recentFires, _ := db.RecentHookFireTimes(project, time.Now().Add(-1*time.Hour))
	_ = timedRet("FormatHookFindings", func() {
		_, _, _, _ = analysis.FormatHookFindings(topo, vulns, claims, recentFires, 0, 0, 5)
	})

	// Memory footprint snapshot.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\nmem: alloc=%s  total_alloc=%s  sys=%s\n",
		humanBytes(m.Alloc), humanBytes(m.TotalAlloc), humanBytes(m.Sys))

	// Estimate the lower bound on per-fire local work if CoreParseTranscript
	// is run as-is: 4× GetClaims + 4× GetEdges + 2× Analyze + counts.
	fmt.Println("\nNote: CoreParseTranscript calls GetClaimsByProject 4× and")
	fmt.Println("GetEdgesByProject 4× per fire on top of the API call.")
	fmt.Println("Multiply the above per-call timings to estimate the floor.")
}

func timedT[T any](label string, f func() (T, error)) (T, error) {
	t0 := time.Now()
	v, err := f()
	dur := time.Since(t0)
	if err != nil {
		fmt.Printf("  %-44s  ERR %v\n", label, err)
		return v, err
	}
	fmt.Printf("  %-44s  %s\n", label, dur)
	return v, nil
}

func timedRet(label string, f func()) time.Duration {
	t0 := time.Now()
	f()
	dur := time.Since(t0)
	fmt.Printf("  %-44s  %s\n", label, dur)
	return dur
}

func printProfile(p analysis.Profile) {
	type kv struct {
		k string
		v time.Duration
	}
	rows := make([]kv, 0, len(p))
	var total time.Duration
	for k, v := range p {
		rows = append(rows, kv{k, v})
		total += v
	}
	// Sort by duration descending so the cost hogs surface first.
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].v > rows[i].v {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	for _, r := range rows {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(r.v) / float64(total)
		}
		fmt.Printf("    %-30s  %10s  (%4.1f%%)\n", r.k, r.v, pct)
	}
	fmt.Printf("    %-30s  %10s\n", "total", total)
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// Suppress unused-import lint when strings ends up unreferenced in a refactor.
var _ = strings.Builder{}
