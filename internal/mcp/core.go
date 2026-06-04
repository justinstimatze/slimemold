package mcp

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

// autoSweepDisabled returns true when the user has opted out of auto-sweep.
// Accepts case-insensitive "off", "false", "0", "no" — a typo like "OFF"
// silently failing-open was an easy regression (the previous strict
// `!= "off"` check would have enabled sweep on "OFF" / "False" / "no").
func autoSweepDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SLIMEMOLD_AUTO_SWEEP"))) {
	case "off", "false", "0", "no":
		return true
	}
	return false
}

// CoreParseTranscript extracts claims from a transcript and runs analysis.
// sessionID is the Claude Code session identifier; pass "" to auto-generate one.
// When non-empty, hook findings are scoped to claims from this session only
// (cross-session claims are excluded from the priority finding selection).
//
// preCountedTurns: if >0, the caller has already scanned the transcript and
// is passing the turn count in. CoreParseTranscript skips its own scan and
// uses this value as audit.LastTurn. The Stop hook in main.go counts turns
// pre-call for new-session detection, so reusing that count saves a second
// full-file scan (~100-200ms on a 26MB transcript). Pass 0 to count internally.
//
// Side effect: at the end of a successful parse, triggers a debounced
// auto-sweep (once per day per project) that archives stale claims matching
// the criteria documented in internal/store/sweep.go. The sweep is a no-op
// inside its debounce window. Set SLIMEMOLD_AUTO_SWEEP=off (or false/0/no)
// to disable; SLIMEMOLD_SWEEP_CAP=N caps the per-fire archive count.
func CoreParseTranscript(ctx context.Context, db *store.DB, extractor *extract.Extractor, project, transcriptPath string, sinceTurn int, sessionID string, preCountedTurns int) (*types.AuditResult, error) {
	// Validate transcript path — restrict to JSONL files in expected locations
	if err := validateTranscriptPath(transcriptPath); err != nil {
		return nil, err
	}

	// Load existing claims for cross-batch edge resolution. The dedup index
	// uses GetClaimsByProjectAll (NOT just non-archived) so a claim that was
	// previously archived by the sweep doesn't get silently re-inserted as a
	// fresh row when the model paraphrases it again. The selector below
	// (selectRelevantClaims) and edge-resolution paths use the non-archived
	// view — they shouldn't reach into archived state for context or edges.
	allClaims, _ := db.GetClaimsByProjectAll(project)
	existingClaims := make([]types.Claim, 0, len(allClaims))
	for _, c := range allClaims {
		if !c.Archived {
			existingClaims = append(existingClaims, c)
		}
	}
	existingEdges, _ := db.GetEdgesByProject(project)

	// Read the transcript chunk ONCE. Same string feeds the LLM, the topical-
	// relevance selector (selectRelevantClaims), and the basis validator
	// (validateResearchBasis below). Previously CoreParseTranscript read the
	// transcript twice — once for context (full-text format) and again inside
	// the extractor (role-prefixed format). For 26MB transcripts that's
	// ~150ms saved per fire. Semantically also correct: validateResearchBasis
	// should check against what the LLM actually saw, not a separate view.
	//
	// IO errors are surfaced — previously swallowed, which conflated "the
	// transcript is empty" with "we failed to read the transcript." An empty
	// chunk is fine (no API call needed) and the function continues to the
	// extract path; a real IO error should fail loudly rather than silently
	// skip extraction.
	chunk, err := extract.ReadTranscriptChunk(transcriptPath, sinceTurn)
	if err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	relevantClaims := selectRelevantClaims(existingClaims, existingEdges, chunk)

	existingRefs := make([]extract.ExistingClaimRef, 0, len(relevantClaims))
	for _, c := range relevantClaims {
		existingRefs = append(existingRefs, extract.ExistingClaimRef{
			ID:    c.ID,
			Text:  c.Text,
			Basis: string(c.Basis),
		})
	}

	var result *types.ExtractionResult
	if strings.TrimSpace(chunk) == "" {
		result = &types.ExtractionResult{}
	} else {
		result, err = extractor.Extract(ctx, chunk, existingRefs)
		if err != nil {
			return nil, fmt.Errorf("extraction: %w", err)
		}
	}

	// Phase 0: Validate basis classifications against transcript text
	// (chunk was already read above for context selection)
	if chunk != "" {
		validateResearchBasis(result.Claims, chunk)
	}

	providedSessionID := sessionID != ""
	if !providedSessionID {
		sessionID = fmt.Sprintf("session-%d", time.Now().Unix())
	}

	// Phase 0.5: Intra-batch dedup — merge near-identical claims before insertion.
	// The model often extracts 2-5 phrasings of the same proposition. Merging them
	// reduces graph noise and prevents hub inflation.
	result.Claims = deduplicateBatch(result.Claims)

	// Pre-compute n-gram index for cross-batch dedup (avoids O(n*m*len) per
	// claim). Built from ALL claims (including archived) so a paraphrased
	// resurrection of an archived claim doesn't get re-inserted as a fresh
	// row — it matches the archived original and (below) unarchives it.
	// Without this, every sweep cycle would re-import the same stale
	// observations as new claims, and the working-set growth the sweep was
	// designed to bound would just repopulate.
	existingIndex := buildNgramIndex(allClaims)

	// Begin transaction for phases 1-3 (all writes are atomic).
	txDB, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = txDB.Rollback() }() // no-op after successful commit

	// Phase 1: Insert all claims, building index → ID map
	indexToID := make(map[int]string)
	newClaims := 0

	for _, ec := range result.Claims {
		// Cross-batch dedup: skip if a similar claim already exists in the graph.
		// Do NOT call RecordSessionClaim for normal (non-archived) dedup
		// matches — old-session claims should stay in their origin session.
		// Adding them here bleeds stale state observations (e.g. "feature X
		// is missing") into the current session's findings after the
		// feature has been implemented.
		//
		// Archived matches are different: the sweep had retired the claim
		// as stale, and the model just paraphrased it again. That's the
		// model saying "I assert this proposition NOW in this session," so:
		//   1. Unarchive in-tx (UnarchiveClaims also bumps last_referenced_at,
		//      so the next debounce cycle doesn't immediately re-archive it).
		//   2. Record session membership — the current session is now
		//      asserting it; hook findings need to see it.
		// Error is logged but not propagated — unarchive failure shouldn't
		// abort the whole parse path; the dedup will just leave the row
		// archived for this turn and the next paraphrase will retry.
		if match := existingIndex.findSimilar(ec.Text, types.Speaker(ec.Speaker)); match != nil {
			if match.Archived {
				if n, err := txDB.UnarchiveClaims(project, []string{match.ID}); err != nil {
					fmt.Fprintf(os.Stderr, "slimemold: failed to unarchive %s for dedup match: %v\n", match.ID, err)
				} else if n == 0 {
					fmt.Fprintf(os.Stderr, "slimemold: unarchive of %s affected 0 rows (race with concurrent archive?)\n", match.ID)
				}
				_ = txDB.RecordSessionClaim(sessionID, match.ID)
			}
			indexToID[ec.Index] = match.ID
			continue
		}

		claim := claimFromExtracted(ec, sessionID, project)

		if err := txDB.CreateClaim(claim); err != nil {
			return nil, fmt.Errorf("creating claim: %w", err)
		}
		_ = txDB.RecordSessionClaim(sessionID, claim.ID)
		indexToID[ec.Index] = claim.ID
		newClaims++
	}

	// Phase 2: Create edges using index resolution (intra-batch) and ID
	// resolution (cross-batch). Shared helper with CoreIngestDocument
	// (see internal/mcp/ingest.go) — keeps both call sites under the
	// gocognit linter budget and ensures edge handling stays consistent
	// across transcript and document ingestion paths.
	newEdges := 0
	for _, ec := range result.Claims {
		newEdges += resolveEdgesForClaim(txDB, ec, indexToID)
	}

	// Phase 3: Prune noisy edges — cap outgoing degree per claim. This is the
	// only inside-transaction load: edges have changed during phase 2, so we
	// need a fresh snapshot to decide what to prune.
	pruneHighDegreeEdges(txDB, project)

	// Commit transaction — phases 1-3 are now atomic.
	if err := txDB.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	// Phase 5 (run before the post-commit load so the closed flag is visible
	// in our snapshot): cross-session auto-close. Any claim in this project
	// that has been contradicted by a newer claim from any session is
	// permanently retired here — extends filterSuperseded's within-session
	// behavior to the whole graph so resolution doesn't have to be
	// re-discovered each session. Idempotent and bounded; safe to run on
	// every parse. Errors are logged but not propagated — auto-close failure
	// shouldn't block the parse_transcript path, and the next parse will retry.
	if _, err := db.CloseSupersededClaims(project); err != nil {
		fmt.Fprintf(os.Stderr, "slimemold: CloseSupersededClaims failed for %s: %v\n", project, err)
	}

	// Auto-sweep stale claims, debounced to once per day per project. Hits
	// the same archive criteria as `slimemold sweep --apply`: closed claims
	// or old+idle+weak-basis+no-deps. Runs after CloseSupersededClaims so
	// any newly-superseded claims are eligible for archival in the same pass.
	// Errors swallowed — sweep is best-effort maintenance and must not block
	// the parse path. Set SLIMEMOLD_AUTO_SWEEP=off (or false/0/no) to disable.
	if !autoSweepDisabled() {
		maxArchive := store.SweepCap()
		if n, overflow, ran, err := db.SweepStaleClaimsDebounced(project, 24*time.Hour, maxArchive); err != nil {
			fmt.Fprintf(os.Stderr, "slimemold: auto-sweep error [%s]: %v\n", project, err)
		} else if ran && n > 0 {
			if overflow > 0 {
				fmt.Fprintf(os.Stderr, "slimemold: auto-swept %d stale claims (cap %d hit, %d pending) [%s]\n", n, maxArchive, overflow, project)
			} else {
				fmt.Fprintf(os.Stderr, "slimemold: auto-swept %d stale claims [%s]\n", n, project)
			}
		}
	}

	// Single post-commit load. Threaded through validateIntegrity, analysis,
	// session-scoping, and audit-record counts. On large graphs (13K+ claims)
	// this saves ~3× duplicate full-table scans per fire.
	claims, _ := db.GetClaimsByProject(project)
	edges, _ := db.GetEdgesByProject(project)

	// Phase 4: Integrity validation (read-only, on committed data).
	integrityWarnings := validateIntegrity(claims, edges)

	topo, vulns := analysis.Analyze(claims, edges, project)

	// Counts are just slice lengths — no extra SQL round-trip.
	totalClaims := len(claims)
	totalEdges := len(edges)

	summary := analysis.FormatAuditSummary(topo, vulns)

	// Session-scoped analysis for hook findings: only surface claims that
	// belong to this session so concurrent sessions don't bleed into each
	// other's findings. SLIMEMOLD_SCOPE=all restores the previous global view.
	findingClaims := claims
	findingTopo := topo
	findingVulns := vulns
	if os.Getenv("SLIMEMOLD_SCOPE") != "all" && providedSessionID {
		if sessionClaims, err := db.GetClaimsBySession(sessionID); err == nil && len(sessionClaims) > 0 {
			sessionIDs := make(map[string]bool, len(sessionClaims))
			for _, c := range sessionClaims {
				sessionIDs[c.ID] = true
			}
			sessionEdges := filterEdgesByClaimSet(edges, sessionIDs)
			// Filter out claims the model has explicitly closed (B-manual) and
			// claims superseded by newer contradicting claims in this session
			// (B-auto: extractor sets contradicts edges when it sees a resolution).
			sessionClaims = filterClosed(sessionClaims)
			sessionClaims = filterSuperseded(sessionClaims, sessionEdges)
			if len(sessionClaims) > 0 {
				sessionEdges = filterEdgesByClaimSet(sessionEdges, claimIDSet(sessionClaims))
				sessionTopo, sessionVulns := analysis.Analyze(sessionClaims, sessionEdges, project)
				findingClaims = sessionClaims
				findingTopo = sessionTopo
				findingVulns = sessionVulns
			}
		}
	}

	// Cooldown filter: skip findings whose (claim, type) fired inside the
	// applicable cooldown window. Differential cooldown applies — persistent-
	// only findings get HookPersistentCooldown, others get HookCooldownWindow.
	// Query with the longest window so all potentially-suppressed fires are
	// visible; FormatHookFindings picks the right threshold per candidate.
	recentFires, _ := db.RecentHookFireTimes(project, time.Now().Add(-analysis.HookPersistentCooldown))
	hookSummary, pickedClaimID, pickedFindingType, _ := analysis.FormatHookFindings(findingTopo, findingVulns, findingClaims, recentFires, newClaims, newEdges, 5)
	if pickedClaimID != "" && pickedFindingType != "" {
		_ = db.LogHookFire(project, pickedClaimID, pickedFindingType)
	}

	// Append integrity warnings to summary
	if len(integrityWarnings) > 0 {
		summary += "\n  INTEGRITY:\n"
		for _, w := range integrityWarnings {
			summary += "    " + w + "\n"
		}
	}

	// Record audit (full summary for history)
	_ = db.CreateAudit(&types.Audit{
		Project:       project,
		SessionID:     sessionID,
		Findings:      summary,
		ClaimCount:    totalClaims,
		EdgeCount:     totalEdges,
		CriticalCount: vulns.CriticalCount,
	})

	lastTurn := preCountedTurns
	if lastTurn <= 0 {
		lastTurn, _ = extract.CountTranscriptTurns(transcriptPath)
	}

	return &types.AuditResult{
		NewClaims:       newClaims,
		NewEdges:        newEdges,
		TotalClaims:     totalClaims,
		TotalEdges:      totalEdges,
		Vulnerabilities: *vulns,
		Summary:         summary,
		HookSummary:     hookSummary,
		LastTurn:        lastTurn,
	}, nil
}

