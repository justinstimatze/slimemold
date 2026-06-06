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

	"io"

	"github.com/justinstimatze/slimemold/internal/adapt"
	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/config"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/hookevents"
	"github.com/justinstimatze/slimemold/internal/mcp"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/internal/viz"
	"github.com/justinstimatze/slimemold/types"
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
	case "calibrate":
		cmdCalibrate(project)
	case "status":
		cmdStatus(project)
	case "reset":
		cmdReset(project)
	case "ingest":
		cmdIngest(project, args[1:])
	case "eval":
		cmdEval()
	case "analyze-winze":
		cmdAnalyzeWinze(args[1:])
	case "sweep":
		cmdSweep(project, args[1:])
	case "unarchive":
		cmdUnarchive(project, args[1:])
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
  slimemold [--project NAME] calibrate Per-session inventory-flag rates and saturation threshold sweep (Moore et al. 2026)
  slimemold [--project NAME] status     Check if the hook is working
  slimemold [--project NAME] reset     Clear all data for project
  slimemold [--project NAME] ingest PATH   Ingest a document (text or markdown) into the graph
  slimemold eval                       Run the demo corpus through extraction and print a basis-distribution snapshot
  slimemold analyze-winze [FILE]       Run slimemold detectors on a winze KB export (stdin if FILE is "-" or omitted)
  slimemold [--project NAME] sweep [--apply [--no-cap]]
                                       List claims that would be archived (dry-run by default; --apply to actually archive).
                                       --apply honors SLIMEMOLD_SWEEP_CAP; --no-cap forces a single uncapped pass.
  slimemold [--project NAME] unarchive [--all [--confirm] | CLAIM_ID...]
                                       Reverse archival: --all needs --confirm (shows count first); per-ID is unconfirmed.
  slimemold help                       Show this help

Project resolution: --project flag > .slimemold-project file > directory name

