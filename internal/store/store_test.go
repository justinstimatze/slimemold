package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justinstimatze/slimemold/types"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir, "test-project")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesDir(t *testing.T) {
	dir := t.TempDir() + "/sub/deep"
	db, err := Open(dir, "p")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	if _, err := os.Stat(dir + "/p/graph.sqlite"); err != nil {
		t.Fatalf("expected database file: %v", err)
	}
}

func TestCreateAndGetClaim(t *testing.T) {
	db := testDB(t)

	c := &types.Claim{
		Text:      "dopamine encodes prediction error",
		Basis:     types.BasisResearch,
		Source:    "Schultz 1997",
		SessionID: "s1",
		Project:   "test-project",
		Speaker:   types.SpeakerAssistant,
	}
	if err := db.CreateClaim(c); err != nil {
		t.Fatalf("CreateClaim: %v", err)
	}
	if c.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := db.GetClaim(c.ID)
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.Text != c.Text {
		t.Errorf("text = %q, want %q", got.Text, c.Text)
	}
	if got.Basis != types.BasisResearch {
		t.Errorf("basis = %q, want research", got.Basis)
	}
	if got.Source != "Schultz 1997" {
		t.Errorf("source = %q, want Schultz 1997", got.Source)
	}
}

func TestCreateAndGetEdge(t *testing.T) {
	db := testDB(t)

	c1 := &types.Claim{Text: "claim A", Basis: types.BasisResearch, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	c2 := &types.Claim{Text: "claim B", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(c1)
	db.CreateClaim(c2)

	e := &types.Edge{FromID: c1.ID, ToID: c2.ID, Relation: types.RelSupports}
	if _, err := db.CreateEdge(e); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	edges, err := db.GetEdgesForClaim(c1.ID)
	if err != nil {
		t.Fatalf("GetEdgesForClaim: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].Relation != types.RelSupports {
		t.Errorf("relation = %q, want supports", edges[0].Relation)
	}
}

func TestGetClaimsByProject(t *testing.T) {
	db := testDB(t)

	for _, text := range []string{"alpha", "beta", "gamma"} {
		db.CreateClaim(&types.Claim{Text: text, Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})
	}
	// Different project
	db.CreateClaim(&types.Claim{Text: "other", Basis: types.BasisVibes, SessionID: "s1", Project: "other-project", Speaker: types.SpeakerUser})

	claims, err := db.GetClaimsByProject("test-project")
	if err != nil {
		t.Fatalf("GetClaimsByProject: %v", err)
	}
	if len(claims) != 3 {
		t.Errorf("got %d claims, want 3", len(claims))
	}
}

func TestSearchClaims(t *testing.T) {
	db := testDB(t)

	db.CreateClaim(&types.Claim{Text: "dopamine encodes prediction error", Basis: types.BasisResearch, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})
	db.CreateClaim(&types.Claim{Text: "slime mold forages unevenly", Basis: types.BasisAnalogy, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})

	results, err := db.SearchClaims("test-project", "dopamine", "")
	if err != nil {
		t.Fatalf("SearchClaims: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	// Filter by basis
	results, err = db.SearchClaims("test-project", "dopamine", "analogy")
	if err != nil {
		t.Fatalf("SearchClaims with basis: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (wrong basis)", len(results))
	}
}

func TestFindClaimByText(t *testing.T) {
	db := testDB(t)

	db.CreateClaim(&types.Claim{Text: "Exact Match Claim", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})

	got, err := db.FindClaimByText("test-project", "exact match claim")
	if err != nil {
		t.Fatalf("FindClaimByText: %v", err)
	}
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Text != "Exact Match Claim" {
		t.Errorf("text = %q", got.Text)
	}

	got, err = db.FindClaimByText("test-project", "nonexistent")
	if err != nil {
		t.Fatalf("FindClaimByText: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestChallengeClaim(t *testing.T) {
	db := testDB(t)

	c := &types.Claim{Text: "weak claim", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(c)

	if err := db.ChallengeClaim(c.ID, "weakened", "revised: stronger claim", "research", "found source"); err != nil {
		t.Fatalf("ChallengeClaim: %v", err)
	}

	got, _ := db.GetClaim(c.ID)
	if !got.Challenged {
		t.Error("expected challenged = true")
	}
	if got.Text != "revised: stronger claim" {
		t.Errorf("text = %q, want revised", got.Text)
	}
	if got.Basis != types.BasisResearch {
		t.Errorf("basis = %q, want research", got.Basis)
	}
}

func TestMergeClaims(t *testing.T) {
	db := testDB(t)

	c1 := &types.Claim{Text: "keep this", Basis: types.BasisResearch, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	c2 := &types.Claim{Text: "absorb this", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	c3 := &types.Claim{Text: "downstream", Basis: types.BasisDeduction, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(c1)
	db.CreateClaim(c2)
	db.CreateClaim(c3)

	// c2 supports c3
	db.CreateEdge(&types.Edge{FromID: c2.ID, ToID: c3.ID, Relation: types.RelSupports})

	if err := db.MergeClaims(c1.ID, c2.ID); err != nil {
		t.Fatalf("MergeClaims: %v", err)
	}

	// c2 should be gone
	_, err := db.GetClaim(c2.ID)
	if err == nil {
		t.Error("expected absorbed claim to be deleted")
	}

	// Edge should now point from c1 to c3
	edges, _ := db.GetEdgesForClaim(c1.ID)
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].FromID != c1.ID || edges[0].ToID != c3.ID {
		t.Errorf("edge from=%s to=%s, want %s→%s", edges[0].FromID, edges[0].ToID, c1.ID, c3.ID)
	}
}

func TestCountClaimsAndEdges(t *testing.T) {
	db := testDB(t)

	c1 := &types.Claim{Text: "a", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	c2 := &types.Claim{Text: "b", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(c1)
	db.CreateClaim(c2)
	db.CreateEdge(&types.Edge{FromID: c1.ID, ToID: c2.ID, Relation: types.RelSupports})

	n, _ := db.CountClaims("test-project")
	if n != 2 {
		t.Errorf("claims = %d, want 2", n)
	}
	n, _ = db.CountEdges("test-project")
	if n != 1 {
		t.Errorf("edges = %d, want 1", n)
	}
}

func TestDeleteProject(t *testing.T) {
	db := testDB(t)

	c := &types.Claim{Text: "doomed", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser}
	db.CreateClaim(c)

	if err := db.DeleteProject("test-project"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	n, _ := db.CountClaims("test-project")
	if n != 0 {
		t.Errorf("claims = %d after delete, want 0", n)
	}
}

func TestCreateAudit(t *testing.T) {
	db := testDB(t)

	a := &types.Audit{
		Project:       "test-project",
		SessionID:     "s1",
		Findings:      "2 critical findings",
		ClaimCount:    10,
		EdgeCount:     15,
		CriticalCount: 2,
	}
	if err := db.CreateAudit(a); err != nil {
		t.Fatalf("CreateAudit: %v", err)
	}
	if a.ID == "" {
		t.Error("expected ID to be set")
	}
}

func TestTransactionCommit(t *testing.T) {
	db := testDB(t)

	txDB, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	txDB.CreateClaim(&types.Claim{ID: "tx-claim", Text: "in transaction", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})

	if err := txDB.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Claim should be visible via the original db handle
	c, err := db.GetClaim("tx-claim")
	if err != nil {
		t.Fatalf("GetClaim after commit: %v", err)
	}
	if c.Text != "in transaction" {
		t.Errorf("claim text = %q, want %q", c.Text, "in transaction")
	}
}

func TestTransactionRollback(t *testing.T) {
	db := testDB(t)

	txDB, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	txDB.CreateClaim(&types.Claim{ID: "rolled-back", Text: "should not persist", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})

	if err := txDB.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Claim should NOT be visible
	_, err = db.GetClaim("rolled-back")
	if err == nil {
		t.Error("claim should not exist after rollback")
	}
}

func TestInTxNestedSafe(t *testing.T) {
	db := testDB(t)

	// Begin an outer transaction
	txDB, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Create two claims within the transaction
	txDB.CreateClaim(&types.Claim{ID: "keep", Text: "keeper", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})
	txDB.CreateClaim(&types.Claim{ID: "absorb", Text: "absorbed", Basis: types.BasisVibes, SessionID: "s1", Project: "test-project", Speaker: types.SpeakerUser})
	txDB.CreateEdge(&types.Edge{FromID: "absorb", ToID: "keep", Relation: types.RelSupports})

	// MergeClaims uses inTx internally — should NOT deadlock when already in a transaction
	if err := txDB.MergeClaims("keep", "absorb"); err != nil {
		t.Fatalf("MergeClaims inside transaction: %v", err)
	}

	if err := txDB.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Only "keep" should survive
	claims, _ := db.GetClaimsByProject("test-project")
	if len(claims) != 1 {
		t.Errorf("claims after merge = %d, want 1", len(claims))
	}
	if len(claims) > 0 && claims[0].ID != "keep" {
		t.Errorf("surviving claim = %s, want keep", claims[0].ID)
	}
}

// TestSpeakerCheckMigration verifies that an existing DB created with the old
// claims.speaker CHECK constraint (pre-'document') is rebuilt on Open, data is
// preserved, and the new constraint accepts 'document' speakers.
func TestSpeakerCheckMigration(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "test-project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(projectDir, "graph.sqlite")
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

	// Create a DB with the OLD schema — speaker CHECK only allows user/assistant.
	{
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("raw open: %v", err)
		}
		oldSchema := `CREATE TABLE claims (
			id          TEXT PRIMARY KEY,
			text        TEXT NOT NULL,
			basis       TEXT NOT NULL CHECK(basis IN ('research','empirical','analogy','vibes','llm_output','deduction','assumption','definition')),
			confidence  REAL DEFAULT 0.5,
			source      TEXT DEFAULT '',
			session_id  TEXT NOT NULL,
			project     TEXT NOT NULL,
			turn_number INTEGER DEFAULT 0,
			speaker     TEXT DEFAULT 'user' CHECK(speaker IN ('user','assistant')),
			created_at  TEXT NOT NULL,
			challenged  INTEGER DEFAULT 0,
			verified    INTEGER DEFAULT 0
		)`
		if _, err := raw.Exec(oldSchema); err != nil {
			t.Fatalf("create old schema: %v", err)
		}
		// Seed a row so we can verify preservation.
		_, err = raw.Exec(
			`INSERT INTO claims (id, text, basis, session_id, project, speaker, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"pre-migration-claim", "seeded before migration", "vibes",
			"s1", "test-project", "user", time.Now().Format(time.RFC3339),
		)
		if err != nil {
			t.Fatalf("seed insert: %v", err)
		}
		// Confirm the OLD constraint rejects 'document' — sanity check.
		_, err = raw.Exec(
			`INSERT INTO claims (id, text, basis, session_id, project, speaker, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"reject-me", "should be blocked", "vibes", "s1", "test-project", "document", time.Now().Format(time.RFC3339),
		)
		if err == nil {
			t.Fatal("expected old schema to reject speaker='document', but insert succeeded")
		}
		raw.Close()
	}

	// Open via the store — this runs migrate() → migrateSpeakerCheck().
	db, err := Open(dir, "test-project")
	if err != nil {
		t.Fatalf("Open after migration: %v", err)
	}
	defer db.Close()

	// Seeded row must still be there.
	existing, err := db.GetClaim("pre-migration-claim")
	if err != nil {
		t.Fatalf("seeded claim lost during migration: %v", err)
	}
	if existing.Text != "seeded before migration" {
		t.Errorf("seeded claim text corrupted: %q", existing.Text)
	}

	// 'document' speaker should now be accepted.
	doc := &types.Claim{
		Text:      "post-migration doc claim",
		Basis:     types.BasisDefinition,
		SessionID: "s2",
		Project:   "test-project",
		Speaker:   types.SpeakerDocument,
	}
	if err := db.CreateClaim(doc); err != nil {
		t.Fatalf("CreateClaim with speaker=document after migration: %v", err)
	}

	// 'convention' basis should also be accepted — migrateBasisCheck runs in
	// the same migration chain and widens basis CHECK to include it.
	conv := &types.Claim{
		Text:      "this project uses beads for issue tracking",
		Basis:     types.BasisConvention,
		SessionID: "s2",
		Project:   "test-project",
		Speaker:   types.SpeakerDocument,
	}
	if err := db.CreateClaim(conv); err != nil {
		t.Fatalf("CreateClaim with basis=convention after migration: %v", err)
	}
}

func TestExtractionCacheRoundtrip(t *testing.T) {
	db := testDB(t)

	if _, ok := db.GetExtractionCache("hash1", "modelA", 1); ok {
		t.Error("empty cache should miss")
	}

	if err := db.SetExtractionCache("hash1", "modelA", 1, `{"claims":[]}`); err != nil {
		t.Fatalf("SetExtractionCache: %v", err)
	}
	got, ok := db.GetExtractionCache("hash1", "modelA", 1)
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if got != `{"claims":[]}` {
		t.Errorf("cached JSON = %q", got)
	}

	// Different model / prompt version should miss.
	if _, ok := db.GetExtractionCache("hash1", "modelB", 1); ok {
		t.Error("cache should miss on different model")
	}
	if _, ok := db.GetExtractionCache("hash1", "modelA", 2); ok {
		t.Error("cache should miss on different prompt version")
	}

	// Overwrite works (ON CONFLICT UPDATE).
	if err := db.SetExtractionCache("hash1", "modelA", 1, `{"claims":[{"text":"updated"}]}`); err != nil {
		t.Fatalf("SetExtractionCache overwrite: %v", err)
	}
	got, _ = db.GetExtractionCache("hash1", "modelA", 1)
	if got != `{"claims":[{"text":"updated"}]}` {
		t.Errorf("cache did not overwrite: %q", got)
	}

	// Delete removes the row; subsequent get misses.
	if err := db.DeleteExtractionCache("hash1", "modelA", 1); err != nil {
		t.Fatalf("DeleteExtractionCache: %v", err)
	}
	if _, ok := db.GetExtractionCache("hash1", "modelA", 1); ok {
		t.Error("cache should miss after delete")
	}
}

func TestHookFireLogRoundtrip(t *testing.T) {
	db := testDB(t)

	if err := db.LogHookFire("test-project", "claim-a", "load_bearing_vibes"); err != nil {
		t.Fatalf("LogHookFire: %v", err)
	}
	if err := db.LogHookFire("test-project", "claim-b", "unchallenged_chain"); err != nil {
		t.Fatalf("LogHookFire b: %v", err)
	}
	if err := db.LogHookFire("other-project", "claim-a", "load_bearing_vibes"); err != nil {
		t.Fatalf("LogHookFire other: %v", err)
	}

	// All three logged — ask for recent within the last hour, project filter
	// excludes the "other-project" entry.
	fires, err := db.RecentHookFires("test-project", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("RecentHookFires: %v", err)
	}
	if !fires["claim-a|load_bearing_vibes"] {
		t.Error("missing claim-a|load_bearing_vibes")
	}
	if !fires["claim-b|unchallenged_chain"] {
		t.Error("missing claim-b|unchallenged_chain")
	}
	if fires["claim-a|load_bearing_vibes"] && len(fires) > 2 {
		t.Errorf("cross-project leak: expected 2 entries, got %d", len(fires))
	}

	// Old-cutoff query returns nothing.
	fires, _ = db.RecentHookFires("test-project", time.Now().Add(time.Hour))
	if len(fires) != 0 {
		t.Errorf("future-cutoff should return 0 fires, got %d", len(fires))
	}
}

// TestEdgeRelationCheckMigration verifies that an existing DB created with
// the old edges.relation CHECK constraint (pre-'questions') is rebuilt on
// Open, data is preserved, and the new constraint accepts 'questions'.
func TestEdgeRelationCheckMigration(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "test-project")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(projectDir, "graph.sqlite")
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

	// Create a DB with the OLD edges schema — CHECK only allows the three
	// original relation values.
	{
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("raw open: %v", err)
		}
		// Need claims too for the FK.
		oldClaimsSchema := `CREATE TABLE claims (
			id          TEXT PRIMARY KEY,
			text        TEXT NOT NULL,
			basis       TEXT NOT NULL CHECK(basis IN ('research','empirical','analogy','vibes','llm_output','deduction','assumption','definition','convention')),
			confidence  REAL DEFAULT 0.5,
			source      TEXT DEFAULT '',
			session_id  TEXT NOT NULL,
			project     TEXT NOT NULL,
			turn_number INTEGER DEFAULT 0,
			speaker     TEXT DEFAULT 'user' CHECK(speaker IN ('user','assistant','document')),
			created_at  TEXT NOT NULL,
			challenged  INTEGER DEFAULT 0,
			verified    INTEGER DEFAULT 0,
			terminates_inquiry INTEGER DEFAULT 0
		)`
		oldEdgesSchema := `CREATE TABLE edges (
			id          TEXT PRIMARY KEY,
			from_id     TEXT NOT NULL REFERENCES claims(id),
			to_id       TEXT NOT NULL REFERENCES claims(id),
			relation    TEXT NOT NULL CHECK(relation IN ('supports','depends_on','contradicts')),
			strength    REAL DEFAULT 1.0,
			created_at  TEXT NOT NULL
		)`
		for _, stmt := range []string{oldClaimsSchema, oldEdgesSchema} {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("old schema: %v", err)
			}
		}
		// Seed a claim + edge.
		now := time.Now().Format(time.RFC3339)
		if _, err := raw.Exec(`INSERT INTO claims (id, text, basis, session_id, project, speaker, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"c1", "claim one", "vibes", "s1", "test-project", "user", now); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
		if _, err := raw.Exec(`INSERT INTO claims (id, text, basis, session_id, project, speaker, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"c2", "claim two", "vibes", "s1", "test-project", "user", now); err != nil {
			t.Fatalf("seed claim 2: %v", err)
		}
		if _, err := raw.Exec(`INSERT INTO edges (id, from_id, to_id, relation, created_at) VALUES (?, ?, ?, ?, ?)`,
			"e1", "c1", "c2", "supports", now); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
		// Confirm the old constraint rejects 'questions'.
		_, err = raw.Exec(`INSERT INTO edges (id, from_id, to_id, relation, created_at) VALUES (?, ?, ?, ?, ?)`,
			"e2", "c1", "c2", "questions", now)
		if err == nil {
			t.Fatal("expected old edges schema to reject relation='questions'")
		}
		raw.Close()
	}

	// Open via the store — migrations run, including migrateEdgeRelationCheck.
	db, err := Open(dir, "test-project")
	if err != nil {
		t.Fatalf("Open after edge migration: %v", err)
	}
	defer db.Close()

	// Preserved edge must still be there.
	edges, err := db.GetEdgesByProject("test-project")
	if err != nil {
		t.Fatalf("GetEdgesByProject: %v", err)
	}
	var preserved bool
	for _, e := range edges {
		if e.ID == "e1" && e.Relation == types.RelSupports {
			preserved = true
		}
	}
	if !preserved {
		t.Error("seeded edge lost during migration")
	}

	// 'questions' relation should now be accepted.
	if _, err := db.CreateEdge(&types.Edge{FromID: "c1", ToID: "c2", Relation: types.RelQuestions}); err != nil {
		t.Fatalf("CreateEdge with relation=questions after migration: %v", err)
	}
}
