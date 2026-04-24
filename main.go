package main

import (
	"context"
	"crypto/md5"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/config"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/internal/viz"
)

// evalCorpus embeds the demo documents into the binary so `slimemold eval`
// works regardless of the caller's CWD (installed binaries, etc.). Writing
// out to a temp directory at eval-time costs ~200KB of binary size and a
// few ms per run.
//
//go:embed examples/documents/marinetti-futurist-manifesto-1909.md examples/documents/sokal-social-text-1996.md
var evalCorpus embed.FS

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
	case "hook":
		cmdHook()
	case "deliver":
		cmdDeliver()
	case "init":
		cmdInit()
	case "viz":
		cmdViz(project)
	case "audit":
		cmdAudit(project)
	case "status":
		cmdStatus(project)
	case "reset":
		cmdReset(project)
	case "ingest":
		cmdIngest(project, args[1:])
	case "eval":
		cmdEval()
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
  slimemold init                       Register hooks and MCP globally in ~/.claude/settings.json
  slimemold [--project NAME] mcp       Start MCP server on stdio
  slimemold hook                       Stop hook: extract claims (writes pending)
  slimemold deliver                    UserPromptSubmit hook: deliver findings
  slimemold [--project NAME] viz       Render ASCII topology
  slimemold [--project NAME] audit     Run topology analysis and print findings
  slimemold [--project NAME] status     Check if the hook is working
  slimemold [--project NAME] reset     Clear all data for project
  slimemold [--project NAME] ingest PATH   Ingest a document (text or markdown) into the graph
  slimemold eval                       Run the demo corpus through extraction and print a basis-distribution snapshot
  slimemold help                       Show this help

Project resolution: --project flag > .slimemold-project file > directory name

Environment:
  ANTHROPIC_API_KEY    Required for claim extraction
  SLIMEMOLD_MODEL      Extraction model (default: claude-sonnet-4-6)
  SLIMEMOLD_DATA_DIR   Data directory (default: ~/.slimemold)
  SLIMEMOLD_INTERVAL   Hook fires every N turns (default: 3)
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

// cmdDeliver runs as a UserPromptSubmit hook. Reads pending findings from a
// previous extraction and outputs them as additionalContext that Claude reads.
// Returns instantly — no API calls, no DB access.
func cmdDeliver() {
	var input struct {
		CWD string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.CWD == "" {
		return
	}

	_ = os.Chdir(input.CWD)

	cfg, err := config.Load()
	if err != nil {
		return
	}

	project := filepath.Base(input.CWD)
	if data, err := os.ReadFile(filepath.Join(input.CWD, ".slimemold-project")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			project = name
		}
	}

	hash := fmt.Sprintf("%x", md5.Sum([]byte(project)))[:8]
	logDir := filepath.Join(cfg.DataDir, "tmp")
	pendingFile := filepath.Join(logDir, "pending-"+hash+".txt")

	if data, err := os.ReadFile(pendingFile); err == nil && len(data) > 0 {
		// Keep delivering until the next extraction replaces the file.
		// This provides constant corrective pressure between extractions
		// instead of one hard redirect followed by silence.
		fmt.Print(string(data))
	}
}

