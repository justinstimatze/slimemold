package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/justinstimatze/slimemold/internal/adapt"
	"github.com/justinstimatze/slimemold/internal/analysis"
	"github.com/justinstimatze/slimemold/internal/extract"
	"github.com/justinstimatze/slimemold/internal/store"
	"github.com/justinstimatze/slimemold/internal/viz"
	"github.com/justinstimatze/slimemold/types"
)

type mcpServer struct {
	db        *store.DB
	extractor *extract.Extractor
	project   string
}

// topologyParams handles read/analyze operations.
type topologyParams struct {
	Action   string          `json:"action"`
	Project  string          `json:"project"`
	ClaimID  string          `json:"claim_id,omitempty"`
	Query    string          `json:"query,omitempty"`
	Basis    string          `json:"basis,omitempty"`
	Format   string          `json:"format,omitempty"`
	KBClaims []types.KBClaim `json:"kb_claims,omitempty"`
}

// claimsParams handles write operations.
type claimsParams struct {
	Action         string   `json:"action"`
	Project        string   `json:"project"`
	Text           string   `json:"text,omitempty"`
	Basis          string   `json:"basis,omitempty"`
	Confidence     float64  `json:"confidence,omitempty"`
	Source         string   `json:"source,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Supports       []string `json:"supports,omitempty"`
	Contradicts    []string `json:"contradicts,omitempty"`
	ClaimID        string   `json:"claim_id,omitempty"`
	Result         string   `json:"result,omitempty"`
	Notes          string   `json:"notes,omitempty"`
	KeepID         string   `json:"keep_id,omitempty"`
	AbsorbID       string   `json:"absorb_id,omitempty"`
	TranscriptPath string   `json:"transcript_path,omitempty"`
	SinceTurn      int      `json:"since_turn,omitempty"`
	DocumentPath   string   `json:"document_path,omitempty"`
	MaxChars       int      `json:"max_chars,omitempty"`
}

var topologySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "description": "Action: get_topology | get_vulnerabilities | get_claim | search | viz | export | analyze_kb", "enum": ["get_topology", "get_vulnerabilities", "get_claim", "search", "viz", "export", "analyze_kb"]},
		"project": {"type": "string", "description": "Project name"},
		"claim_id": {"type": "string", "description": "Claim ID (for get_claim)"},
		"query": {"type": "string", "description": "Search query (for search)"},
		"basis": {"type": "string", "description": "Filter by basis type"},
		"format": {"type": "string", "description": "Output format: ascii | dot | json"},
		"kb_claims": {"type": "array", "description": "Array of typed KB claims for stateless analysis (analyze_kb)", "items": {"type": "object", "properties": {"id": {"type": "string"}, "predicate_type": {"type": "string"}, "subject": {"type": "string"}, "object": {"type": "string"}, "basis": {"type": "string"}, "has_quote": {"type": "boolean"}, "provenance_url": {"type": "string"}}, "required": ["id", "predicate_type", "subject", "object"]}}
	},
	"required": ["action", "project"]
}`)

var claimsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "description": "Action: register | challenge | merge | parse_transcript | ingest_document", "enum": ["register", "challenge", "merge", "parse_transcript", "ingest_document"]},
		"project": {"type": "string", "description": "Project name"},
		"text": {"type": "string", "description": "Claim text (for register)"},
		"basis": {"type": "string", "description": "Epistemic basis (for register)"},
		"confidence": {"type": "number", "description": "Confidence 0-1 (for register)"},
		"source": {"type": "string", "description": "Citation (for register)"},
		"depends_on": {"type": "array", "items": {"type": "string"}, "description": "Claims this depends on (text)"},
		"supports": {"type": "array", "items": {"type": "string"}, "description": "Claims this supports (text)"},
		"contradicts": {"type": "array", "items": {"type": "string"}, "description": "Claims this contradicts (text)"},
		"claim_id": {"type": "string", "description": "Claim ID (for challenge/merge)"},
		"result": {"type": "string", "description": "Challenge result: upheld | weakened | refuted | revised"},
		"notes": {"type": "string", "description": "Notes"},
		"keep_id": {"type": "string", "description": "Claim to keep (for merge)"},
		"absorb_id": {"type": "string", "description": "Claim to absorb (for merge)"},
		"transcript_path": {"type": "string", "description": "Path to transcript file (for parse_transcript)"},
		"since_turn": {"type": "integer", "description": "Parse from this turn number onwards"},
		"document_path": {"type": "string", "description": "Path to an authored document — essay, paper, markdown notes — to chunk and extract (for ingest_document)"},
		"max_chars": {"type": "integer", "description": "Soft chunk-size limit in characters (default 12000)"}
	},
	"required": ["action", "project"]
}`)

// RunMCP starts the MCP server on stdio.
func RunMCP(db *store.DB, extractor *extract.Extractor, project string) error {
	s := &mcpServer{
		db:        db,
		extractor: extractor,
		project:   project,
	}

	srv := server.NewMCPServer(
		"slimemold",
		"0.5.7",
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)

	srv.AddTool(
		sdkmcp.NewToolWithRawSchema("topology", "Query and analyze the reasoning topology graph. Actions: get_topology, get_vulnerabilities, get_claim, search, viz, export", topologySchema),
		s.handleTopology,
	)

	srv.AddTool(
		sdkmcp.NewToolWithRawSchema("claims", "Modify the reasoning topology graph. Actions: register, challenge, merge, parse_transcript, ingest_document", claimsSchema),
		s.handleClaims,
	)

	return server.ServeStdio(srv)
}

func (s *mcpServer) handleTopology(ctx context.Context, req sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	var args topologyParams
	raw, _ := json.Marshal(req.Params.Arguments)
	if err := json.Unmarshal(raw, &args); err != nil {
		return sdkmcp.NewToolResultError(fmt.Sprintf("invalid params: %s", err)), nil
	}

	project := args.Project
	if project == "" {
		project = s.project
	}

	switch args.Action {
	case "get_topology":
		topo, err := CoreGetTopology(ctx, s.db, project)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(topo)

	case "get_vulnerabilities":
		vulns, err := CoreGetVulnerabilities(ctx, s.db, project)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(vulns)

	case "get_claim":
		if args.ClaimID == "" {
			return sdkmcp.NewToolResultError("claim_id required"), nil
		}
		claim, err := s.db.GetClaim(args.ClaimID)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		edges, _ := s.db.GetEdgesForClaim(args.ClaimID)
		result := map[string]interface{}{"claim": claim, "edges": edges}
		return jsonResult(result)

	case "search":
		if args.Query == "" {
			return sdkmcp.NewToolResultError("query required"), nil
		}
		claims, err := s.db.SearchClaims(project, args.Query, args.Basis)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(claims)

	case "viz":
		claims, _ := s.db.GetClaimsByProject(project)
		edges, _ := s.db.GetEdgesByProject(project)
		topo, vulns := analysis.Analyze(claims, edges, project)
		output := viz.RenderASCII(topo, vulns)
		return sdkmcp.NewToolResultText(output), nil

	case "export":
		claims, _ := s.db.GetClaimsByProject(project)
		edges, _ := s.db.GetEdgesByProject(project)
		format := args.Format
		if format == "" {
			format = "json"
		}
		switch format {
		case "json":
			return jsonResult(map[string]interface{}{"claims": claims, "edges": edges})
		case "dot":
			output := viz.RenderDOT(claims, edges, project)
			return sdkmcp.NewToolResultText(output), nil
		default:
			return sdkmcp.NewToolResultError(fmt.Sprintf("unknown format: %s", format)), nil
		}

	case "analyze_kb":
		if len(args.KBClaims) == 0 {
			return sdkmcp.NewToolResultError("kb_claims required"), nil
		}
		claims, edges := adapt.AdaptKBClaims(args.KBClaims)
		for i := range claims {
			claims[i].Project = project
		}
		topo, vulns := analysis.Analyze(claims, edges, project)
		result := map[string]interface{}{
			"topology":        topo,
			"vulnerabilities": vulns,
		}
		return jsonResult(result)

	default:
		return sdkmcp.NewToolResultError(fmt.Sprintf("unknown action: %s", args.Action)), nil
	}
}

func (s *mcpServer) handleClaims(ctx context.Context, req sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	var args claimsParams
	raw, _ := json.Marshal(req.Params.Arguments)
	if err := json.Unmarshal(raw, &args); err != nil {
		return sdkmcp.NewToolResultError(fmt.Sprintf("invalid params: %s", err)), nil
	}

	project := args.Project
	if project == "" {
		project = s.project
	}

	switch args.Action {
	case "register":
		if args.Text == "" {
			return sdkmcp.NewToolResultError("text required"), nil
		}
		basis := types.Basis(args.Basis)
		if basis == "" {
			basis = types.BasisVibes
		}
		conf := args.Confidence
		if conf == 0 {
			conf = 0.5
		}
		result, err := CoreRegisterClaim(ctx, s.db, project, args.Text, basis, conf, args.Source, args.DependsOn, args.Supports, args.Contradicts)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(result)

	case "challenge":
		if args.ClaimID == "" {
			return sdkmcp.NewToolResultError("claim_id required"), nil
		}
		if err := CoreChallengeClaim(ctx, s.db, args.ClaimID, args.Result, args.Notes); err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return sdkmcp.NewToolResultText(fmt.Sprintf("Claim %s challenged: %s", args.ClaimID, args.Result)), nil

	case "merge":
		if args.KeepID == "" || args.AbsorbID == "" {
			return sdkmcp.NewToolResultError("keep_id and absorb_id required"), nil
		}
		if err := s.db.MergeClaims(args.KeepID, args.AbsorbID); err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return sdkmcp.NewToolResultText(fmt.Sprintf("Merged %s into %s", args.AbsorbID, args.KeepID)), nil

	case "parse_transcript":
		if s.extractor == nil {
			return sdkmcp.NewToolResultError("extraction unavailable: ANTHROPIC_API_KEY not set"), nil
		}
		if args.TranscriptPath == "" {
			return sdkmcp.NewToolResultError("transcript_path required"), nil
		}
		audit, err := CoreParseTranscript(ctx, s.db, s.extractor, project, args.TranscriptPath, args.SinceTurn, "")
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return sdkmcp.NewToolResultText(audit.HookSummary), nil

	case "ingest_document":
		if s.extractor == nil {
			return sdkmcp.NewToolResultError("extraction unavailable: ANTHROPIC_API_KEY not set"), nil
		}
		if args.DocumentPath == "" {
			return sdkmcp.NewToolResultError("document_path required"), nil
		}
		audit, err := CoreIngestDocument(ctx, s.db, s.extractor, project, args.DocumentPath, args.MaxChars)
		if err != nil {
			return sdkmcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]interface{}{
			"new_claims":      audit.NewClaims,
			"new_edges":       audit.NewEdges,
			"total_claims":    audit.TotalClaims,
			"total_edges":     audit.TotalEdges,
			"summary":         audit.Summary,
			"vulnerabilities": audit.Vulnerabilities,
		})

	default:
		return sdkmcp.NewToolResultError(fmt.Sprintf("unknown action: %s", args.Action)), nil
	}
}

func jsonResult(v interface{}) (*sdkmcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return sdkmcp.NewToolResultError(err.Error()), nil
	}
	return sdkmcp.NewToolResultText(string(data)), nil
}
