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
const documentPromptVersion = 2

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
		indexToID[ec.Index] = claim.ID
		newClaims++
	}

	newEdges := 0
	for _, ec := range result.Claims {
		fromID, ok := indexToID[ec.Index]
		if !ok {
			continue
		}
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

	pruneHighDegreeEdges(txDB, project)

	if err := txDB.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return newClaims, newEdges, nil
}

func readDocument(path string) (string, string, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", fmt.Errorf("reading stdin: %w", err)
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
		// Unmarshal failed — fall through to re-extract.
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