// CoreGetTopology returns the full structural summary.
func CoreGetTopology(ctx context.Context, db *store.DB, project string) (*types.Topology, error) {
	claims, err := db.GetClaimsByProject(project)
	if err != nil {
		return nil, err
	}
	edges, err := db.GetEdgesByProject(project)
	if err != nil {
		return nil, err
	}
	topo, _ := analysis.Analyze(claims, edges, project)
	return topo, nil
}

// CoreGetVulnerabilities returns structural weaknesses.
func CoreGetVulnerabilities(ctx context.Context, db *store.DB, project string) (*types.Vulnerabilities, error) {
	claims, err := db.GetClaimsByProject(project)
	if err != nil {
		return nil, err
	}
	edges, err := db.GetEdgesByProject(project)
	if err != nil {
		return nil, err
	}
	_, vulns := analysis.Analyze(claims, edges, project)
	return vulns, nil
}

// CoreRegisterClaim manually registers a claim with optional edges.
func CoreRegisterClaim(ctx context.Context, db *store.DB, project string, text string, basis types.Basis, confidence float64, source string, dependsOn, supports, contradicts []string) (*types.ClaimResult, error) {
	claim := &types.Claim{
		ID:         uuid.New().String(),
		Text:       text,
		Basis:      basis,
		Confidence: confidence,
		Source:     source,
		SessionID:  "manual",
		Project:    project,
		Speaker:    types.SpeakerUser,
		CreatedAt:  time.Now(),
	}

	txDB, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = txDB.Rollback() }()

	if err := txDB.CreateClaim(claim); err != nil {
		return nil, fmt.Errorf("creating claim: %w", err)
	}

	edgeCount := 0
	for _, dep := range dependsOn {
		if resolveEdgeByText(txDB, project, claim.ID, dep, types.RelDependsOn) {
			edgeCount++
		}
	}
	for _, sup := range supports {
		if resolveEdgeByText(txDB, project, claim.ID, sup, types.RelSupports) {
			edgeCount++
		}
	}
	for _, con := range contradicts {
		if resolveEdgeByText(txDB, project, claim.ID, con, types.RelContradicts) {
			edgeCount++
		}
	}

	if err := txDB.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	count, _ := db.CountClaims(project)

	var warnings []string
	if basis == types.BasisVibes || basis == types.BasisAssumption || basis == types.BasisLLMOutput {
		warnings = append(warnings, fmt.Sprintf("Claim has weak basis (%s) — consider finding a source", basis))
	}
	if edgeCount == 0 {
		warnings = append(warnings, "Orphan claim — no connections to other claims")
	}

	return &types.ClaimResult{
		ClaimID:   claim.ID,
		GraphSize: count,
		Warnings:  warnings,
	}, nil
}