// cmdHook runs as a Stop hook. Fires after assistant responds, runs extraction,
// writes findings to a pending file for cmdDeliver to pick up.
//
// Discipline: **strictly additive.** The hook is allowed to produce findings
// (additive) or produce nothing (silent) — it is NEVER allowed to interfere
// with the conversation. All error paths fall through to silent-exit; a
// top-level recover guarantees a rogue panic can't propagate. The Stop hook
// returning non-zero to Claude Code is visible to the user; we'd rather
// silently skip an extraction than leak a stack trace.
func cmdHook() {
	defer func() {
		if r := recover(); r != nil {
			// Best effort: log the panic if we can, then swallow.
			cfg, err := config.Load()
			if err == nil {
				logDir := filepath.Join(cfg.DataDir, "tmp")
				_ = os.MkdirAll(logDir, 0700)
				if f, err := os.OpenFile(filepath.Join(logDir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
					fmt.Fprintf(f, "%s PANIC in hook: %v\n", time.Now().Format("2006-01-02 15:04:05"), r)
					_ = f.Close()
				}
			}
		}
	}()
	var input struct {
		CWD            string `json:"cwd"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.CWD == "" || input.TranscriptPath == "" {
		return
	}

	_ = os.Chdir(input.CWD)

	cfg, err := config.Load()
	if err != nil {
		return
	}

	logDir := filepath.Join(cfg.DataDir, "tmp")
	_ = os.MkdirAll(logDir, 0700)
	logFile, _ := os.OpenFile(filepath.Join(logDir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	logf := func(format string, args ...interface{}) {
		if logFile != nil {
			fmt.Fprintf(logFile, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
		}
	}
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	project := filepath.Base(input.CWD)
	if data, err := os.ReadFile(filepath.Join(input.CWD, ".slimemold-project")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			project = name
		}
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(project)))[:8]
	pendingFile := filepath.Join(logDir, "pending-"+hash+".txt")

	if cfg.AnthropicAPIKey == "" {
		logf("no ANTHROPIC_API_KEY — check .env in %s", input.CWD)
		return
	}

	if input.TranscriptPath == "" {
		return
	}

	// Rate limit
	counterFile := filepath.Join(logDir, "turns-"+hash+".txt")
	count := 1
	if data, err := os.ReadFile(counterFile); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			count = n + 1
		}
	}
	_ = os.WriteFile(counterFile, []byte(strconv.Itoa(count)), 0600)

	// Fire on turn 1 (establish baseline), then every N turns after
	if count != 1 && count%cfg.HookInterval != 0 {
		return
	}

	// Atomic lock file with stale PID detection
	lockFile := filepath.Join(logDir, "hook-"+hash+".lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		// Lock exists — check if holder is still alive
		if data, err := os.ReadFile(lockFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					if err := process.Signal(syscall.Signal(0)); err == nil {
						logf("skipping: concurrent extraction (pid %d)", pid)
						return
					}
				}
			}
			// Stale lock — remove and retry once
			os.Remove(lockFile)
			f, err = os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				logf("skipping: could not acquire lock after stale removal")
				return
			}
		} else {
			logf("skipping: lock file exists but unreadable")
			return
		}
	}
	fmt.Fprintf(f, "%d", os.Getpid())
	f.Close()
	defer os.Remove(lockFile)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	db, err := store.Open(cfg.DataDir, project)
	if err != nil {
		logf("db error: %s", err)
		return
	}
	defer db.Close()

	extractor := extract.New(cfg.AnthropicAPIKey, cfg.Model)
	extractor.KnowledgeMode = cfg.KnowledgeMode

	// Incremental extraction: only process turns added since last run
	lastTurnFile := filepath.Join(logDir, "lastTurn-"+hash+".txt")
	sinceTurn := 0
	if data, err := os.ReadFile(lastTurnFile); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			sinceTurn = n
		}
	}

	// Detect new session: transcript has fewer turns than our bookmark
	if sinceTurn > 0 {
		if actualTurns, err := extract.CountTranscriptTurns(input.TranscriptPath); err == nil && sinceTurn >= actualTurns {
			logf("new session detected (sinceTurn=%d >= actualTurns=%d), resetting", sinceTurn, actualTurns)
			sinceTurn = 0
		}
	}

	logf("extracting [%s] from %s (since turn %d)", project, filepath.Base(input.TranscriptPath), sinceTurn)

	audit, err := mcp.CoreParseTranscript(ctx, db, extractor, project, input.TranscriptPath, sinceTurn)
	if err != nil {
		logf("extraction error: %s", err)
		return
	}

	logf("done: %d claims, %d edges (+%d new)", audit.TotalClaims, audit.TotalEdges, audit.NewClaims)

	// Persist last turn for incremental extraction on next run
	if audit.LastTurn > 0 {
		_ = os.WriteFile(lastTurnFile, []byte(strconv.Itoa(audit.LastTurn)), 0600)
	}

	// Write status
	statusJSON, _ := json.Marshal(map[string]interface{}{
		"project": project, "timestamp": time.Now().Format(time.RFC3339),
		"claims": audit.TotalClaims, "edges": audit.TotalEdges,
		"new_claims": audit.NewClaims,
		"findings":   audit.Vulnerabilities.CriticalCount + audit.Vulnerabilities.WarningCount,
	})
	_ = os.WriteFile(filepath.Join(logDir, "status-"+hash+".json"), statusJSON, 0600)

	// Write findings to pending file — delivered by the UserPromptSubmit hook
	if audit.HookSummary != "" {
		_ = os.WriteFile(pendingFile, []byte(audit.HookSummary), 0600)
		logf("wrote pending findings for [%s]", project)
	}
}

// cmdInit registers slimemold globally in ~/.claude/settings.json: the Stop
// and UserPromptSubmit hooks, plus the MCP server. Everything is global so
// slimemold runs in every project without per-project setup. The MCP server
// carries the behavioral contract via WithInstructions.
func cmdInit() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: cannot find own binary: %s\n", err)
		os.Exit(1)
	}
	exe, _ = filepath.Abs(exe)

	// --- ~/.claude/settings.json (global hook + MCP config) ---
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: cannot find home directory: %s\n", err)
		os.Exit(1)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	var settings map[string]interface{}

	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: existing settings.json is invalid JSON, not modifying\n")
			os.Exit(1)
		}
	} else {
		settings = map[string]interface{}{}
	}

	extractCmd := exe + " hook"    // Stop: runs extraction
	deliverCmd := exe + " deliver" // UserPromptSubmit: delivers findings

	// Check which hooks are already registered
	hasStop := hookRegistered(settings, "Stop")
	hasSubmit := hookRegistered(settings, "UserPromptSubmit")

	mcpServers, _ := settings["mcpServers"].(map[string]interface{})
	if mcpServers == nil {
		mcpServers = map[string]interface{}{}
	}
	_, hasMCP := mcpServers["slimemold"]

	dirty := false

	if hasStop && hasSubmit {
		fmt.Fprintf(os.Stderr, "  settings.json: slimemold hooks already registered, skipping\n")
	} else {
		hooks, _ := settings["hooks"].(map[string]interface{})
		if hooks == nil {
			hooks = map[string]interface{}{}
		}

		if !hasStop {
			stopHooks, _ := hooks["Stop"].([]interface{})
			stopHooks = append(stopHooks, map[string]interface{}{
				"matcher": "",
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": extractCmd},
				},
			})
			hooks["Stop"] = stopHooks
			fmt.Fprintf(os.Stderr, "    Stop → %s\n", extractCmd)
		}

		if !hasSubmit {
			submitHooks, _ := hooks["UserPromptSubmit"].([]interface{})
			submitHooks = append(submitHooks, map[string]interface{}{
				"matcher": "",
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": deliverCmd},
				},
			})
			hooks["UserPromptSubmit"] = submitHooks
			fmt.Fprintf(os.Stderr, "    UserPromptSubmit → %s\n", deliverCmd)
		}

		settings["hooks"] = hooks
		dirty = true
	}

	if hasMCP {
		fmt.Fprintf(os.Stderr, "  settings.json: slimemold MCP already registered, skipping\n")
	} else {
		mcpServers["slimemold"] = map[string]interface{}{
			"command": exe,
			"args":    []string{"mcp"},
			"env":     map[string]interface{}{},
		}
		settings["mcpServers"] = mcpServers
		fmt.Fprintf(os.Stderr, "    MCP slimemold → %s mcp\n", exe)
		dirty = true
	}

	if dirty {
		_ = os.MkdirAll(filepath.Dir(settingsPath), 0700)
		data, _ := json.MarshalIndent(settings, "", "  ")
		if err := os.WriteFile(settingsPath, append(data, '\n'), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: error writing settings.json: %s\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  settings.json: written\n")
	}

	// --- API key check ---
	// The hook runs in the project directory, so the API key must be
	// discoverable from there: shell env, project .env, or ~/.config/slimemold/.env
	cfg, _ := config.Load()
	if cfg == nil || cfg.AnthropicAPIKey == "" {
		home, _ := os.UserHomeDir()
		globalEnv := filepath.Join(home, ".config", "slimemold", ".env")
		fmt.Fprintf(os.Stderr, "\n  ⚠ ANTHROPIC_API_KEY not found. The hook will silently skip extraction.\n")
		fmt.Fprintf(os.Stderr, "    Set it in one of these locations:\n")
		fmt.Fprintf(os.Stderr, "    1. Shell environment: export ANTHROPIC_API_KEY=sk-ant-...\n")
		fmt.Fprintf(os.Stderr, "    2. Global config:     echo 'ANTHROPIC_API_KEY=sk-ant-...' >> %s\n", globalEnv)
		fmt.Fprintf(os.Stderr, "    3. Project .env:      echo 'ANTHROPIC_API_KEY=sk-ant-...' >> .env\n")
	}

	fmt.Fprintf(os.Stderr, "\nDone. Restart Claude Code or run /mcp to connect.\n")
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

func cmdStatus(projectOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}

	project := resolveProject(projectOverride)
	hash := fmt.Sprintf("%x", md5.Sum([]byte(project)))[:8]
	statusFile := filepath.Join(cfg.DataDir, "tmp", "status-"+hash+".json")
	logFile := filepath.Join(cfg.DataDir, "tmp", "hook.log")

	fmt.Fprintf(os.Stderr, "Project: %s\n", project)

	// Read last run status
	if data, err := os.ReadFile(statusFile); err == nil {
		var status map[string]interface{}
		if json.Unmarshal(data, &status) == nil {
			ts, _ := status["timestamp"].(string)
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				ago := time.Since(t).Round(time.Second)
				fmt.Fprintf(os.Stderr, "Last hook: %s ago\n", ago)
			}
			fmt.Fprintf(os.Stderr, "Claims: %.0f  Edges: %.0f  New: %.0f  Findings: %.0f\n",
				status["claims"], status["edges"], status["new_claims"], status["findings"])
		}
	} else {
		fmt.Fprintf(os.Stderr, "Last hook: never (no status file)\n")
	}

	// Show last few log lines
	if data, err := os.ReadFile(logFile); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		start := len(lines) - 5
		if start < 0 {
			start = 0
		}
		fmt.Fprintf(os.Stderr, "\nRecent log:\n")
		for _, line := range lines[start:] {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
	}

	// Check API key
	if cfg.AnthropicAPIKey == "" {
		fmt.Fprintf(os.Stderr, "\n⚠ ANTHROPIC_API_KEY not set\n")
	}
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

func cmdIngest(projectOverride string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "slimemold ingest: missing PATH")
		fmt.Fprintln(os.Stderr, "Usage: slimemold [--project NAME] ingest PATH")
		os.Exit(1)
	}
	path := args[0]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}
	if cfg.AnthropicAPIKey == "" {
		fmt.Fprintln(os.Stderr, "slimemold: ANTHROPIC_API_KEY required for ingestion")
		os.Exit(1)
	}

	dbProject, queryProject := resolveDBProject(projectOverride)
	db, err := store.Open(cfg.DataDir, dbProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	extractor := extract.New(cfg.AnthropicAPIKey, cfg.Model)
	fmt.Fprintf(os.Stderr, "slimemold: ingesting %s into project %q\n", path, queryProject)

	result, err := mcp.CoreIngestDocument(context.Background(), db, extractor, queryProject, path, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: ingest error: %s\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "\nslimemold: %d new claims, %d new edges (total: %d claims, %d edges)\n",
		result.NewClaims, result.NewEdges, result.TotalClaims, result.TotalEdges)
	fmt.Println(result.Summary)
}

// cmdEval runs the demo corpus through extraction and prints a basis-
// distribution snapshot as JSON. Purpose: regression-detect prompt changes.
// The workflow is:
//
//  1. Run `slimemold eval` before changing the extraction prompt — capture
//     the output as a baseline (e.g. `slimemold eval > baseline.json`).
//  2. Change the prompt, bump documentPromptVersion.
//  3. Run `slimemold eval` again — diff against the baseline.
//
// Drift in basis distribution (especially in vibes/convention/research
// ratios) is a signal the prompt change altered classification behavior.
// No pass/fail is emitted — it's left to the caller to decide what drift
// is acceptable; this command is the measurement, not the policy.
//
// Cost: first run after a prompt change re-extracts all chunks (~$0.30 at
// current Sonnet pricing). Subsequent runs hit the cache and complete in
// seconds. Uses project name "slimemold-eval" to isolate from real data.
func cmdEval() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}
	if cfg.AnthropicAPIKey == "" {
		fmt.Fprintln(os.Stderr, "slimemold: ANTHROPIC_API_KEY required for eval")
		os.Exit(1)
	}

	// Embedded demo filenames (inside the evalCorpus go:embed FS).
	embeddedPaths := []string{
		"examples/documents/marinetti-futurist-manifesto-1909.md",
		"examples/documents/sokal-social-text-1996.md",
	}

	// Extract embeds into a persistent well-known location so the content
	// hash is stable across invocations (extraction cache hits). Under
	// cfg.DataDir so it lives alongside the eval DB.
	corpusDir := filepath.Join(cfg.DataDir, "eval-corpus")
	if err := os.MkdirAll(corpusDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: eval corpus dir: %s\n", err)
		os.Exit(1)
	}
	var demos []string
	for _, rel := range embeddedPaths {
		data, err := evalCorpus.ReadFile(rel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: missing embedded corpus file %s: %s\n", rel, err)
			os.Exit(1)
		}
		out := filepath.Join(corpusDir, filepath.Base(rel))
		if err := os.WriteFile(out, data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: writing eval corpus file: %s\n", err)
			os.Exit(1)
		}
		demos = append(demos, out)
	}

	// Hardcode BOTH the DB project directory AND the query project name to
	// "slimemold-eval" so the eval DB lives at a fixed location regardless
	// of the caller's CWD. Previous behavior used resolveDBProject("") which
	// was CWD-derived — that meant cache entries didn't persist across
	// invocations from different directories.
	const evalProject = "slimemold-eval"
	db, err := store.Open(cfg.DataDir, evalProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Fresh eval project each run so numbers are reproducible. Cache
	// entries in the eval DB's extract_cache survive DeleteProject.
	_ = db.DeleteProject(evalProject)

	extractor := extract.New(cfg.AnthropicAPIKey, cfg.Model)

	type perDoc struct {
		Path        string         `json:"path"`
		Claims      int            `json:"claims"`
		Edges       int            `json:"edges"`
		BasisCounts map[string]int `json:"basis_counts"`
	}
	type evalReport struct {
		Model         string   `json:"model"`
		PromptVersion int      `json:"prompt_version"`
		Docs          []perDoc `json:"docs"`
	}

	report := evalReport{Model: cfg.Model, PromptVersion: mcp.DocumentPromptVersion()}

	for _, docPath := range demos {
		fmt.Fprintf(os.Stderr, "slimemold eval: ingesting %s\n", docPath)
		if _, err := mcp.CoreIngestDocument(context.Background(), db, extractor, evalProject, docPath, 0); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: eval ingest error on %s: %s\n", docPath, err)
			os.Exit(1)
		}
	}

	claims, _ := db.GetClaimsByProject(evalProject)
	edges, _ := db.GetEdgesByProject(evalProject)

	// Per-doc breakdown keyed on session_id, which CoreIngestDocument
	// sets deterministically from the absolute document path via
	// DocumentSessionID. Source on individual claims is LLM-provided and
	// not reliable as a filename — using the session_id correlation
	// avoids that.
	basisByDoc := make(map[string]map[string]int)
	claimCountByDoc := make(map[string]int)
	claimToSession := make(map[string]string, len(claims))
	for _, c := range claims {
		sid := c.SessionID
		if basisByDoc[sid] == nil {
			basisByDoc[sid] = make(map[string]int)
		}
		basisByDoc[sid][string(c.Basis)]++
		claimCountByDoc[sid]++
		claimToSession[c.ID] = sid
	}
	edgesByDoc := make(map[string]int)
	for _, e := range edges {
		if sid, ok := claimToSession[e.FromID]; ok {
			edgesByDoc[sid]++
		}
	}

	// Stable ordering by demos[] order so the JSON diffs cleanly.
	for _, docPath := range demos {
		abs, err := filepath.Abs(docPath)
		if err != nil {
			continue
		}
		sid := mcp.DocumentSessionID(abs)
		report.Docs = append(report.Docs, perDoc{
			Path:        docPath,
			Claims:      claimCountByDoc[sid],
			Edges:       edgesByDoc[sid],
			BasisCounts: basisByDoc[sid],
		})
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: eval marshal error: %s\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// hookRegistered checks if a slimemold hook is registered for the given event type.
func hookRegistered(settings map[string]interface{}, eventType string) bool {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	arr, ok := hooks[eventType].([]interface{})
	if !ok {
		return false
	}
	for _, h := range arr {
		hm, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		hooksArr, ok := hm["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, inner := range hooksArr {
			im, ok := inner.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, ok := im["command"].(string); ok && strings.Contains(cmd, "slimemold") {
				return true
			}
		}
	}
	return false
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
