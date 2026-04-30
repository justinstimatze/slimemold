package adapt

import (
	"testing"

	"github.com/justinstimatze/slimemold/types"
)

func TestParseWinzeInputArray(t *testing.T) {
	input := []byte(`[
		{"var_name":"c1","predicate":"Proposes","subject":"Chalmers","object":"hard_problem","file":"x.go"},
		{"var_name":"c2","predicate":"Disputes","subject":"Dennett","object":"hard_problem","file":"x.go"}
	]`)
	export, err := ParseWinzeInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if len(export.Claims) != 2 {
		t.Errorf("expected 2 claims, got %d", len(export.Claims))
	}
	if len(export.Provenance) != 0 {
		t.Errorf("expected 0 provenance, got %d", len(export.Provenance))
	}
}

func TestParseWinzeInputObject(t *testing.T) {
	input := []byte(`{
		"claims": [
			{"var_name":"c1","predicate":"Proposes","subject":"A","object":"B","prov_ref":"p1","file":"x.go"}
		],
		"provenance": [
			{"var_name":"p1","origin":"Wikipedia","quote":"some text","file":"x.go"}
		]
	}`)
	export, err := ParseWinzeInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if len(export.Claims) != 1 {
		t.Errorf("expected 1 claim, got %d", len(export.Claims))
	}
	if len(export.Provenance) != 1 {
		t.Errorf("expected 1 provenance, got %d", len(export.Provenance))
	}
}

func TestParseWinzeInputBOM(t *testing.T) {
	// UTF-8 BOM prefix
	input := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`[]`)...)
	export, err := ParseWinzeInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if export == nil {
		t.Fatal("expected non-nil export")
	}
}

func TestAdaptWinzeExportBasis(t *testing.T) {
	export := &WinzeExport{
		Claims: []WinzeClaimRecord{
			{VarName: "c1", Predicate: "Proposes", Subject: "A", Object: "B", ProvRef: "p1"},
			{VarName: "c2", Predicate: "Proposes", Subject: "C", Object: "D"}, // no prov_ref
		},
		Provenance: []WinzeProvRecord{
			{VarName: "p1", Origin: "Wikipedia", Quote: "exact text"},
		},
	}
	claims, _ := AdaptWinzeExport(export)
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	// c1 has provenance with quote → research basis
	if claims[0].Basis != types.BasisResearch {
		t.Errorf("c1: expected research basis (has quote), got %s", claims[0].Basis)
	}
	// c2 has no prov_ref → assumption basis
	if claims[1].Basis != types.BasisAssumption {
		t.Errorf("c2: expected assumption basis (no prov_ref), got %s", claims[1].Basis)
	}
}

func TestAdaptWinzeExportProvRefNoRecord(t *testing.T) {
	// prov_ref present but no matching provenance record → treat as sourced (HasQuote=true)
	export := &WinzeExport{
		Claims: []WinzeClaimRecord{
			{VarName: "c1", Predicate: "Proposes", Subject: "A", Object: "B", ProvRef: "missing_prov"},
		},
	}
	claims, _ := AdaptWinzeExport(export)
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if claims[0].Basis != types.BasisResearch {
		t.Errorf("expected research basis (prov_ref present), got %s", claims[0].Basis)
	}
}

func TestAdaptWinzeExportPredicateMapping(t *testing.T) {
	export := &WinzeExport{
		Claims: []WinzeClaimRecord{
			{VarName: "c1", Predicate: "Disputes", Subject: "Dennett", Object: "hard_problem"},
			{VarName: "c2", Predicate: "Proposes", Subject: "Chalmers", Object: "hard_problem"},
		},
	}
	claims, edges := AdaptWinzeExport(export)
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	// Shared entity "hard_problem" → one edge
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (shared entity), got %d", len(edges))
	}
	// Disputes predicate → contradicts edge
	if edges[0].Relation != types.RelContradicts {
		t.Errorf("expected contradicts edge for Disputes, got %s", edges[0].Relation)
	}
}

func TestAdaptWinzeExportSkipsEmptySlots(t *testing.T) {
	export := &WinzeExport{
		Claims: []WinzeClaimRecord{
			{VarName: "c1", Predicate: "Proposes", Subject: "", Object: "B"},  // empty subject
			{VarName: "c2", Predicate: "Proposes", Subject: "A", Object: ""},  // empty object
			{VarName: "c3", Predicate: "Proposes", Subject: "A", Object: "B"}, // valid
		},
	}
	claims, _ := AdaptWinzeExport(export)
	if len(claims) != 1 {
		t.Errorf("expected 1 claim (invalid slots skipped), got %d", len(claims))
	}
}