// CoreChallengeClaim marks a claim as interrogated.
func CoreChallengeClaim(ctx context.Context, db *store.DB, claimID, result, notes string) error {
	return db.ChallengeClaim(claimID, result, "", "", notes)
}

// selectRelevantClaims filters existing claims to fit the extraction context window.
// For small graphs (<100 claims), returns everything. For larger graphs, selects
// claims by recency, structural importance, and topical similarity to the transcript.
func selectRelevantClaims(claims []types.Claim, edges []types.Edge, transcriptChunk string) []types.Claim {
	const maxContext = 100

	if len(claims) <= maxContext {
		return claims
	}

	selected := make(map[string]bool)

	// 1. Most recent 50 claims (by creation time — already sorted from GetClaimsByProject)
	recentStart := len(claims) - 50
	if recentStart < 0 {
		recentStart = 0
	}
	for _, c := range claims[recentStart:] {
		selected[c.ID] = true
	}

	// 2. Structural hubs: claims with 3+ dependents (load-bearing nodes)
	dependents := make(map[string]int)
	for _, e := range edges {
		switch e.Relation {
		case types.RelSupports:
			dependents[e.FromID]++
		case types.RelDependsOn:
			dependents[e.ToID]++
		}
	}
	for _, c := range claims {
		if dependents[c.ID] >= 3 && !selected[c.ID] {
			selected[c.ID] = true
		}
	}

	// 3. Topical relevance: claims similar to the current transcript chunk.
	// Use n-gram overlap between the transcript and each claim.
	if transcriptChunk != "" && len(selected) < maxContext {
		chunkGrams := charNgrams(strings.ToLower(transcriptChunk), 4)
		type scored struct {
			id    string
			score float64
		}
		var candidates []scored
		for _, c := range claims {
			if selected[c.ID] {
				continue
			}
			claimGrams := charNgrams(strings.ToLower(c.Text), 4)
			if len(claimGrams) == 0 {
				continue
			}
			intersection := 0
			for ng := range claimGrams {
				if chunkGrams[ng] {
					intersection++
				}
			}
			// Score by raw intersection count — favors claims with more topical
			// overlap regardless of claim length. A long specific claim that shares
			// 20 n-grams is more useful context than a short generic claim sharing 3.
			score := float64(intersection)
			if score >= 3 { // at least 3 shared n-grams (roughly one shared word)
				candidates = append(candidates, scored{c.ID, score})
			}
		}
		// Sort by score descending, take top remaining slots
		slices.SortFunc(candidates, func(a, b scored) int {
			return cmp.Compare(b.score, a.score)
		})
		remaining := maxContext - len(selected)
		for i := 0; i < len(candidates) && i < remaining; i++ {
			selected[candidates[i].id] = true
		}
	}

	// Build filtered result preserving original order
	result := make([]types.Claim, 0, len(selected))
	for _, c := range claims {
		if selected[c.ID] {
			result = append(result, c)
		}
	}
	return result
}

