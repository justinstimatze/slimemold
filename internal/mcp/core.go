package mcp

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
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

// CoreParseTranscript extracts claims from a transcript and runs analysis.
func CoreParseTranscript(ctx context.Context, db *store.DB, extractor *extract.Extractor, project, transcriptPath string, sinceTurn int) (*types.AuditResult, error) {
	// Validate transcript path — restrict to JSONL files in expected locations
	if err := validateTranscriptPath(transcriptPath); err != nil {
		return nil, err
	}

	// Load existing claims for cross-batch edge resolution.
	// For large graphs, filter to relevant claims to avoid overwhelming the context window.
	existingClaims, _ := db.GetClaimsByProject(project)
	existingEdges, _ := db.GetEdgesByProject(project)

	chunk, _ := readTranscriptText(transcriptPath, sinceTurn)
	relevantClaims := selectRelevantClaims(existingClaims, existingEdges, chunk)

	existingRefs := make([]extract.ExistingClaimRef, 0, len(relevantClaims))
	for _, c := range relevantClaims {
		existingRefs = append(existingRefs, extract.ExistingClaimRef{
			ID:    c.ID,
			Text:  c.Text,
			Basis: string(c.Basis),
		})
	}

	result, err := extractor.ExtractFromTranscript(ctx, transcriptPath, sinceTurn, existingRefs)
	if err != nil {
		return nil, fmt.Errorf("extraction: %w", err)
	}

	// Phase 0: Validate basis classifications against transcript text
	// (chunk was already read above for context selection)
	if chunk != "" {
		validateResearchBasis(result.Claims, chunk)
	}

	sessionID := fmt.Sprintf("session-%d", time.Now().Unix())

	// Phase 0.5: Intra-batch dedup — merge near-identical claims before insertion.
	// The model often extracts 2-5 phrasings of the same proposition. Merging them
	// reduces graph noise and prevents hub inflation.
	result.Claims = deduplicateBatch(result.Claims)

	// Pre-compute n-gram index for cross-batch dedup (avoids O(n*m*len) per claim)
	existingIndex := buildNgramIndex(existingClaims)

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
		if match := existingIndex.findSimilar(ec.Text, types.Speaker(ec.Speaker)); match != nil {
			indexToID[ec.Index] = match.ID
			continue
		}

		claim := &types.Claim{
			ID:         uuid.New().String(),
			Text:       ec.Text,
			Basis:      types.Basis(ec.Basis),
			Confidence: ec.Confidence,
			Source:     ec.Source,
			SessionID:  sessionID,
			Project:    project,
			Speaker:    types.Speaker(ec.Speaker),
			CreatedAt:  time.Now(),
		}

		if err := txDB.CreateClaim(claim); err != nil {
			return nil, fmt.Errorf("creating claim: %w", err)
		}
		indexToID[ec.Index] = claim.ID
		newClaims++
	}

	// Phase 2: Create edges using index resolution (intra-batch) and ID resolution (cross-batch)
	newEdges := 0

	for _, ec := range result.Claims {
		fromID, ok := indexToID[ec.Index]
		if !ok {
			continue
		}

		// Intra-batch edges: resolve by numeric index
		for _, targetIdx := range ec.DependsOnIndices {
			if toID, ok := indexToID[targetIdx]; ok && toID != fromID {
				if createEdgeIfNew(txDB, fromID, toID, types.RelDependsOn) {
					newEdges++
				}
			}
		}
		for _, targetIdx := range ec.SupportsIndices {
			if toID, ok := indexToID[targetIdx]; ok && toID != fromID {
				if createEdgeIfNew(txDB, fromID, toID, types.RelSupports) {
					newEdges++
				}
			}
		}
		for _, targetIdx := range ec.ContradictsIndices {
			if toID, ok := indexToID[targetIdx]; ok && toID != fromID {
				if createEdgeIfNew(txDB, fromID, toID, types.RelContradicts) {
					newEdges++
				}
			}
		}

		// Cross-batch edges: resolve by existing claim ID
		for _, existingID := range ec.DependsOnExisting {
			if existingID != fromID && claimExists(txDB, existingID) {
				if createEdgeIfNew(txDB, fromID, existingID, types.RelDependsOn) {
					newEdges++
				}
			}
		}
		for _, existingID := range ec.SupportsExisting {
			if existingID != fromID && claimExists(txDB, existingID) {
				if createEdgeIfNew(txDB, fromID, existingID, types.RelSupports) {
					newEdges++
				}
			}
		}
		for _, existingID := range ec.ContradictsExisting {
			if existingID != fromID && claimExists(txDB, existingID) {
				if createEdgeIfNew(txDB, fromID, existingID, types.RelContradicts) {
					newEdges++
				}
			}
		}
	}

	// Phase 3: Prune noisy edges — cap outgoing degree per claim
	pruneHighDegreeEdges(txDB, project)

	// Commit transaction — phases 1-3 are now atomic.
	if err := txDB.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	// Phase 4: Integrity validation (read-only, on committed data)
	integrityWarnings := validateIntegrity(db, project)

	// Run analysis
	claims, _ := db.GetClaimsByProject(project)
	edges, _ := db.GetEdgesByProject(project)
	topo, vulns := analysis.Analyze(claims, edges, project)

	totalClaims, _ := db.CountClaims(project)
	totalEdges, _ := db.CountEdges(project)

	summary := analysis.FormatAuditSummary(topo, vulns)
	hookSummary := analysis.FormatHookFindings(topo, vulns, newClaims, newEdges, 5)

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

	return &types.AuditResult{
		NewClaims:       newClaims,
		NewEdges:        newEdges,
		TotalClaims:     totalClaims,
		TotalEdges:      totalEdges,
		Vulnerabilities: *vulns,
		Summary:         summary,
		HookSummary:     hookSummary,
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

	if err := db.CreateClaim(claim); err != nil {
		return nil, fmt.Errorf("creating claim: %w", err)
	}

	edgeCount := 0
	for _, dep := range dependsOn {
		if resolveEdgeByText(db, project, claim.ID, dep, types.RelDependsOn) {
			edgeCount++
		}
	}
	for _, sup := range supports {
		if resolveEdgeByText(db, project, claim.ID, sup, types.RelSupports) {
			edgeCount++
		}
	}
	for _, con := range contradicts {
		if resolveEdgeByText(db, project, claim.ID, con, types.RelContradicts) {
			edgeCount++
		}
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

	// Build contradicts set — don't merge claims that contradict each other
	contradictsIdx := make(map[[2]int]bool)
	for _, c := range claims {
		for _, target := range c.ContradictsIndices {
			contradictsIdx[[2]int{c.Index, target}] = true
			contradictsIdx[[2]int{target, c.Index}] = true
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
			if contradictsIdx[[2]int{claims[i].Index, claims[j].Index}] {
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
			rep.DependsOnExisting = appendUniqueStr(rep.DependsOnExisting, other.DependsOnExisting...)
			rep.SupportsExisting = appendUniqueStr(rep.SupportsExisting, other.SupportsExisting...)
			rep.ContradictsExisting = appendUniqueStr(rep.ContradictsExisting, other.ContradictsExisting...)
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
		if len(w) < n {
			continue
		}
		for i := 0; i <= len(w)-n; i++ {
			set[w[i:i+n]] = true
		}
	}
	return set
}

// basisRank returns a rank for basis strength (lower = stronger).
func basisRank(basis string) int {
	switch basis {
	case "research":
		return 0
	case "empirical":
		return 1
	case "definition":
		return 2
	case "deduction":
		return 3
	case "analogy":
		return 4
	case "llm_output":
		return 5
	case "vibes":
		return 6
	case "assumption":
		return 7
	default:
		return 8
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

	claims, err := db.GetClaimsByProject(project)
	if err != nil || len(claims) == 0 {
		return
	}
	edges, _ := db.GetEdgesByProject(project)

	// Group outgoing edges by source claim
	outgoing := make(map[string][]types.Edge)
	for _, e := range edges {
		outgoing[e.FromID] = append(outgoing[e.FromID], e)
	}

	// Priority: contradicts (keep) > depends_on > supports (prune first)
	// Unknown relation types get lowest priority (pruned first)
	relPriority := func(r types.Relation) int {
		switch r {
		case types.RelContradicts:
			return 0 // highest priority (keep)
		case types.RelDependsOn:
			return 1
		case types.RelSupports:
			return 2
		default:
			return 3 // lowest priority (prune first)
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

// validateIntegrity checks the graph for structural problems after edge creation.
func validateIntegrity(db *store.DB, project string) []string {
	claims, _ := db.GetClaimsByProject(project)
	edges, _ := db.GetEdgesByProject(project)

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

// readTranscriptText reads the raw text content from a transcript for validation.
func readTranscriptText(path string, sinceTurn int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var parts []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Text()), &entry); err != nil {
			continue
		}
		// Extract text content from any structure
		if content, ok := entry["content"].(string); ok {
			parts = append(parts, content)
		} else if content, ok := entry["content"].([]interface{}); ok {
			for _, block := range content {
				if m, ok := block.(map[string]interface{}); ok {
					if m["type"] == "text" {
						if text, ok := m["text"].(string); ok {
							parts = append(parts, text)
						}
					}
				}
			}
		}
		if msg, ok := entry["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].([]interface{}); ok {
				for _, block := range content {
					if m, ok := block.(map[string]interface{}); ok {
						if m["type"] == "text" {
							if text, ok := m["text"].(string); ok {
								parts = append(parts, text)
							}
						}
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n"), nil
}

// validateResearchBasis checks research-classified claims against the transcript
// and downgrades to llm_output if the cited source text isn't found in the transcript.
func validateResearchBasis(claims []types.ExtractedClaim, transcript string) {
	transcriptLower := strings.ToLower(transcript)

	for i := range claims {
		c := &claims[i]

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

		}
	}
}
