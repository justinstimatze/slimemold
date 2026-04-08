package viz

import (
	"fmt"
	"strings"

	"github.com/justinstimatze/slimemold/types"
)

// RenderASCII produces a terminal-friendly topology visualization.
func RenderASCII(topo *types.Topology, vulns *types.Vulnerabilities) string {
	var b strings.Builder

	fmt.Fprintf(&b, "SLIMEMOLD TOPOLOGY — %s\n", topo.Project)
	fmt.Fprintf(&b, "%s\n\n", strings.Repeat("═", 50))

	if topo.ClaimCount == 0 {
		fmt.Fprintf(&b, "  (empty graph — no claims registered)\n")
		return b.String()
	}

	fmt.Fprintf(&b, "  %d claims, %d edges, max depth %d\n\n", topo.ClaimCount, topo.EdgeCount, topo.MaxDepth)

	// Basis distribution bar
	fmt.Fprintf(&b, "  Basis distribution:\n")
	for _, basis := range []types.Basis{types.BasisResearch, types.BasisEmpirical, types.BasisDeduction, types.BasisAnalogy, types.BasisVibes, types.BasisLLMOutput, types.BasisAssumption, types.BasisDefinition} {
		count := topo.BasisCounts[basis]
		if count == 0 {
			continue
		}
		bar := strings.Repeat("█", count)
		marker := " "
		if basis == types.BasisVibes || basis == types.BasisLLMOutput || basis == types.BasisAssumption {
			marker = "!"
		}
		fmt.Fprintf(&b, "    %s%-11s %s %d\n", marker, basis, bar, count)
	}
	fmt.Fprintln(&b)

	// Build vulnerability lookup
	vulnMap := make(map[string][]types.Vulnerability)
	for _, v := range vulns.Items {
		for _, id := range v.ClaimIDs {
			vulnMap[id] = append(vulnMap[id], v)
		}
	}

	// Clusters
	if len(topo.Clusters) > 0 {
		fmt.Fprintf(&b, "  Clusters:\n")
		for _, cl := range topo.Clusters {
			fmt.Fprintf(&b, "  ┌─ %s (%d claims, density %.2f) %s┐\n",
				cl.Label, len(cl.Claims), cl.Density, strings.Repeat("─", max(1, 40-len(cl.Label))))

			for _, c := range cl.Claims {
				icon := basisIcon(c.Basis)
				suffix := ""
				if vs, ok := vulnMap[c.ID]; ok {
					for _, v := range vs {
						switch v.Type {
						case "load_bearing_vibes":
							suffix += " ← LOAD-BEARING " + strings.ToUpper(string(c.Basis))
						case "bottleneck":
							suffix += " ← BOTTLENECK"
						}
					}
				}
				src := ""
				if c.Source != "" {
					src = " — " + truncate(c.Source, 30)
				}
				fmt.Fprintf(&b, "  │  %s %s [%s]%s%s\n", icon, truncate(c.Text, 50), c.Basis, src, suffix)
			}

			fmt.Fprintf(&b, "  └%s┘\n", strings.Repeat("─", 55))
		}
		fmt.Fprintln(&b)
	}

	// Orphans
	if len(topo.Orphans) > 0 {
		fmt.Fprintf(&b, "  Orphans (%d unconnected):\n", len(topo.Orphans))
		for _, o := range topo.Orphans {
			fmt.Fprintf(&b, "    ? %s [%s]\n", truncate(o.Text, 60), o.Basis)
		}
		fmt.Fprintln(&b)
	}

	// Vulnerabilities summary
	if vulns.CriticalCount > 0 || vulns.WarningCount > 0 {
		fmt.Fprintf(&b, "  Findings: %d critical, %d warning, %d info\n", vulns.CriticalCount, vulns.WarningCount, vulns.InfoCount)
		for _, v := range vulns.Items {
			if v.Severity == "critical" {
				fmt.Fprintf(&b, "    ✗ CRITICAL: %s\n", v.Description)
			}
		}
		for _, v := range vulns.Items {
			if v.Severity == "warning" {
				fmt.Fprintf(&b, "    ⚠ WARNING: %s\n", v.Description)
			}
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "  ● = research/empirical  ○ = vibes/assumption  △ = analogy  ? = orphan\n")

	return b.String()
}

// RenderDOT produces Graphviz DOT format.
func RenderDOT(claims []types.Claim, edges []types.Edge, project string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "digraph %q {\n", project)
	fmt.Fprintf(&b, "  rankdir=BT;\n")
	fmt.Fprintf(&b, "  node [shape=box, style=rounded, fontsize=10];\n\n")

	for _, c := range claims {
		color := basisColor(c.Basis)
		label := strings.ReplaceAll(truncate(c.Text, 40), `"`, `\"`)
		fmt.Fprintf(&b, "  %q [label=%q, fillcolor=%q, style=\"rounded,filled\"];\n",
			c.ID, fmt.Sprintf("%s\\n[%s]", label, c.Basis), color)
	}

	fmt.Fprintln(&b)

	for _, e := range edges {
		style := "solid"
		if e.Relation == types.RelContradicts {
			style = "dashed"
		}
		fmt.Fprintf(&b, "  %q -> %q [label=%q, style=%s];\n",
			e.FromID, e.ToID, e.Relation, style)
	}

	fmt.Fprintf(&b, "}\n")
	return b.String()
}

func basisIcon(b types.Basis) string {
	switch b {
	case types.BasisResearch, types.BasisEmpirical:
		return "●"
	case types.BasisAnalogy:
		return "△"
	case types.BasisVibes, types.BasisLLMOutput, types.BasisAssumption:
		return "○"
	case types.BasisDeduction:
		return "◆"
	default:
		return "·"
	}
}

func basisColor(b types.Basis) string {
	switch b {
	case types.BasisResearch, types.BasisEmpirical:
		return "#d4edda"
	case types.BasisAnalogy:
		return "#fff3cd"
	case types.BasisVibes, types.BasisLLMOutput, types.BasisAssumption:
		return "#f8d7da"
	case types.BasisDeduction:
		return "#d1ecf1"
	default:
		return "#e2e3e5"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