// ngramIndex pre-computes n-gram sets for existing claims to avoid O(n*m*len) on every comparison.
type ngramIndex struct {
	claims []types.Claim
	grams  []map[string]bool
}

func buildNgramIndex(claims []types.Claim) *ngramIndex {
	idx := &ngramIndex{claims: claims, grams: make([]map[string]bool, len(claims))}
	for i := range claims {
		idx.grams[i] = charNgrams(strings.ToLower(claims[i].Text), 4)
	}
	return idx
}

// findSimilar checks if a new claim is similar to any indexed claim using
// pre-computed n-gram sets (threshold 0.5). Only matches same speaker.
func (idx *ngramIndex) findSimilar(text string, speaker types.Speaker) *types.Claim {
	newGrams := charNgrams(strings.ToLower(text), 4)
	if len(newGrams) == 0 {
		return nil
	}
	for i := range idx.claims {
		if idx.claims[i].Speaker != speaker {
			continue
		}
		existing := idx.grams[i]
		if len(existing) == 0 {
			continue
		}
		intersection := 0
		for ng := range newGrams {
			if existing[ng] {
				intersection++
			}
		}
		union := len(newGrams) + len(existing) - intersection
		if union > 0 && float64(intersection)/float64(union) > 0.5 {
			return &idx.claims[i]
		}
	}
	return nil
}