Environment:
  ANTHROPIC_API_KEY    Required for claim extraction
  SLIMEMOLD_MODEL      Extraction model (default: claude-sonnet-4-6)
  SLIMEMOLD_DATA_DIR   Data directory (default: ~/.slimemold)
  SLIMEMOLD_INTERVAL   Hook fires every N turns (default: 3)
  SLIMEMOLD_AUTO_SWEEP Set to "off" to disable the daily auto-archive of stale claims
  SLIMEMOLD_SWEEP_CAP  Max claims to archive per fire (default: 1000; set 0 or negative for no cap)
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
		CWD       string `json:"cwd"`
		SessionID string `json:"session_id"`
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

	// Key the pending file on session_id when available so concurrent sessions
	// in the same project don't deliver each other's findings. Claude Code
	// 2.1.x exposes the session ID as CLAUDE_CODE_SESSION_ID in the hook
	// subprocess environment — prefer that over the JSON field so we don't
	// depend on a payload shape that may shift across CC versions. JSON is
	// kept as fallback for older runtimes.
	pendingKey := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if pendingKey == "" {
		pendingKey = input.SessionID
	}
	if pendingKey == "" {
		pendingKey = project
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(pendingKey)))[:8]
	logDir := filepath.Join(cfg.DataDir, "tmp")
	pendingFile := filepath.Join(logDir, "pending-"+hash+".txt")

	// Skip stale pending files — a session that ended more than 12h ago
	// can't ground the finding it was going to deliver, so don't.
	if info, err := os.Stat(pendingFile); err != nil || time.Since(info.ModTime()) > 12*time.Hour {
		return
	}
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
	start := time.Now()

	// First config.Load (pre-Chdir): gives us logDir for the panic-recover
	// emit. config.Load reads env vars + global .env in ~/.config/slimemold
	// — these don't depend on CWD. The SECOND Load below (post-Chdir)
	// picks up any project-local .env.
	cfg, _ := config.Load()
	var logDir string
	if cfg != nil {
		logDir = filepath.Join(cfg.DataDir, "tmp")
		_ = os.MkdirAll(logDir, 0700)
	}

	emitter := &hookevents.Emitter{LogDir: logDir, Start: start}

	defer func() {
		r := recover()
		if r == nil {
			return
		}
		reason := hookevents.TruncateReason(r)
		// Best-effort write to hook.log too (free-form prose for human
		// tail-f); structured event goes via the emitter.
		if logDir != "" {
			if f, err := os.OpenFile(filepath.Join(logDir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				fmt.Fprintf(f, "%s PANIC in hook: %s\n", time.Now().Format("2006-01-02 15:04:05"), reason)
				_ = f.Close()
			}
			emitter.Panic(reason)
		} else {
			// Emergency: cfg.Load failed at entry so logDir is unknown.
			// Surface the panic to stderr so something records it (and
			// fall through — strict-additive discipline preserved by the
			// outer recover; we just can't write to the events file).
			fmt.Fprintf(os.Stderr, "slimemold: hook panic (no logDir): %s\n", reason)
		}
	}()

	if cfg == nil {
		fmt.Fprintln(os.Stderr, "slimemold: config.Load failed; skipping hook fire (no observability)")
		return
	}

	var input struct {
		CWD            string `json:"cwd"`
		TranscriptPath string `json:"transcript_path"`
		SessionID      string `json:"session_id"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.CWD == "" || input.TranscriptPath == "" {
		// Structured noinput so `mlr stats1 -g kind` accounts for every
		// fire AND so downstream queries can ask "how often did decode
		// fail vs how often was a field empty?" without regex-parsing a
		// Sprintf'd reason.
		emitter.Noinput(input.CWD, input.TranscriptPath, err)
		return
	}

	// Chdir to the project so config.Load can pick up project-local .env.
	// Errors swallowed — same discipline as the previous code; if Chdir
	// fails the post-Chdir config will fall back to env-only.
	_ = os.Chdir(input.CWD)

	// Second config.Load (post-Chdir): picks up project-local .env.
	// loadDotenv() only sets env vars that aren't already set, so global
	// .env / shell-env still win — only NEW keys from the project .env
	// take effect. Effect: cfg now reflects what the OLD pre-hoist code
	// saw before the recover-defer hoist forced this reorder.
	if cfg2, err := config.Load(); err == nil {
		cfg = cfg2
	}

	cleanStaleLocks(logDir)
	logPath := filepath.Join(logDir, "hook.log")
	hookevents.RotateLogIfLarge(logPath)
	logFile, _ := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
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
	emitter.Project = project // backfill so subsequent emits (including a
	// late panic) attribute to the right project.

	emit := emitter.Emit

	// Resolve session ID: prefer CLAUDE_CODE_SESSION_ID env (Claude Code
	// 2.1.x+ broadcasts it to hook subprocesses); JSON field is the
	// fallback for older runtimes. sessionID is the canonical identifier
	// stored on extracted claims (passed through CoreParseTranscript);
	// sessionKey is what we hash for per-session state files and falls
	// back further to project so single-session-per-project usage still
	// gets a stable key even when neither env nor JSON provide one.
	sessionID := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if sessionID == "" {
		sessionID = input.SessionID
	}
	sessionKey := sessionID
	if sessionKey == "" {
		sessionKey = project
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(sessionKey)))[:8]
	pendingFile := filepath.Join(logDir, "pending-"+hash+".txt")

	if cfg.AnthropicAPIKey == "" {
		logf("no ANTHROPIC_API_KEY — check .env in %s", input.CWD)
		emit("nokey", nil)
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
		emit("skip", map[string]any{"turn": count, "interval": cfg.HookInterval})
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
						emit("locked", map[string]any{"holder_pid": pid})
						return
					}
				}
			}
			// Stale lock — remove and retry once
			os.Remove(lockFile)
			f, err = os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				logf("skipping: could not acquire lock after stale removal")
				emit("locked", map[string]any{"phase": "retry_failed", "reason": err.Error()})
				return
			}
		} else {
			logf("skipping: lock file exists but unreadable")
			emit("locked", map[string]any{"phase": "unreadable"})
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
		emit("error", map[string]any{"phase": "db_open", "reason": err.Error()})
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

	// Single transcript scan: counts turns for new-session detection AND
	// passes the count through to CoreParseTranscript so it doesn't rescan
	// the file. Saves ~100-200ms per fire on multi-MB transcripts. We always
	// count (not only when sinceTurn>0) so CoreParseTranscript gets the hint
	// regardless of whether we're in baseline or incremental mode.
	turnCount, _ := extract.CountTranscriptTurns(input.TranscriptPath)
	if sinceTurn > 0 && turnCount > 0 && sinceTurn >= turnCount {
		logf("new session detected (sinceTurn=%d >= actualTurns=%d), resetting", sinceTurn, turnCount)
		sinceTurn = 0
	}

	logf("extracting [%s] from %s (since turn %d)", project, filepath.Base(input.TranscriptPath), sinceTurn)

	audit, err := mcp.CoreParseTranscript(ctx, db, extractor, project, input.TranscriptPath, sinceTurn, sessionID, turnCount)
	if err != nil {
		logf("extraction error: %s", err)
		emit("error", map[string]any{"phase": "extract", "model": cfg.Model, "reason": err.Error()})
		return
	}

	logf("done: %d claims, %d edges (+%d new)", audit.TotalClaims, audit.TotalEdges, audit.NewClaims)
	emit("extract", map[string]any{
		"model":      cfg.Model,
		"claims":     audit.TotalClaims,
		"edges":      audit.TotalEdges,
		"new_claims": audit.NewClaims,
		"findings":   audit.Vulnerabilities.CriticalCount + audit.Vulnerabilities.WarningCount,
		"since_turn": sinceTurn,
		"turn":       turnCount,
	})

	// Persist last turn for incremental extraction on next run
	if audit.LastTurn > 0 {
		_ = os.WriteFile(lastTurnFile, []byte(strconv.Itoa(audit.LastTurn)), 0600)
	}

	// Write status (project-scoped so `slimemold status` can find it regardless of session).
	projectHash := fmt.Sprintf("%x", md5.Sum([]byte(project)))[:8]
	statusJSON, _ := json.Marshal(map[string]interface{}{
		"project": project, "timestamp": time.Now().Format(time.RFC3339),
		"claims": audit.TotalClaims, "edges": audit.TotalEdges,
		"new_claims": audit.NewClaims,
		"findings":   audit.Vulnerabilities.CriticalCount + audit.Vulnerabilities.WarningCount,
	})
	_ = os.WriteFile(filepath.Join(logDir, "status-"+projectHash+".json"), statusJSON, 0600)

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

	fmt.Fprintf(os.Stderr, "\nDone. Hooks are active immediately in new Claude Code sessions.\n")
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

// cmdCalibrate prints a per-session report of Moore et al. 2026 inventory-flag
// activity. Lets the user tune sycophancy_saturation and amplification_cascade
// thresholds against their own data instead of trusting the (uncalibrated)
// codebase defaults.
func cmdCalibrate(projectOverride string) {
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

	report := analysis.Calibrate(queryProject, claims, edges)
	fmt.Print(analysis.FormatCalibrationReport(report))
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
	eventsFile := filepath.Join(cfg.DataDir, "tmp", hookevents.EventsFilename)

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

	// Show last few structured events. Uses TailLines (bounded 32KB read
	// from end) instead of os.ReadFile (full 5MB load), and renders
	// missing duration_ms as "-" instead of silently coalescing to "0ms".
	fmt.Fprint(os.Stderr, hookevents.FormatRecentForStatus(eventsFile, 5))

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

// cmdAnalyzeWinze runs slimemold detectors on a winze KB JSON export.
// Input is a WinzeClaimRecord[] array or a {"claims":[...],"provenance":[...]} object.
// Reads from FILE if given, or stdin if "-" or no arg.
//
// Typical workflow:
//
//	go run ./cmd/query --json --disputes .  | slimemold analyze-winze -
//	go run ./cmd/query --json --theories consciousness . | slimemold analyze-winze -
func cmdAnalyzeWinze(args []string) {
	var data []byte
	var err error

	src := "-"
	if len(args) > 0 {
		src = args[0]
	}

	if src == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold analyze-winze: %s\n", err)
		os.Exit(1)
	}

	export, err := adapt.ParseWinzeInput(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold analyze-winze: %s\n", err)
		os.Exit(1)
	}
	if len(export.Claims) == 0 {
		fmt.Fprintln(os.Stderr, "slimemold analyze-winze: no claims found in input")
		os.Exit(1)
	}

	claims, edges := adapt.AdaptWinzeExport(export)
	for i := range claims {
		claims[i].Project = "winze"
	}

	topo, vulns := analysis.Analyze(claims, edges, "winze")
	fmt.Printf("Winze KB: %d claims, %d edges\n\n", topo.ClaimCount, topo.EdgeCount)
	fmt.Print(analysis.FormatAuditSummary(topo, vulns))
}

// emitHookEvent + rotateLogIfLarge moved to internal/hookevents/ —
// callers use hookevents.Emitter.{Emit,Panic,Noinput} +
// hookevents.RotateLogIfLarge. The package owns the flock-based
// rotation (no .rotating sidecar to orphan on SIGKILL), the bounded
// TruncateReason helper, the TailLines + FormatRecentForStatus pair
// used by cmdStatus, and the test suite that pins the emitter contract.

// sweepBuckets is the per-category breakdown shown in the sweep CLI report.
// Built from the candidate IDs returned by store.SweepCandidates — the
// criteria themselves live in the store package so CoreParseTranscript can
// share them without an import cycle.
type sweepBuckets struct {
	count              int
	closedCount        int
	weakAndNoDepsCount int
	byBasis            map[string]int
	bySpeaker          map[string]int
}

// buildSweepBreakdown bins candidate IDs into a per-category report and
// returns the set as a map for callers (the sample-rendering loop) to reuse.
// deps is the incoming-structural-edges map produced by
// store.SweepCandidatesWithDeps — passed in to avoid recomputing the same
// O(E) pass already done inside the criteria function. Returning idSet
// avoids an O(N*K) `contains(ids, c.ID)` lookup in the sample loop on
// lucida-class graphs (15k claims, 5k candidates) where the inner loop
// would dominate the report cost.
func buildSweepBreakdown(ids []string, claims []types.Claim, deps map[string]int) (sweepBuckets, map[string]bool) {
	b := sweepBuckets{byBasis: map[string]int{}, bySpeaker: map[string]int{}}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	for _, c := range claims {
		if !idSet[c.ID] {
			continue
		}
		b.count++
		b.byBasis[string(c.Basis)]++
		b.bySpeaker[string(c.Speaker)]++
		if c.Closed {
			b.closedCount++
		} else if deps[c.ID] < store.SweepStructuralMin && store.SweepWeakBasis[string(c.Basis)] {
			b.weakAndNoDepsCount++
		}
	}
	return b, idSet
}

// cmdSweep reports (default) or applies (with --apply) the stale-claim
// sweep. Criteria documented at sweepCandidates above. --apply is the only
// destructive action in slimemold; the dry-run path is purely diagnostic.
//
// Reversal: every archived claim can be restored via `slimemold unarchive`.
// Archival sets a flag rather than deleting rows, so no data is actually lost.
//
// --apply honors SLIMEMOLD_SWEEP_CAP for parity with the auto-sweep path —
// previously the env var only applied inside the MCP daemon, so a manual
// `sweep --apply` on a lucida-class backlog (5k+ candidates) ignored the
// user's "max N per batch" preference and archived everything in one shot.
// `--no-cap` overrides the env to force a single uncapped pass (useful when
// the user has explicitly inspected the dry-run report and knows what they
// want). Set the env var to 0 or negative for "no cap" without the flag.
func cmdSweep(projectOverride string, args []string) {
	apply := false
	noCap := false
	for _, a := range args {
		switch a {
		case "--apply":
			apply = true
		case "--no-cap":
			noCap = true
		}
	}
	// --no-cap is only meaningful with --apply (dry-run reports all
	// candidates regardless of cap). Warn rather than silently ignore so
	// the user notices the typo / wrong invocation order.
	if noCap && !apply {
		fmt.Fprintf(os.Stderr, "slimemold: --no-cap has no effect without --apply (dry-run always reports all candidates); ignoring.\n")
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: config error: %s\n", err)
		os.Exit(1)
	}
	// DB path is always CWD-keyed; --project only changes which rows we
	// query/mutate inside it. resolveDBProject preserves that invariant —
	// the previous form (queryProject = projectOverride or detectProject)
	// would open a SECOND db under the override name, splitting state.
	dbProject, queryProject := resolveDBProject(projectOverride)
	db, err := store.Open(cfg.DataDir, dbProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: database error: %s\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Use the *all-including-archived* view so report counts reflect the
	// actual DB state — re-running the sweep should show already-archived
	// claims as archived, not invisible. sweepCandidates filters internally.
	claims, err := db.GetClaimsByProjectAll(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading claims: %s\n", err)
		os.Exit(1)
	}
	edges, err := db.GetEdgesByProject(queryProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: error loading edges: %s\n", err)
		os.Exit(1)
	}

	ids, deps := store.SweepCandidatesWithDeps(claims, edges)
	b, idSet := buildSweepBreakdown(ids, claims, deps)
	archivedAlready := 0
	for _, c := range claims {
		if c.Archived {
			archivedAlready++
		}
	}

	mode := "dry-run"
	if apply {
		mode = "APPLY"
	}
	fmt.Printf("=== Stale-claim sweep (%s): project=%q ===\n\n", mode, queryProject)
	fmt.Printf("Total claims: %d (%d already archived, %d active)\n", len(claims), archivedAlready, len(claims)-archivedAlready)
	fmt.Printf("Total edges:  %d\n\n", len(edges))
	fmt.Printf("New archive candidates: %d (%.1f%% of active)\n",
		b.count, percent(b.count, len(claims)-archivedAlready))
	fmt.Printf("  closed=true:                  %d\n", b.closedCount)
	fmt.Printf("  weak basis + no deps:         %d\n", b.weakAndNoDepsCount)
	fmt.Println("  by basis:")
	for k, v := range b.byBasis {
		fmt.Printf("    %-12s %d\n", k, v)
	}
	fmt.Println("  by speaker:")
	for k, v := range b.bySpeaker {
		fmt.Printf("    %-12s %d\n", k, v)
	}

	// Sample candidates so the user can eyeball them. Reuses idSet from the
	// breakdown to avoid an O(N*K) contains() scan per claim on large graphs.
	if b.count > 0 {
		fmt.Println("\nSample candidates:")
		now := time.Now()
		shown := 0
		for _, c := range claims {
			if c.Archived || shown >= 8 {
				continue
			}
			if !idSet[c.ID] {
				continue
			}
			age := int(now.Sub(c.CreatedAt).Hours() / 24)
			idle := int(now.Sub(c.LastReferencedAt).Hours() / 24)
			fmt.Printf("  - [%dd old, %dd idle, deps=%d, basis=%s%s] %s\n",
				age, idle, deps[c.ID], c.Basis, closedTag(c.Closed),
				truncateForSweep(c.Text, 80))
			shown++
		}
	}

	if !apply {
		fmt.Println("\n(No data was modified. Add --apply to archive these claims.)")
		return
	}
	if b.count == 0 {
		fmt.Println("\nNothing to archive.")
		return
	}

	// Apply the cap (SLIMEMOLD_SWEEP_CAP or --no-cap override). Oldest
	// first — the same ordering policy the auto-sweep uses, so manual and
	// auto runs converge on the same set of archived claims given the same
	// state. Print BOTH the cap-affected count and the overflow so the user
	// knows how many fires it'll take to drain the backlog.
	toArchive := ids
	overflow := 0
	capValue := 0
	if !noCap {
		capValue = store.SweepCap()
		if capValue >= 1 && len(toArchive) > capValue {
			store.SortCandidatesOldestFirst(toArchive, claims)
			overflow = len(toArchive) - capValue
			toArchive = toArchive[:capValue]
		}
	}
	if overflow > 0 {
		// cap=%d uses the captured capValue (not len(toArchive)) so the
		// label can't desync if future logic archives fewer than cap due
		// to a different filter.
		fmt.Printf("\nArchiving %d claims (cap=%d hit, %d pending — re-run to drain, or pass --no-cap)...\n",
			len(toArchive), capValue, overflow)
	} else {
		fmt.Printf("\nArchiving %d claims...\n", len(toArchive))
	}
	n, err := db.ArchiveClaims(queryProject, toArchive)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nslimemold: archive error: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Archived %d claims. Restore with: slimemold --project %s unarchive --all --confirm\n", n, queryProject)
}

// cmdUnarchive flips archived=0 on claims, either by ID or all-at-once.
// The recovery hatch for false-positive sweep archives.
//
// --all is asymmetric to cmdSweep --apply: sweep defaults to dry-run and
// requires explicit --apply, whereas unarchive --all mutates immediately
// once entered. To avoid a misclick on a lucida-class graph (~5k archived
// claims that the legacy_load_bearer detector would then flood), --all
// shows the count and requires --confirm in the same invocation. Per-ID
// unarchive doesn't need this guard — the user typed the IDs.
func cmdUnarchive(projectOverride string, args []string) {
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

	all := false
	confirm := false
	var ids []string
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case a == "--confirm":
			confirm = true
		case !strings.HasPrefix(a, "--"):
			ids = append(ids, a)
		}
	}
	if !all && len(ids) == 0 {
		fmt.Fprintf(os.Stderr, "usage: slimemold [--project NAME] unarchive --all [--confirm] | CLAIM_ID...\n")
		os.Exit(1)
	}
	// --all + per-ID args is ambiguous: did the user mean "everything plus
	// these" (which is just "everything") or "wait I'll just unarchive
	// these"? Reject rather than silently doing one and dropping the other.
	if all && len(ids) > 0 {
		fmt.Fprintf(os.Stderr, "slimemold: --all and explicit CLAIM_IDs are mutually exclusive; pick one.\n")
		os.Exit(1)
	}

	// For --all paths (both dry-run and --confirm), short-circuit on zero
	// archived rows so neither path prints a misleading "Unarchived 0
	// claims" success line. Per-ID paths don't need this — UnarchiveClaims
	// already returns 0 on misses and the message is accurate.
	if all {
		everything, _ := db.GetClaimsByProjectAll(queryProject)
		archivedCount := 0
		for _, c := range everything {
			if c.Archived {
				archivedCount++
			}
		}
		if archivedCount == 0 {
			fmt.Printf("No archived claims in project %q. Nothing to do.\n", queryProject)
			return
		}
		if !confirm {
			fmt.Printf("Would unarchive %d claims in project %q.\n", archivedCount, queryProject)
			fmt.Println("This re-activates them in hook findings, viz, audit, and analysis.")
			fmt.Println("Add --confirm to actually unarchive.")
			return
		}
	}

	var n int64
	if all {
		n, err = db.UnarchiveAll(queryProject)
	} else {
		n, err = db.UnarchiveClaims(queryProject, ids)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: unarchive error: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Unarchived %d claims in project %q.\n", n, queryProject)
}

func closedTag(closed bool) string {
	if closed {
		return " CLOSED"
	}
	return ""
}

func truncateForSweep(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func percent(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return 100.0 * float64(num) / float64(denom)
}

// cleanStaleLocks removes hook lock files whose owner PIDs are no longer alive.
// Called at cmdHook startup so sessions killed mid-extraction don't leave locks
// behind permanently (normal stale detection only fires on lock contention).
func cleanStaleLocks(logDir string) {
	matches, _ := filepath.Glob(filepath.Join(logDir, "hook-*.lock"))
	for _, lf := range matches {
		data, err := os.ReadFile(lf)
		if err != nil {
			_ = os.Remove(lf)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			_ = os.Remove(lf)
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			_ = os.Remove(lf)
			continue
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			_ = os.Remove(lf) // process not alive
		}
	}
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
