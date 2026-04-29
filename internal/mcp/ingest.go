package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/ingest"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/types"
)

// documentPromptVersion bumps whenever systemPrompt + documentModeSupplement
// changes in a way that invalidates prior cached extractions. The cache key
// includes this so we don't serve stale results after a prompt edit.
const documentPromptVersion = 4

// DocumentPromptVersion exposes the version constant so outside packages
// (e.g. the eval CLI) can label snapshots by prompt identity.
func DocumentPromptVersion() int { return documentPromptVersion }

// CoreIngestDocument chunks an authored document and extracts claims from each
// chunk. One session per ingest, named deterministically from the path so
// re-ingesting the same file reuses the session identifier.
func CoreIngestDocument(ctx context.Context, db *store.DB, extractor *extract.Extractor, project, path string, maxChars int) (*types.AuditResult, error) {
	content, displayPath, err := readDocument(path)
	if err != nil {
		return nil, err
	}

	chunks := ingest.Chunk(content, maxChars)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no extractable content in %s", displayPath)
	}

	sessionID := documentSessionID(displayPath)

	// Flip extractor into document mode for this ingest. Restore on exit so a
	// long-lived extractor (e.g. the MCP server's) doesn't stay in doc mode.
	prevDocMode := extractor.DocumentMode
	extractor.DocumentMode = true
	defer func() { extractor.DocumentMode = prevDocMode }()

	totalNewClaims := 0
	totalNewEdges := 0

	for i, chunk := range chunks {
		newClaims, newEdges, err := ingestOneChunk(ctx, db, extractor, project, sessionID, displayPath, chunk)
		if err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
		}
		totalNewClaims += newClaims
		totalNewEdges += newEdges
		fmt.Fprintf(os.Stderr, "  chunk %d/%d: %s — %d claims, %d edges\n",
			i+1, len(chunks), chunkLabel(chunk), newClaims, newEdges)
	}

	claims, _ := db.GetClaimsByProject(project)
	edges, _ := db.GetEdgesByProject(project)
	topo, vulns := analysis.Analyze(claims, edges, project)

	totalClaims, _ := db.CountClaims(project)
	totalEdges, _ := db.CountEdges(project)

	summary := analysis.FormatAuditSummary(topo, vulns)

	_ = db.CreateAudit(&types.Audit{
		Project:       project,
		SessionID:     sessionID,
		Findings:      summary,
		ClaimCount:    totalClaims,
		EdgeCount:     totalEdges,
		CriticalCount: vulns.CriticalCount,
	})

	return &types.AuditResult{
		NewClaims:       totalNewClaims,
		NewEdges:        totalNewEdges,
		TotalClaims:     totalClaims,
		TotalEdges:      totalEdges,
		Vulnerabilities: *vulns,
		Summary:         summary,
	}, nil
}