// createEdgeIfNew creates an edge, relying on the UNIQUE index to skip duplicates.
// Returns true only if a new edge was actually inserted.
func createEdgeIfNew(db *store.DB, fromID, toID string, relation types.Relation) bool {
	created, _ := db.CreateEdge(&types.Edge{
		FromID:   fromID,
		ToID:     toID,
		Relation: relation,
		Strength: 1.0,
	})
	return created
}

// claimExists checks if a claim ID exists in the database.
func claimExists(db *store.DB, id string) bool {
	_, err := db.GetClaim(id)
	return err == nil
}

// resolveEdgeByText tries to find an existing claim matching the text and creates an edge.
// Used for manual claim registration where edges are specified by text.
func resolveEdgeByText(db *store.DB, project, fromID, targetText string, relation types.Relation) bool {
	targetText = strings.TrimSpace(targetText)
	if targetText == "" {
		return false
	}

	// Try exact match first
	target, _ := db.FindClaimByText(project, targetText)
	if target == nil {
		// Try substring match
		matches, _ := db.FindClaimBySubstring(project, targetText)
		if len(matches) == 1 {
			target = &matches[0]
		}
	}

	if target == nil || target.ID == fromID {
		return false
	}

	return createEdgeIfNew(db, fromID, target.ID, relation)
}

