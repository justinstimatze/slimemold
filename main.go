package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/config"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/internal/viz"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	// Parse --project flag from anywhere in args
	project, args := extractProjectFlag(os.Args[1:])

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "mcp":
		cmdMCP(project)
	case "viz":
		cmdViz(project)
	case "audit":
		cmdAudit(project)
	case "reset":
		cmdReset(project)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "slimemold: unknown command %q\n", args[0])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `slimemold — reasoning topology mapper

Usage:
  slimemold [--project NAME] mcp       Start MCP server on stdio
  slimemold [--project NAME] viz       Render ASCII topology
  slimemold [--project NAME] audit     Run topology analysis and print findings
  slimemold [--project NAME] reset     Clear all data for project
  slimemold help                       Show this help

Project resolution: --project flag > .slimemold-project file > directory name

Environment:
  ANTHROPIC_API_KEY   Required for claim extraction
  SLIMEMOLD_MODEL     Extraction model (default: claude-sonnet-4-6)
  SLIMEMOLD_DATA_DIR  Data directory (default: ~/.slimemold)
`)
}

// extractProjectFlag pulls --project NAME from args, returning the project name
// (empty if not specified) and remaining args.
func extractProjectFlag(args []string) (string, []string) {
	var project string
	var remaining []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--project" || args[i] == "-p") && i+1 < len(args) {
			project = args[i+1]
			i++ // skip value
		} else if strings.HasPrefix(args[i], "--project=") {
			project = strings.TrimPrefix(args[i], "--project=")
		} else {
			remaining = append(remaining, args[i])
		}
	}
	return project, remaining
}

func resolveProject(override string) string {
	if override != "" {
		return override
	}
	return detectProject()
}

// resolveDBProject returns the project name used for DB directory (always CWD-based),
// and the project name used for row filtering (override or CWD-based).
// The MCP server stores all projects in one DB (keyed by CWD), so --project
// only changes which rows are queried, not which DB file is opened.
func resolveDBProject(override string) (dbProject, queryProject string) {
	dbProject = detectProject()
	queryProject = dbProject
	if override != "" {
		queryProject = override
	}
	return
}

func cmdMCP(projectOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}

	project := resolveProject(projectOverride)
	db, err := store.Open(cfg.DataDir, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var extractor *extract.Extractor
	if cfg.AnthropicAPIKey != "" {
		extractor = extract.New(cfg.AnthropicAPIKey, cfg.Model)
		extractor.KnowledgeMode = cfg.KnowledgeMode
	}

	if err := mcp.RunMCP(db, extractor, project); err != nil {
		db.Close() // ensure WAL checkpoint before exit
		fmt.Fprintf(os.Stderr, "slimemold: mcp error: %s\n", err)
		os.Exit(1)
	}
}

func cmdViz(projectOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}

	dbProject, queryProject := resolveDBProject(projectOverride)
	db, err := store.Open(cfg.DataDir, dbProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	claims, err := db.GetClaimsByProject(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading claims: %s\n", err)
		os.Exit(1)
	}
	edges, err := db.GetEdgesByProject(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading edges: %s\n", err)
		os.Exit(1)
	}
	topo, vulns := analysis.Analyze(claims, edges, queryProject)
	fmt.Print(viz.RenderASCII(topo, vulns))
}

func cmdAudit(projectOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}

	dbProject, queryProject := resolveDBProject(projectOverride)
	db, err := store.Open(cfg.DataDir, dbProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	claims, err := db.GetClaimsByProject(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading claims: %s\n", err)
		os.Exit(1)
	}
	edges, err := db.GetEdgesByProject(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading edges: %s\n", err)
		os.Exit(1)
	}
	topo, vulns := analysis.Analyze(claims, edges, queryProject)
	summary := analysis.FormatAuditSummary(topo, vulns)
	fmt.Print(summary)
}

func cmdReset(projectOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}

	dbProject, queryProject := resolveDBProject(projectOverride)
	db, err := store.Open(cfg.DataDir, dbProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.DeleteProject(queryProject); err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: reset error: %s\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "slimemold: reset project %q\n", queryProject)
}

func detectProject() string {
	// Check .slimemold-project in cwd
	if data, err := os.ReadFile(".slimemold-project"); err == nil {
		name := strings.TrimSpace(string(data))
		if name != "" {
			return name
		}
	}

	// Fall back to directory name
	cwd, err := os.Getwd()
	if err != nil {
		return "default"
	}
	parts := strings.Split(cwd, string(os.PathSeparator))
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "default"
}