func ingestOneChunk(ctx context.Context, db *store.DB, extractor *extract.Extractor, project, sessionID, displayPath string, chunk ingest.DocumentChunk) (int, int, error) {
	existingClaims, _ := db.GetClaimsByProject(project)
	existingEdges, _ := db.GetEdgesByProject(project)
	relevant := selectRelevantClaims(existingClaims, existingEdges, chunk.Text)

	existingRefs := make([]extract.ExistingClaimRef, 0, len(relevant))
	for _, c := range relevant {
		existingRefs = append(existingRefs, extract.ExistingClaimRef{
			ID:    c.ID,
			Text:  c.Text,
			Basis: string(c.Basis),
		})
	}

	chunkText := chunk.Text
	if len(chunk.HeadingPath) > 0 {
		chunkText = fmt.Sprintf("[Section: %s]\n\n%s", strings.Join(chunk.HeadingPath, " > "), chunk.Text)
	}

	result, cached, err := extractWithCache(ctx, db, extractor, chunk.ContentHash, chunkText, existingRefs)
	if err != nil {
		return 0, 0, fmt.Errorf("extraction: %w", err)
	}
	if cached {
		fmt.Fprintf(os.Stderr, "    (cache hit)\n")
	}
	if result == nil || len(result.Claims) == 0 {
		return 0, 0, nil
	}

	validateResearchBasis(result.Claims, chunk.Text)

	source := formatDocumentSource(displayPath, chunk.HeadingPath)
	for i := range result.Claims {
		if result.Claims[i].Speaker != string(types.SpeakerDocument) {
			result.Claims[i].Speaker = string(types.SpeakerDocument)
		}
		// Authored prose never has basis=llm_output — the author is not an AI.
		// The prompt asks the extractor to avoid llm_output in document mode, but
		// it leaks through occasionally on confidently-asserted technical content
		// (~10% of Sokal claims). Normalize here so the invariant doesn't depend
		// on prompt adherence.
		if result.Claims[i].Basis == "llm_output" {
			result.Claims[i].Basis = string(types.BasisVibes)
		}
		if result.Claims[i].Source == "" {
			result.Claims[i].Source = source
		}
	}

	result.Claims = deduplicateBatch(result.Claims)
	existingIndex := buildNgramIndex(existingClaims)

	txDB, err := db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = txDB.Rollback() }()

	indexToID := make(map[int]string)
	newClaims := 0
	for _, ec := range result.Claims {
		if match := existingIndex.findSimilar(ec.Text, types.Speaker(ec.Speaker)); match != nil {
			indexToID[ec.Index] = match.ID
			_ = txDB.RecordSessionClaim(sessionID, match.ID)
			continue
		}
		claim := &types.Claim{
			ID:                uuid.New().String(),
			Text:              ec.Text,
			Basis:             types.Basis(ec.Basis),
			Confidence:        ec.Confidence,
			Source:            ec.Source,
			SessionID:         sessionID,
			Project:           project,
			Speaker:           types.Speaker(ec.Speaker),
			CreatedAt:         time.Now(),
			TerminatesInquiry: ec.TerminatesInquiry,
		}
		if err := txDB.CreateClaim(claim); err != nil {
			return 0, 0, fmt.Errorf("creating claim: %w", err)
		}
		_ = txDB.RecordSessionClaim(sessionID, claim.ID)
		indexToID[ec.Index] = claim.ID
		newClaims++
	}

	newEdges := 0
	for _, ec := range result.Claims {
		newEdges += resolveEdgesForClaim(txDB, ec, indexToID)
	}

	pruneHighDegreeEdges(txDB, project)

	if err := txDB.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return newClaims, newEdges, nil
}

// resolveEdgesForClaim inserts all edges (intra-batch by index, cross-batch by
// existing claim ID) for a single extracted claim and returns the count of
// edges actually created.
func resolveEdgesForClaim(txDB *store.DB, ec types.ExtractedClaim, indexToID map[int]string) int {
	fromID, ok := indexToID[ec.Index]
	if !ok {
		return 0
	}
	n := 0
	n += resolveIntraBatchEdges(txDB, fromID, ec.DependsOnIndices, indexToID, types.RelDependsOn)
	n += resolveIntraBatchEdges(txDB, fromID, ec.SupportsIndices, indexToID, types.RelSupports)
	n += resolveIntraBatchEdges(txDB, fromID, ec.ContradictsIndices, indexToID, types.RelContradicts)
	n += resolveIntraBatchEdges(txDB, fromID, ec.QuestionsIndices, indexToID, types.RelQuestions)
	n += resolveCrossBatchEdges(txDB, fromID, ec.DependsOnExisting, types.RelDependsOn)
	n += resolveCrossBatchEdges(txDB, fromID, ec.SupportsExisting, types.RelSupports)
	n += resolveCrossBatchEdges(txDB, fromID, ec.ContradictsExisting, types.RelContradicts)
	n += resolveCrossBatchEdges(txDB, fromID, ec.QuestionsExisting, types.RelQuestions)
	return n
}

func resolveIntraBatchEdges(txDB *store.DB, fromID string, targets []int, indexToID map[int]string, rel types.Relation) int {
	n := 0
	for _, targetIdx := range targets {
		toID, ok := indexToID[targetIdx]
		if !ok || toID == fromID {
			continue
		}
		if createEdgeIfNew(txDB, fromID, toID, rel) {
			n++
		}
	}
	return n
}

