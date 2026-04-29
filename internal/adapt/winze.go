package adapt

import (
	"encoding/json"
	"fmt"

	"github.com/justinstimatze/slimemold/types"
)

// WinzeClaimRecord matches winze's cmd/query --json claimRecord output format.
// Produce with: go run ./cmd/query --json --disputes .
//
//	go run ./cmd/query --json --theories <concept> .
//	go run ./cmd/query --json --claims <entity> .
type WinzeClaimRecord struct {
	VarName   string `json:"var_name"`
	Predicate string `json:"predicate"`
	Subject   string `json:"subject"`
	Object    string `json:"object"`
	ProvRef   string `json:"prov_ref,omitempty"`
	File      string `json:"file"`
}

// WinzeProvRecord matches winze's cmd/query --json provRecord output format.
// Produce with: go run ./cmd/query --json --provenance <query> .
type WinzeProvRecord struct {
	VarName string `json:"var_name"`
	Origin  string `json:"origin"`
	Quote   string `json:"quote,omitempty"`
	File    string `json:"file"`
}

// WinzeExport is the combined format for slimemold analysis.
// Either field alone is valid; Claims is required, Provenance is optional.
//
// Produce with:
//
//	echo '{"claims":' > out.json
//	go run ./cmd/query --json --disputes . >> out.json
//	echo ', "provenance":' >> out.json
//	go run ./cmd/query --json --provenance "" . >> out.json
//	echo '}' >> out.json
//
// Or pipe a raw WinzeClaimRecord[] array directly.
type WinzeExport struct {
	Claims     []WinzeClaimRecord `json:"claims"`
	Provenance []WinzeProvRecord  `json:"provenance,omitempty"`
}

// ParseWinzeInput parses a JSON blob in either of two formats:
//   - A raw WinzeClaimRecord array: [{"var_name": ..., "predicate": ...}, ...]
//   - A WinzeExport object:         {"claims": [...], "provenance": [...]}
func ParseWinzeInput(data []byte) (*WinzeExport, error) {
	data = trimBOM(data)

	// Try array first (most common: piped from a single --json query)
	if len(data) > 0 && data[0] == '[' {
		var claims []WinzeClaimRecord
		if err := json.Unmarshal(data, &claims); err != nil {
			return nil, fmt.Errorf("parsing claim array: %w", err)
		}
		return &WinzeExport{Claims: claims}, nil
	}

	// Try wrapped object
	var export WinzeExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parsing winze export: %w", err)
	}
	return &export, nil
}

// AdaptWinzeExport converts a WinzeExport to slimemold Claims and Edges.
// Provenance records are used to set HasQuote and ProvenanceURL on KBClaims;
// if absent, prov_ref presence alone is treated as "sourced" (HasQuote=true).
func AdaptWinzeExport(export *WinzeExport) ([]types.Claim, []types.Edge) {
	provByVar := make(map[string]WinzeProvRecord, len(export.Provenance))
	for _, p := range export.Provenance {
		provByVar[p.VarName] = p
	}

	kbClaims := make([]types.KBClaim, 0, len(export.Claims))
	for _, c := range export.Claims {
		if c.Subject == "" || c.Object == "" {
			continue
		}
		kb := types.KBClaim{
			ID:            c.VarName,
			PredicateType: c.Predicate,
			Subject:       c.Subject,
			Object:        c.Object,
		}
		if c.ProvRef != "" {
			if prov, ok := provByVar[c.ProvRef]; ok {
				kb.HasQuote = prov.Quote != ""
				kb.ProvenanceURL = prov.Origin
			} else {
				// prov_ref present but record not supplied — claim is sourced
				kb.HasQuote = true
			}
		}
		kbClaims = append(kbClaims, kb)
	}
	return AdaptKBClaims(kbClaims)
}

// trimBOM strips a UTF-8 BOM if present.
func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}