// deduplicateBatch merges near-identical claims within a single extraction batch.
// The model often extracts 2-5 phrasings of the same proposition. We cluster them
// by character n-gram similarity (Jaccard >0.5 on 4-grams) and keep one representative
// per cluster, remapping all intra-batch edge references to the representative's index.
func deduplicateBatch(claims []types.ExtractedClaim) []types.ExtractedClaim {
	if len(claims) < 2 {
		return claims
	}

	n := len(claims)

	// Build index-to-position mapping for consistent lookups
	indexToPos := make(map[int]int, n)
	for i, c := range claims {
		indexToPos[c.Index] = i
	}

	// Build contradicts set using array positions, not Index values
	contradictsPos := make(map[[2]int]bool)
	for i, c := range claims {
		for _, target := range c.ContradictsIndices {
			if j, ok := indexToPos[target]; ok {
				contradictsPos[[2]int{i, j}] = true
				contradictsPos[[2]int{j, i}] = true
			}
		}
	}

	// Union-find clustering
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	// Cluster by character n-gram similarity >0.5, same speaker only
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if claims[i].Speaker != claims[j].Speaker {
				continue
			}
			if contradictsPos[[2]int{i, j}] {
				continue
			}
			if claimSimilarity(claims[i].Text, claims[j].Text) > 0.5 {
				union(i, j)
			}
		}
	}

	// Group by cluster root
	clusters := make(map[int][]int) // root -> [indices into claims]
	for i := 0; i < n; i++ {
		root := find(i)
		clusters[root] = append(clusters[root], i)
	}

	// For each cluster, pick the representative (longest text = most information)
	// and merge edge references
	indexRemap := make(map[int]int) // old index -> representative index
	var deduped []types.ExtractedClaim

	for _, members := range clusters {
		// Pick representative: longest text
		bestIdx := members[0]
		for _, idx := range members[1:] {
			if len(claims[idx].Text) > len(claims[bestIdx].Text) {
				bestIdx = idx
			}
		}

		rep := claims[bestIdx]

		// Map all member indices to the representative
		for _, idx := range members {
			indexRemap[claims[idx].Index] = rep.Index
		}

		// Merge edges from other members into representative
		for _, idx := range members {
			if idx == bestIdx {
				continue
			}
			other := claims[idx]
			rep.DependsOnIndices = appendUnique(rep.DependsOnIndices, other.DependsOnIndices...)
			rep.SupportsIndices = appendUnique(rep.SupportsIndices, other.SupportsIndices...)
			rep.ContradictsIndices = appendUnique(rep.ContradictsIndices, other.ContradictsIndices...)
			rep.QuestionsIndices = appendUnique(rep.QuestionsIndices, other.QuestionsIndices...)
			rep.DependsOnExisting = appendUniqueStr(rep.DependsOnExisting, other.DependsOnExisting...)
			rep.SupportsExisting = appendUniqueStr(rep.SupportsExisting, other.SupportsExisting...)
			rep.ContradictsExisting = appendUniqueStr(rep.ContradictsExisting, other.ContradictsExisting...)
			rep.QuestionsExisting = appendUniqueStr(rep.QuestionsExisting, other.QuestionsExisting...)
			// Keep stronger basis if available
			if basisRank(other.Basis) < basisRank(rep.Basis) {
				rep.Basis = other.Basis
				rep.Source = other.Source
			}
		}

		deduped = append(deduped, rep)
	}

	// Remap all intra-batch edge indices to point to representatives
	for i := range deduped {
		deduped[i].DependsOnIndices = remapIndices(deduped[i].DependsOnIndices, indexRemap, deduped[i].Index)
		deduped[i].SupportsIndices = remapIndices(deduped[i].SupportsIndices, indexRemap, deduped[i].Index)
		deduped[i].ContradictsIndices = remapIndices(deduped[i].ContradictsIndices, indexRemap, deduped[i].Index)
		deduped[i].QuestionsIndices = remapIndices(deduped[i].QuestionsIndices, indexRemap, deduped[i].Index)
	}

	return deduped
}

// claimSimilarity computes similarity between two claim texts using character
// n-gram overlap. This handles morphological variants (struggling/struggle,
// investing/invest) naturally without a stemmer.
func claimSimilarity(a, b string) float64 {
	const n = 4 // 4-character grams
	a = strings.ToLower(a)
	b = strings.ToLower(b)

	ngramsA := charNgrams(a, n)
	ngramsB := charNgrams(b, n)

	if len(ngramsA) == 0 || len(ngramsB) == 0 {
		return 0
	}

	intersection := 0
	for ng := range ngramsA {
		if ngramsB[ng] {
			intersection++
		}
	}
	union := len(ngramsA) + len(ngramsB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func charNgrams(text string, n int) map[string]bool {
	words := strings.Fields(text)
	set := make(map[string]bool)
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-[]{}…–—")
		runes := []rune(w)
		if len(runes) < n {
			continue
		}
		for i := 0; i <= len(runes)-n; i++ {
			set[string(runes[i:i+n])] = true
		}
	}
	return set
}

// basisRank returns a rank for basis strength (lower = stronger).
// When deduplicating a batch, we keep the claim with the stronger basis.
func basisRank(basis string) int {
	switch basis {
	case "research":
		return 0
	case "empirical":
		return 1
	case "definition":
		return 2
	case "convention":
		return 3
	case "deduction":
		return 4
	case "analogy":
		return 5
	case "llm_output":
		return 6
	case "vibes":
		return 7
	case "assumption":
		return 8
	default:
		return 9
	}
}

func appendUnique(dst []int, vals ...int) []int {
	seen := make(map[int]bool)
	for _, v := range dst {
		seen[v] = true
	}
	for _, v := range vals {
		if !seen[v] {
			dst = append(dst, v)
			seen[v] = true
		}
	}
	return dst
}

func appendUniqueStr(dst []string, vals ...string) []string {
	seen := make(map[string]bool)
	for _, v := range dst {
		seen[v] = true
	}
	for _, v := range vals {
		if !seen[v] {
			dst = append(dst, v)
			seen[v] = true
		}
	}
	return dst
}

func remapIndices(indices []int, remap map[int]int, selfIdx int) []int {
	seen := make(map[int]bool)
	var result []int
	for _, idx := range indices {
		mapped := idx
		if m, ok := remap[idx]; ok {
			mapped = m
		}
		if mapped != selfIdx && !seen[mapped] { // no self-edges
			result = append(result, mapped)
			seen[mapped] = true
		}
	}
	return result
}

// pruneHighDegreeEdges caps the outgoing edge count per claim to reduce noise.
// When a claim has too many edges, it's usually a topical hub rather than a genuine
// argumentative nexus. We keep contradicts edges (most informative), then depends_on,
// then supports (most likely to be topical noise).
func pruneHighDegreeEdges(db *store.DB, project string) {
	const maxOutDegree = 5

	// Edges only — the previous claims fetch was just an early-exit guard.
	// Empty-edge check below covers the same case for ~250ms cheaper on large
	// graphs (one less full-table scan inside the transaction).
	edges, err := db.GetEdgesByProject(project)
	if err != nil || len(edges) == 0 {
		return
	}

	// Group outgoing edges by source claim
	outgoing := make(map[string][]types.Edge)
	for _, e := range edges {
		outgoing[e.FromID] = append(outgoing[e.FromID], e)
	}

	// Priority: contradicts (keep) > questions > depends_on > supports
	// (prune first). Unknown relation types get lowest priority.
	// Questions ranks above depends_on because pushback edges are rare and
	// high-signal — losing a questions edge would hide real epistemic
	// challenge from the productive-stress-test / echo-chamber detectors.
	relPriority := func(r types.Relation) int {
		switch r {
		case types.RelContradicts:
			return 0 // highest priority (keep)
		case types.RelQuestions:
			return 1
		case types.RelDependsOn:
			return 2
		case types.RelSupports:
			return 3
		default:
			return 4 // lowest priority (prune first)
		}
	}

	for _, outs := range outgoing {
		if len(outs) <= maxOutDegree {
			continue
		}

		// Sort: lowest priority number first (keep), highest last (prune).
		// Within same priority, higher strength first (keep).
		slices.SortFunc(outs, func(a, b types.Edge) int {
			if c := cmp.Compare(relPriority(a.Relation), relPriority(b.Relation)); c != 0 {
				return c
			}
			return cmp.Compare(b.Strength, a.Strength) // descending
		})

		// Remove excess edges (from the end — lowest priority)
		for _, e := range outs[maxOutDegree:] {
			_ = db.DeleteEdge(e.ID)
		}
	}
}

// validateTranscriptPath checks that the transcript path is a JSONL file in a
// safe location. Prevents arbitrary file read via the MCP transcript_path parameter.
func validateTranscriptPath(path string) error {
	if path == "" {
		return fmt.Errorf("transcript_path is required")
	}

	// Must be absolute
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid transcript path: %w", err)
	}

	// Must end in .jsonl
	if !strings.HasSuffix(abs, ".jsonl") {
		return fmt.Errorf("transcript must be a .jsonl file: %s", abs)
	}

	// Must exist and be a regular file
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("transcript not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("transcript path is a directory: %s", abs)
	}

	return nil
}

