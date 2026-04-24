package types

import "time"

// Basis describes how a claim was established.
type Basis string

const (
	BasisResearch   Basis = "research"
	BasisEmpirical  Basis = "empirical"
	BasisAnalogy    Basis = "analogy"
	BasisVibes      Basis = "vibes"
	BasisLLMOutput  Basis = "llm_output"
	BasisDeduction  Basis = "deduction"
	BasisAssumption Basis = "assumption"
	BasisDefinition Basis = "definition"
	// BasisConvention is a project/organization-declared practice or policy
	// stated as an adopted choice: "this project uses Go 1.26", "agents must
	// push after committing", "we track work in beads". Stipulative like
	// definition but about practice/policy rather than semantics of terms.
	BasisConvention Basis = "convention"
)

// Relation describes the epistemic relationship between two claims.
type Relation string

const (
	RelSupports    Relation = "supports"
	RelDependsOn   Relation = "depends_on"
	RelContradicts Relation = "contradicts"
	// RelQuestions — "A questions B." Claim A raises epistemic doubt about
	// claim B without asserting B is wrong. Distinct from contradicts: a
	// question is asking for clarification/justification, a contradiction
	// is counter-evidence. Ported from the buddy port, which added this
	// edge type and uses it as the cleanest "pushback" signal for the
	// productive-stress-test pattern.
	RelQuestions Relation = "questions"
)

// Speaker identifies who made a claim.
type Speaker string

const (
	SpeakerUser      Speaker = "user"
	SpeakerAssistant Speaker = "assistant"
	SpeakerDocument  Speaker = "document"
)

// Claim is a substantive assertion made during reasoning.
type Claim struct {
	ID                string    `json:"id"`
	Text              string    `json:"text"`
	Basis             Basis     `json:"basis"`
	Confidence        float64   `json:"confidence"`
	Source            string    `json:"source"`
	SessionID         string    `json:"session_id"`
	Project           string    `json:"project"`
	TurnNumber        int       `json:"turn_number"`
	Speaker           Speaker   `json:"speaker"`
	CreatedAt         time.Time `json:"created_at"`
	Challenged        bool      `json:"challenged"`
	Verified          bool      `json:"verified"`
	TerminatesInquiry bool      `json:"terminates_inquiry"`
}

// Edge is a directed epistemic relationship between two claims.
type Edge struct {
	ID        string    `json:"id"`
	FromID    string    `json:"from_id"`
	ToID      string    `json:"to_id"`
	Relation  Relation  `json:"relation"`
	Strength  float64   `json:"strength"`
	CreatedAt time.Time `json:"created_at"`
}

// Audit records a topology analysis snapshot.
type Audit struct {
	ID            string    `json:"id"`
	Project       string    `json:"project"`
	SessionID     string    `json:"session_id"`
	Timestamp     time.Time `json:"timestamp"`
	Findings      string    `json:"findings"`
	ClaimCount    int       `json:"claim_count"`
	EdgeCount     int       `json:"edge_count"`
	CriticalCount int       `json:"critical_count"`
}

// Vulnerability is a structural weakness in the reasoning graph.
type Vulnerability struct {
	Severity    string   `json:"severity"` // critical, warning, info
	Type        string   `json:"type"`     // load_bearing_vibes, bottleneck, orphan, unchallenged_chain
	Description string   `json:"description"`
	ClaimIDs    []string `json:"claim_ids"`
}

// ClusterInfo describes a connected group of claims.
type ClusterInfo struct {
	ID      int     `json:"id"`
	Label   string  `json:"label"`
	Claims  []Claim `json:"claims"`
	Density float64 `json:"density"`
	Edges   int     `json:"edges"`
}

// Topology is the full structural summary of a project's claim graph.
type Topology struct {
	Project     string        `json:"project"`
	ClaimCount  int           `json:"claim_count"`
	EdgeCount   int           `json:"edge_count"`
	BasisCounts map[Basis]int `json:"basis_counts"`
	Clusters    []ClusterInfo `json:"clusters"`
	Orphans     []Claim       `json:"orphans"`
	MaxDepth    int           `json:"max_depth"`
}

// Vulnerabilities is the result of structural analysis.
type Vulnerabilities struct {
	Project       string          `json:"project"`
	Items         []Vulnerability `json:"items"`
	CriticalCount int             `json:"critical_count"`
	WarningCount  int             `json:"warning_count"`
	InfoCount     int             `json:"info_count"`
	// StrengthCount counts bright/structural-strength findings (Type prefix
	// "strength_"). Kept separate from InfoCount so summaries don't fold
	// strengths into deficits. Bright findings are severity=info but
	// semantically distinct — they're not vulnerabilities, they're the
	// opposite.
	StrengthCount int `json:"strength_count"`
}

// AuditResult is the combined output of parse_transcript: extraction + analysis.
type AuditResult struct {
	NewClaims       int             `json:"new_claims"`
	NewEdges        int             `json:"new_edges"`
	TotalClaims     int             `json:"total_claims"`
	TotalEdges      int             `json:"total_edges"`
	Vulnerabilities Vulnerabilities `json:"vulnerabilities"`
	Summary         string          `json:"summary"`      // Full audit for storage/CLI
	HookSummary     string          `json:"hook_summary"` // Terse, directive output for hook injection
	LastTurn        int             `json:"last_turn"`    // Total turns in transcript at extraction time
}

// ClaimResult is returned after registering a claim.
type ClaimResult struct {
	ClaimID   string   `json:"claim_id"`
	GraphSize int      `json:"graph_size"`
	Warnings  []string `json:"warnings"`
}

// ExtractedClaim is a claim extracted from a transcript by the LLM.
type ExtractedClaim struct {
	Index      int     `json:"index"`
	Text       string  `json:"text"`
	Basis      string  `json:"basis"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
	Speaker    string  `json:"speaker"`
	// Intra-batch edges (numeric indices within this extraction batch)
	DependsOnIndices   []int `json:"depends_on_indices"`
	SupportsIndices    []int `json:"supports_indices"`
	ContradictsIndices []int `json:"contradicts_indices"`
	QuestionsIndices   []int `json:"questions_indices"`
	// Cross-batch edges (existing claim IDs from the graph)
	DependsOnExisting   []string `json:"depends_on_existing"`
	SupportsExisting    []string `json:"supports_existing"`
	ContradictsExisting []string `json:"contradicts_existing"`
	QuestionsExisting   []string `json:"questions_existing"`
	// Premature closure detection
	TerminatesInquiry bool `json:"terminates_inquiry"`
}

// ExtractionResult is the structured output from the LLM extraction.
type ExtractionResult struct {
	Claims []ExtractedClaim `json:"claims"`
}

// KBClaim is a typed knowledge base claim from an external system (e.g. winze).
// Speaker is left empty — KB claims have no user/assistant attribution.
type KBClaim struct {
	ID            string `json:"id"`
	PredicateType string `json:"predicate_type"`
	Subject       string `json:"subject"`
	Object        string `json:"object"`
	Basis         Basis  `json:"basis"`
	HasQuote      bool   `json:"has_quote"`
	ProvenanceURL string `json:"provenance_url"`
}
