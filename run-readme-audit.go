//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
)

func main() {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set")
		os.Exit(1)
	}
	model := os.Getenv("SLIMEMOLD_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	dir, _ := os.MkdirTemp("", "readme-audit-*")
	defer os.RemoveAll(dir)
	db, _ := store.Open(dir, "readme-selfcheck")
	defer db.Close()
	ext := extract.New(apiKey, model)
	result, err := mcp.CoreParseTranscript(context.Background(), db, ext, "readme-selfcheck", "/tmp/readme-audit.jsonl", 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "extraction error:", err)
		os.Exit(1)
	}
	claims, _ := db.GetClaimsByProject("readme-selfcheck")
	edges, _ := db.GetEdgesByProject("readme-selfcheck")
	topo, vulns := analysis.Analyze(claims, edges, "readme-selfcheck")
	fmt.Println(analysis.FormatAuditSummary(topo, vulns))
	fmt.Printf("New claims: %d, New edges: %d\n", result.NewClaims, result.NewEdges)
}