func resolveCrossBatchEdges(txDB *store.DB, fromID string, existingIDs []string, rel types.Relation) int {
	n := 0
	for _, existingID := range existingIDs {
		if existingID == fromID || !claimExists(txDB, existingID) {
			continue
		}
		if createEdgeIfNew(txDB, fromID, existingID, rel) {
			n++
		}
	}
	return n
}

// maxDocumentBytes caps how much we're willing to load into memory at once.
// 10 MiB is generously above any realistic essay, paper, or book chapter
// (typical plain-text novels are ~500 KB; full academic papers ~1 MB).
// Streaming ingestion is Phase 2; this guard is here so a mis-typed path or
// pathological input can't OOM the process.
const maxDocumentBytes = 10 * 1024 * 1024

func readDocument(path string) (string, string, error) {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxDocumentBytes+1))
		if err != nil {
			return "", "", fmt.Errorf("reading stdin: %w", err)
		}
		if int64(len(data)) > maxDocumentBytes {
			return "", "", fmt.Errorf("stdin input exceeds %d-byte limit — split the document or ingest sections separately", maxDocumentBytes)
		}
		return string(data), "<stdin>", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", path, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxDocumentBytes {
		return "", "", fmt.Errorf("%s is %d bytes, exceeds %d-byte limit — split the document or ingest sections separately",
			path, info.Size(), maxDocumentBytes)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(data), abs, nil
}

func documentSessionID(displayPath string) string {
	sum := sha256.Sum256([]byte(displayPath))
	return "doc:" + hex.EncodeToString(sum[:])[:12]
}

// DocumentSessionID is the exported form used by callers that need to
// correlate ingested documents to their session_id (e.g. the eval command
// building per-document summaries from a shared project graph).
//
// The input path is resolved to absolute via filepath.Abs before hashing so
// callers get the same session_id that CoreIngestDocument uses internally,
// regardless of whether they pass a relative or absolute path. Silent
// fallback to the input on Abs error — the error case is essentially
// impossible on valid input, and returning ("", error) would make the
// signature awkward for callers who just want a lookup key.
func DocumentSessionID(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return documentSessionID(abs)
}

func formatDocumentSource(displayPath string, headingPath []string) string {
	base := filepath.Base(displayPath)
	if len(headingPath) == 0 {
		return base
	}
	return base + "#" + strings.Join(headingPath, "/")
}

// extractWithCache looks up a prior extraction for this content/model/prompt
// tuple and returns it on hit. On miss, runs the extractor and stores the
// result for next time. Cache key does not include existingRefs — the cached
// intra-batch claims and edges are always valid; cross-batch edge hints from
// the cache are IDs that may no longer exist, and get filtered out downstream
// by the claimExists check.
func extractWithCache(ctx context.Context, db *store.DB, extractor *extract.Extractor, contentHash, chunkText string, existingRefs []extract.ExistingClaimRef) (*types.ExtractionResult, bool, error) {
	if cached, ok := db.GetExtractionCache(contentHash, extractor.Model(), documentPromptVersion); ok {
		var result types.ExtractionResult
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return &result, true, nil
		}
		// Unmarshal failed — the cached row is corrupt (truncated write, schema
		// mismatch, etc.). Delete it so we don't keep re-parsing the same bad
		// blob on every run, then fall through to re-extract.
		_ = db.DeleteExtractionCache(contentHash, extractor.Model(), documentPromptVersion)
	}

	result, err := extractor.Extract(ctx, chunkText, existingRefs)
	if err != nil {
		return nil, false, err
	}
	if result != nil {
		if raw, err := json.Marshal(result); err == nil {
			_ = db.SetExtractionCache(contentHash, extractor.Model(), documentPromptVersion, string(raw))
		}
	}
	return result, false, nil
}

func chunkLabel(chunk ingest.DocumentChunk) string {
	if len(chunk.HeadingPath) == 0 {
		return "(no heading)"
	}
	return strings.Join(chunk.HeadingPath, " > ")
}