// validateIntegrity checks the graph for structural problems after edge
// creation. Accepts pre-loaded claims/edges so the caller can share its
// single post-commit snapshot — previously this did its own GetClaimsByProject
// and GetEdgesByProject (the 3rd duplicate set of full-table scans per fire).
func validateIntegrity(claims []types.Claim, edges []types.Edge) []string {
	if len(claims) == 0 {
		return nil
	}

	var warnings []string

	// Check for edges pointing to nonexistent claims
	claimIDs := make(map[string]bool)
	for _, c := range claims {
		claimIDs[c.ID] = true
	}
	for _, e := range edges {
		if !claimIDs[e.FromID] {
			warnings = append(warnings, fmt.Sprintf("Broken edge: from_id %s does not exist", e.FromID))
		}
		if !claimIDs[e.ToID] {
			warnings = append(warnings, fmt.Sprintf("Broken edge: to_id %s does not exist", e.ToID))
		}
	}

	// Coverage: what fraction of claims have edges?
	hasEdge := make(map[string]bool)
	for _, e := range edges {
		hasEdge[e.FromID] = true
		hasEdge[e.ToID] = true
	}
	orphanCount := 0
	for _, c := range claims {
		if !hasEdge[c.ID] {
			orphanCount++
		}
	}
	if len(claims) > 3 {
		orphanRate := float64(orphanCount) / float64(len(claims))
		if orphanRate > 0.4 {
			warnings = append(warnings, fmt.Sprintf("High orphan rate: %.0f%% of claims are unconnected — extraction may be under-connecting", orphanRate*100))
		}
	}

	return warnings
}

