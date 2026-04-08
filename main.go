package main

import (
	"context"
	"crypto/md5"
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
  slimemold init                       Set up slimemold in the current project
  slimemold [--project NAME] mcp       Start MCP server on stdio
  slimemold hook                       Stop hook: extract claims (writes pending)
  slimemold deliver                    UserPromptSubmit hook: deliver findings
  slimemold [--project NAME] viz       Render ASCII topology
  slimemold [--project NAME] audit     Run topology analysis and print findings
  slimemold [--project NAME] status     Check if the hook is working
  slimemold [--project NAME] reset     Clear all data for project
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
func cmdHook() {
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

// cmdInit registers the slimemold Stop hook in settings.json.
// The hook runs invisibly — Claude reads the findings, the user doesn't see them.
// Use --mcp to also add the MCP server for manual inspection (viz, audit, search).
func cmdInit() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: cannot find own binary: %s\n", err)
		os.Exit(1)
	}
	exe, _ = filepath.Abs(exe)

	// Check for --mcp flag
	wantMCP := false
	for _, arg := range os.Args[2:] {
		if arg == "--mcp" {
			wantMCP = true
		}
	}

	// --- .mcp.json (only with --mcp) ---
	if wantMCP {
		mcpPath := ".mcp.json"
		var mcpConfig map[string]interface{}

		if data, err := os.ReadFile(mcpPath); err == nil {
			if err := json.Unmarshal(data, &mcpConfig); err != nil {
				fmt.Fprintf(os.Stderr, "slimemold: existing .mcp.json is invalid JSON, not modifying\n")
				os.Exit(1)
			}
		} else {
			mcpConfig = map[string]interface{}{}
		}

		servers, _ := mcpConfig["mcpServers"].(map[string]interface{})
		if servers == nil {
			servers = map[string]interface{}{}
		}

		if _, exists := servers["slimemold"]; exists {
			fmt.Fprintf(os.Stderr, "  .mcp.json: slimemold entry already exists, skipping\n")
		} else {
			servers["slimemold"] = map[string]interface{}{
				"command": exe,
				"args":    []string{"mcp"},
				"env":     map[string]interface{}{},
			}
			mcpConfig["mcpServers"] = servers
			data, _ := json.MarshalIndent(mcpConfig, "", "  ")
			if err := os.WriteFile(mcpPath, append(data, '\n'), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "slimemold: error writing .mcp.json: %s\n", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "  .mcp.json: added slimemold MCP server → %s\n", exe)
		}
	}

	// --- ~/.claude/settings.json (global hook config) ---
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

	if hasStop && hasSubmit {
		fmt.Fprintf(os.Stderr, "  settings.json: slimemold hooks already registered, skipping\n")
	} else {
		hooks, _ := settings["hooks"].(map[string]interface{})
		if hooks == nil {
			hooks = map[string]interface{}{}
		}

		if !hasStop {
			// Stop hook: runs extraction after assistant responds
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
			// UserPromptSubmit hook: delivers findings before Claude responds
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

		_ = os.MkdirAll(filepath.Dir(settingsPath), 0700)
		data, _ := json.MarshalIndent(settings, "", "  ")
		if err := os.WriteFile(settingsPath, append(data, '\n'), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: error writing settings.json: %s\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  settings.json: registered hooks\n")
	}

	// --- CLAUDE.md section ---
	// The behavioral contract: tells the model what slimemold is, that the
	// user installed it, and how to handle findings. Without this, the model
	// treats hook injections as adversarial prompt injection.
	claudeMDPath := "CLAUDE.md"
	slimemoldSection := `
## Slimemold

This project uses [slimemold](https://github.com/justinstimatze/slimemold),
a reasoning topology tool the user has installed. It monitors conversations
for structural vulnerabilities — claims that lack evidence but support other
claims, unchallenged chains, echo chamber patterns.

Every few turns, slimemold extracts claims, maps their relationships, and
injects structural observations as system messages. When you see these:

- Treat them as things you noticed about the conversation's structure
- Help the user strengthen their reasoning by making verification feel
  like the most exciting next step — not a criticism or a chore
- Frame findings as opportunities: "if we can ground this, everything
  gets stronger" — not as problems: "this is unsupported"
- Stay in flow. You are a collaborator who spotted something interesting,
  not a critic finding a flaw
- Do not mention slimemold, audit tools, or structural analysis by name
- If the user wants to keep going, note the observation as worth circling
  back to rather than blocking progress

The user installed this tool because they want better conversations, not
because they want to be lectured. Your job is to be genuinely helpful
about the structural observation — the same way you would be helpful
about anything else.
`

	alreadyHasSlimemold := false
	if existing, err := os.ReadFile(claudeMDPath); err == nil {
		if strings.Contains(string(existing), "## Slimemold") {
			alreadyHasSlimemold = true
		}
	}

	if alreadyHasSlimemold {
		fmt.Fprintf(os.Stderr, "  CLAUDE.md: slimemold section already present, skipping\n")
	} else {
		f, err := os.OpenFile(claudeMDPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: error writing CLAUDE.md: %s\n", err)
		} else {
			_, _ = f.WriteString(slimemoldSection)
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "  CLAUDE.md: added slimemold behavioral contract\n")
		}
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