// validateResearchBasis checks basis classifications against the claim text
// and transcript, downgrading common hallucination patterns:
//
//   - "research" without an in-text citation → llm_output (the extractor
//     sometimes upgrades confident-but-uncited assertions)
//   - "deduction" without logical-step signals ("if", "therefore",
//     "because", "follows from") → vibes
//   - "empirical" without observer/measurement signals ("I/we", "saw",
//     "measured", "observed", "tested") → vibes
//   - "convention" without declared-practice signals ("we use", "this
//     project", "must", "our workflow") → vibes
//
// Each check is conservative — a genuine claim with unusual phrasing can
// get downgraded, but the failure mode of a false downgrade is "claim stays
// load-bearing vibes," which is structurally harmless. The opposite failure
// (hallucinated upgrade persisting) is much worse because it hides real
// load-bearing structure from the detectors.
//
// The name remains validateResearchBasis for historical reasons; it now
// validates several basis types.
func validateResearchBasis(claims []types.ExtractedClaim, transcript string) {
	transcriptLower := strings.ToLower(transcript)

	for i := range claims {
		c := &claims[i]
		claimTextLower := strings.ToLower(c.Text)

		switch c.Basis {
		case "research":
			// Research claims must have a source that appears in the transcript.
			// "Studies show" without a named study is not research.
			if c.Source == "" {
				c.Basis = "llm_output"
				continue
			}
			// Check if the source citation actually appears in transcript
			sourceLower := strings.ToLower(c.Source)
			// Extract a keyword from the source (first significant word)
			sourceWords := strings.Fields(sourceLower)
			found := false
			for _, w := range sourceWords {
				w = strings.Trim(w, ".,;:!?\"'()-[]")
				if len(w) > 3 && strings.Contains(transcriptLower, w) {
					found = true
					break
				}
			}
			if !found {
				// Source citation not grounded in transcript — hallucinated
				c.Basis = "llm_output"
				c.Source = ""
			}

		case "deduction":
			// Deduction claims should contain logical-step signals. Without
			// any of them, the extractor is likely calling two sequential
			// assertions "deduction" — that's vibes.
			if !containsAny(claimTextLower, []string{
				"if ", "therefore", " then ", "because", "follows from",
				"implies", "entails", "consequently", "hence", "thus",
				"given that", "it follows",
			}) {
				c.Basis = "vibes"
			}

		case "empirical":
			// Empirical claims should reference first-person observation
			// or explicit measurement. Absent any marker, the extractor is
			// likely calling a confident factual claim "empirical."
			if !containsAny(claimTextLower, []string{
				"i saw", "i observed", "i measured", "i tested", "i tried",
				"we saw", "we observed", "we measured", "we tested", "we tried",
				"we ran", "i ran", "observed", "measured", "noticed",
				"in our experiment", "in our test", "empirically",
			}) {
				c.Basis = "vibes"
			}

		case "convention":
			// Convention claims should declare a practice/policy by a named
			// actor (project/team/organization/author voice). Absent those
			// signals, the extractor is calling a general factual claim
			// "convention."
			if !containsAny(claimTextLower, []string{
				"we use", "we track", "we follow", "we require",
				"this project", "the project uses", "this team", "our workflow",
				"our convention", "agents should", "agents must",
				"must use", "must be", "required to", "by convention",
				"the convention is", "standard practice",
			}) {
				c.Basis = "vibes"
			}
		}
	}
}

// containsAny returns true if any of the needles appears as a substring of
// haystack (both are expected to be already-lowercased).
func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// filterEdgesByClaimSet returns edges where both endpoints are in the claim ID set.
func filterEdgesByClaimSet(edges []types.Edge, claimIDs map[string]bool) []types.Edge {
	out := edges[:0:0]
	for _, e := range edges {
		if claimIDs[e.FromID] && claimIDs[e.ToID] {
			out = append(out, e)
		}
	}
	return out
}

// claimIDSet builds a set of claim IDs from a slice.
func claimIDSet(claims []types.Claim) map[string]bool {
	set := make(map[string]bool, len(claims))
	for _, c := range claims {
		set[c.ID] = true
	}
	return set
}

// filterClosed removes claims marked as closed from the slice.
// Closed claims are explicitly retired by the model after confirming the
// underlying state assertion is no longer true (e.g. "X is missing" after X
// was implemented). They are excluded from hook findings but kept in the DB
// for provenance.
func filterClosed(claims []types.Claim) []types.Claim {
	out := claims[:0:0]
	for _, c := range claims {
		if !c.Closed {
			out = append(out, c)
		}
	}
	return out
}

// filterSuperseded removes claims that have been contradicted by a newer claim
// in the same set. This is B-auto: when the extractor sees a resolution claim
// ("X has been implemented") and produces a contradicts edge to an older claim
// ("X is missing"), the older claim is automatically retired from findings
// without requiring an explicit close call.
func filterSuperseded(claims []types.Claim, edges []types.Edge) []types.Claim {
	if len(edges) == 0 {
		return claims
	}
	byID := make(map[string]types.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID] = c
	}
	superseded := make(map[string]bool)
	for _, e := range edges {
		if e.Relation != types.RelContradicts {
			continue
		}
		from, fromOK := byID[e.FromID]
		to, toOK := byID[e.ToID]
		if !fromOK || !toOK {
			continue
		}
		if !from.CreatedAt.IsZero() && !to.CreatedAt.IsZero() && from.CreatedAt.After(to.CreatedAt) {
			superseded[to.ID] = true
		}
	}
	if len(superseded) == 0 {
		return claims
	}
	out := claims[:0:0]
	for _, c := range claims {
		if !superseded[c.ID] {
			out = append(out, c)
		}
	}
	return out
}
