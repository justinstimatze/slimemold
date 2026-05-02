package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/justinstimatze/slimemold/types"
)

//go:embed schema.sql
var schema string

// claimColumns is the canonical column list used by every SELECT that returns
// a full claim row. Keep in sync with scanClaim, the CreateClaim INSERT, and
// schema.sql. Centralized so adding a column is a single-site change.
const claimColumns = `id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified, terminates_inquiry, closed, grand_significance, unique_connection, dismisses_counterevidence, ability_overstatement, sentience_claim, relational_drift, consequential_action`

// claimColumnsPrefixed is claimColumns with each name qualified with the alias
// "c." — used in JOINs against session_claims/edges where column names need to
// be disambiguated.
const claimColumnsPrefixed = `c.id, c.text, c.basis, c.confidence, c.source, c.session_id, c.project, c.turn_number, c.speaker, c.created_at, c.challenged, c.verified, c.terminates_inquiry, c.closed, c.grand_significance, c.unique_connection, c.dismisses_counterevidence, c.ability_overstatement, c.sentience_claim, c.relational_drift, c.consequential_action`

// querier abstracts the Exec/Query/QueryRow methods shared by *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DB wraps a SQLite connection for the claim graph.
type DB struct {
	db      *sql.DB // always the real connection (for Begin, Close)
	q       querier // either db or tx — used by all read/write methods
	project string
}

// Open opens (or creates) the SQLite database for a project.
// Project name is sanitized to prevent path traversal.
func Open(dataDir, project string) (*DB, error) {
	project = sanitizeProject(project)
	dir := filepath.Join(dataDir, project)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	dbPath := filepath.Join(dir, "graph.sqlite")
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite is single-writer; serialize access

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running schema: %w", err)
	}

	migrate(db)

	// Best-effort hygiene on hook_fire_log — delete rows older than 30 days
	// so the table doesn't grow unbounded across months of use. Cooldowns
	// that matter are measured in hours; anything older is dead weight.
	_, _ = db.Exec(`DELETE FROM hook_fire_log WHERE fired_at < ?`, time.Now().Add(-30*24*time.Hour).Format(time.RFC3339))

	return &DB{db: db, q: db, project: project}, nil
}

// Close checkpoints the WAL and closes the database.
// Must not be called on a transactional handle — call Commit or Rollback instead.
func (d *DB) Close() error {
	if _, ok := d.q.(*sql.Tx); ok {
		return fmt.Errorf("cannot close a transactional handle — call Commit or Rollback")
	}
	_, _ = d.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return d.db.Close()
}

// Begin starts a transaction and returns a new DB handle that executes all
// operations within that transaction. The caller must call Commit or Rollback.
func (d *DB) Begin() (*DB, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	return &DB{db: d.db, q: tx, project: d.project}, nil
}

// Commit commits the transaction. Only valid on a DB returned by Begin.
func (d *DB) Commit() error {
	tx, ok := d.q.(*sql.Tx)
	if !ok {
		return fmt.Errorf("not in a transaction")
	}
	return tx.Commit()
}

// Rollback aborts the transaction. Only valid on a DB returned by Begin.
// Safe to call after Commit (returns sql.ErrTxDone, which is harmless).
func (d *DB) Rollback() error {
	tx, ok := d.q.(*sql.Tx)
	if !ok {
		return nil
	}
	return tx.Rollback()
}

// inTx runs fn inside a transaction. If d is already transactional, fn runs
// on the existing transaction (no nested begin). Otherwise, a new transaction
// is created and committed/rolled back automatically.
func (d *DB) inTx(fn func(querier) error) error {
	if _, ok := d.q.(*sql.Tx); ok {
		return fn(d.q)
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateClaim inserts a new claim.
func (d *DB) CreateClaim(c *types.Claim) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	if c.Project == "" {
		c.Project = d.project
	}

	_, err := d.q.Exec(`
		INSERT INTO claims (`+claimColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Text, string(c.Basis), c.Confidence, c.Source,
		c.SessionID, c.Project, c.TurnNumber, string(c.Speaker),
		c.CreatedAt.Format(time.RFC3339), boolToInt(c.Challenged), boolToInt(c.Verified),
		boolToInt(c.TerminatesInquiry), boolToInt(c.Closed),
		boolToInt(c.GrandSignificance), boolToInt(c.UniqueConnection),
		boolToInt(c.DismissesCounterevidence), boolToInt(c.AbilityOverstatement),
		boolToInt(c.SentienceClaim), boolToInt(c.RelationalDrift),
		boolToInt(c.ConsequentialAction),
	)
	if err != nil {
		return fmt.Errorf("%w (basis=%q speaker=%q)", err, c.Basis, c.Speaker)
	}
	return nil
}

// CreateEdge inserts a new edge. Returns (true, nil) if inserted, (false, nil) if duplicate.
func (d *DB) CreateEdge(e *types.Edge) (bool, error) {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.Strength == 0 {
		e.Strength = 1.0
	}

	res, err := d.q.Exec(`
		INSERT OR IGNORE INTO edges (id, from_id, to_id, relation, strength, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.FromID, e.ToID, string(e.Relation), e.Strength,
		e.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteEdge removes an edge by ID.
func (d *DB) DeleteEdge(id string) error {
	_, err := d.q.Exec(`DELETE FROM edges WHERE id = ?`, id)
	return err
}

// GetClaim retrieves a single claim by ID.
func (d *DB) GetClaim(id string) (*types.Claim, error) {
	row := d.q.QueryRow(`SELECT `+claimColumns+` FROM claims WHERE id = ?`, id)
	return scanClaim(row)
}

// GetClaimsByProject retrieves all claims for a project.
func (d *DB) GetClaimsByProject(project string) ([]types.Claim, error) {
	rows, err := d.q.Query(`SELECT `+claimColumns+` FROM claims WHERE project = ? ORDER BY created_at`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// RecordSessionClaim records that a session has seen (or produced) a claim.
// Uses INSERT OR IGNORE so it's safe to call for both new claims and dedup matches.
func (d *DB) RecordSessionClaim(sessionID, claimID string) error {
	_, err := d.q.Exec(`INSERT OR IGNORE INTO session_claims (session_id, claim_id) VALUES (?, ?)`, sessionID, claimID)
	return err
}

// GetClaimsBySession retrieves all claims associated with a session via
// the session_claims membership table. This includes claims that were
// recognized via cross-batch dedup (which keep a prior session's session_id
// on the claim row but are still logically part of the current session).
func (d *DB) GetClaimsBySession(sessionID string) ([]types.Claim, error) {
	rows, err := d.q.Query(`
		SELECT `+claimColumnsPrefixed+`
		FROM claims c
		JOIN session_claims sc ON sc.claim_id = c.id
		WHERE sc.session_id = ?
		ORDER BY c.created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// GetEdgesByProject retrieves all edges for claims in a project.
func (d *DB) GetEdgesByProject(project string) ([]types.Edge, error) {
	rows, err := d.q.Query(`
		SELECT DISTINCT e.id, e.from_id, e.to_id, e.relation, e.strength, e.created_at
		FROM edges e
		JOIN claims c ON (e.from_id = c.id OR e.to_id = c.id)
		WHERE c.project = ?
		ORDER BY e.created_at`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEdges(rows)
}

// SearchClaims does a case-insensitive text search across claims.
func (d *DB) SearchClaims(project, query string, basis string) ([]types.Claim, error) {
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(query)
	q := `SELECT ` + claimColumns + `
		FROM claims WHERE project = ? AND text LIKE ? ESCAPE '\'`
	args := []any{project, "%" + escaped + "%"}

	if basis != "" {
		q += " AND basis = ?"
		args = append(args, basis)
	}
	q += " ORDER BY created_at"

	rows, err := d.q.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// FindClaimByText finds the best matching claim by normalized text.
func (d *DB) FindClaimByText(project, text string) (*types.Claim, error) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	row := d.q.QueryRow(`
		SELECT `+claimColumns+`
		FROM claims WHERE project = ? AND LOWER(text) = ?
		LIMIT 1`, project, normalized)
	c, err := scanClaim(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// FindClaimBySubstring finds claims containing the given text.
func (d *DB) FindClaimBySubstring(project, text string) ([]types.Claim, error) {
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(strings.ToLower(strings.TrimSpace(text)))
	normalized := "%" + escaped + "%"
	rows, err := d.q.Query(`
		SELECT `+claimColumns+`
		FROM claims WHERE project = ? AND LOWER(text) LIKE ? ESCAPE '\'
		ORDER BY created_at`, project, normalized)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// ChallengeClaim marks a claim as challenged and optionally revises it.
func (d *DB) ChallengeClaim(id, result, revisedText, revisedBasis, notes string) error {
	return d.inTx(func(q querier) error {
		if _, err := q.Exec(`UPDATE claims SET challenged = 1 WHERE id = ?`, id); err != nil {
			return err
		}
		if revisedText != "" {
			if _, err := q.Exec(`UPDATE claims SET text = ? WHERE id = ?`, revisedText, id); err != nil {
				return err
			}
		}
		if revisedBasis != "" {
			if _, err := q.Exec(`UPDATE claims SET basis = ? WHERE id = ?`, revisedBasis, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// CloseClaim marks a claim as permanently closed so it is excluded from future
// hook findings. Unlike ChallengeClaim (which records an epistemic event), Close
// is a maintenance action: the claim was about transient state that is now resolved.
func (d *DB) CloseClaim(id string) error {
	_, err := d.q.Exec(`UPDATE claims SET closed = 1 WHERE id = ?`, id)
	return err
}

// MergeClaims absorbs one claim into another, redirecting all edges.
func (d *DB) MergeClaims(keepID, absorbID string) error {
	return d.inTx(func(q querier) error {
		// Redirect edges pointing to/from absorbID, ignoring conflicts with existing edges
		if _, err := q.Exec(`UPDATE OR IGNORE edges SET from_id = ? WHERE from_id = ?`, keepID, absorbID); err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE OR IGNORE edges SET to_id = ? WHERE to_id = ?`, keepID, absorbID); err != nil {
			return err
		}
		// Remove any edges still referencing absorbID (duplicates that conflicted)
		if _, err := q.Exec(`DELETE FROM edges WHERE from_id = ? OR to_id = ?`, absorbID, absorbID); err != nil {
			return err
		}
		// Remove self-loops created by the merge (scoped to keepID only)
		if _, err := q.Exec(`DELETE FROM edges WHERE from_id = ? AND to_id = ?`, keepID, keepID); err != nil {
			return err
		}
		// Delete absorbed claim
		if _, err := q.Exec(`DELETE FROM claims WHERE id = ?`, absorbID); err != nil {
			return err
		}
		return nil
	})
}

// CountClaims returns the number of claims in a project.
func (d *DB) CountClaims(project string) (int, error) {
	var n int
	err := d.q.QueryRow(`SELECT COUNT(*) FROM claims WHERE project = ?`, project).Scan(&n)
	return n, err
}

// CountEdges returns the number of edges for claims in a project.
func (d *DB) CountEdges(project string) (int, error) {
	var n int
	err := d.q.QueryRow(`
		SELECT COUNT(DISTINCT e.id) FROM edges e JOIN claims c ON (e.from_id = c.id OR e.to_id = c.id) WHERE c.project = ?`, project).Scan(&n)
	return n, err
}

// CreateAudit inserts an audit record.
func (d *DB) CreateAudit(a *types.Audit) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	if a.Timestamp.IsZero() {
		a.Timestamp = time.Now()
	}

	_, err := d.q.Exec(`
		INSERT INTO audits (id, project, session_id, timestamp, findings, claim_count, edge_count, critical_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Project, a.SessionID, a.Timestamp.Format(time.RFC3339),
		a.Findings, a.ClaimCount, a.EdgeCount, a.CriticalCount,
	)
	return err
}

// DeleteProject removes all data for a project.
func (d *DB) DeleteProject(project string) error {
	return d.inTx(func(q querier) error {
		if _, err := q.Exec(`DELETE FROM edges WHERE from_id IN (SELECT id FROM claims WHERE project = ?) OR to_id IN (SELECT id FROM claims WHERE project = ?)`, project, project); err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM claims WHERE project = ?`, project); err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM audits WHERE project = ?`, project); err != nil {
			return err
		}
		return nil
	})
}

// GetEdgesForClaim returns all edges where the claim is from_id or to_id.
func (d *DB) GetEdgesForClaim(claimID string) ([]types.Edge, error) {
	rows, err := d.q.Query(`
		SELECT id, from_id, to_id, relation, strength, created_at
		FROM edges WHERE from_id = ? OR to_id = ?
		ORDER BY created_at`, claimID, claimID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEdges(rows)
}

// Helpers

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scannable interface {
	Scan(dest ...any) error
}

func scanClaim(s scannable) (*types.Claim, error) {
	var c types.Claim
	var basis, speaker, createdAt string
	var challenged, verified, terminatesInquiry, closed int
	var grandSig, uniqueConn, dismissesCE, abilityOver, sentience, relational, consequential int
	err := s.Scan(&c.ID, &c.Text, &basis, &c.Confidence, &c.Source,
		&c.SessionID, &c.Project, &c.TurnNumber, &speaker, &createdAt,
		&challenged, &verified, &terminatesInquiry, &closed,
		&grandSig, &uniqueConn, &dismissesCE, &abilityOver, &sentience, &relational,
		&consequential)
	if err != nil {
		return nil, err
	}
	c.Basis = types.Basis(basis)
	c.Speaker = types.Speaker(speaker)
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	c.Challenged = challenged != 0
	c.Verified = verified != 0
	c.TerminatesInquiry = terminatesInquiry != 0
	c.Closed = closed != 0
	c.GrandSignificance = grandSig != 0
	c.UniqueConnection = uniqueConn != 0
	c.DismissesCounterevidence = dismissesCE != 0
	c.AbilityOverstatement = abilityOver != 0
	c.SentienceClaim = sentience != 0
	c.RelationalDrift = relational != 0
	c.ConsequentialAction = consequential != 0
	return &c, nil
}

func scanClaims(rows *sql.Rows) ([]types.Claim, error) {
	var claims []types.Claim
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		claims = append(claims, *c)
	}
	return claims, rows.Err()
}

func scanEdges(rows *sql.Rows) ([]types.Edge, error) {
	var edges []types.Edge
	for rows.Next() {
		var e types.Edge
		var relation, createdAt string
		if err := rows.Scan(&e.ID, &e.FromID, &e.ToID, &relation, &e.Strength, &createdAt); err != nil {
			return nil, err
		}
		e.Relation = types.Relation(relation)
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// GetExtractionCache returns a cached extraction result if one exists for this
// (content_hash, model, prompt_version) tuple. Returns ("", false) on miss.
func (d *DB) GetExtractionCache(contentHash, model string, promptVersion int) (string, bool) {
	var result string
	err := d.q.QueryRow(
		`SELECT result_json FROM extract_cache WHERE content_hash = ? AND model = ? AND prompt_version = ?`,
		contentHash, model, promptVersion,
	).Scan(&result)
	if err != nil {
		return "", false
	}
	return result, true
}

// SetExtractionCache stores an extraction result. Overwrites on conflict.
func (d *DB) SetExtractionCache(contentHash, model string, promptVersion int, resultJSON string) error {
	_, err := d.q.Exec(
		`INSERT INTO extract_cache (content_hash, model, prompt_version, result_json, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(content_hash, model, prompt_version) DO UPDATE SET result_json = excluded.result_json, created_at = excluded.created_at`,
		contentHash, model, promptVersion, resultJSON, time.Now().Format(time.RFC3339),
	)
	return err
}

// DeleteExtractionCache removes a single cache row. Used to prune entries
// whose stored JSON fails to unmarshal (corruption from truncated writes or
// prior schema mismatches) so the next call re-extracts cleanly.
func (d *DB) DeleteExtractionCache(contentHash, model string, promptVersion int) error {
	_, err := d.q.Exec(
		`DELETE FROM extract_cache WHERE content_hash = ? AND model = ? AND prompt_version = ?`,
		contentHash, model, promptVersion,
	)
	return err
}

// LogHookFire records that the hook surfaced `findingType` for `claimID` in
// `project`. Used by RecentHookFires to suppress repeat firings inside a
// cooldown window.
func (d *DB) LogHookFire(project, claimID, findingType string) error {
	_, err := d.q.Exec(
		`INSERT INTO hook_fire_log (project, claim_id, finding_type, fired_at) VALUES (?, ?, ?, ?)`,
		project, claimID, findingType, time.Now().Format(time.RFC3339),
	)
	return err
}

// CloseSupersededClaims auto-closes any claim in `project` that has been
// contradicted by a newer claim, regardless of which session each claim came
// from. Extends filterSuperseded's within-session behavior to the whole
// cross-session graph: when the extractor produces a `contradicts` edge from
// a newer claim to an older one (e.g. "X is now implemented" contradicting
// "X is missing"), the older claim is retired permanently rather than just
// for the current session's findings.
//
// Returns the number of claims newly closed by this call. Idempotent —
// re-running has no effect on already-closed claims.
//
// Scope of the cull: this only addresses "explicit resolution via
// contradicts edge from a newer claim." It does not address other staleness
// signals (no recent activity, stress-tested-through-use, etc.) — those
// belong in detectors' surface filtering rather than DB-level pruning.
func (d *DB) CloseSupersededClaims(project string) (int, error) {
	res, err := d.q.Exec(`
		UPDATE claims SET closed = 1
		WHERE project = ?
		  AND closed = 0
		  AND id IN (
			SELECT old.id FROM claims old
			JOIN edges e ON e.to_id = old.id AND e.relation = 'contradicts'
			JOIN claims new ON new.id = e.from_id
			WHERE old.project = ?
			  AND new.project = ?
			  AND datetime(new.created_at) > datetime(old.created_at)
		)`, project, project, project)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RecentHookFires returns the set of (claim_id, finding_type) tuples that
// have fired in `project` since `since`. Keys are encoded "claim_id|type".
//
// Deprecated: use RecentHookFireTimes for differential cooldown support;
// this boolean variant kept for any callers that don't need timestamps.
func (d *DB) RecentHookFires(project string, since time.Time) (map[string]bool, error) {
	rows, err := d.q.Query(
		`SELECT claim_id, finding_type FROM hook_fire_log WHERE project = ? AND fired_at >= ?`,
		project, since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var claimID, findingType string
		if err := rows.Scan(&claimID, &findingType); err != nil {
			return nil, err
		}
		out[claimID+"|"+findingType] = true
	}
	return out, rows.Err()
}

// RecentHookFireTimes returns the latest fire timestamp per (claim_id,
// finding_type) tuple in `project` since `since`. Keys are "claim_id|type".
// Used by FormatHookFindings to apply differential cooldown — persistent-only
// findings have a longer effective window than recent-activity findings.
//
// Callers should pass `since = now - HookPersistentCooldown` (the longest
// possible cooldown) so every potentially-suppressed fire is visible; the
// cooldown threshold is then applied per-candidate based on its
// FiredViaPersistent flag.
func (d *DB) RecentHookFireTimes(project string, since time.Time) (map[string]time.Time, error) {
	rows, err := d.q.Query(
		`SELECT claim_id, finding_type, MAX(fired_at) FROM hook_fire_log WHERE project = ? AND fired_at >= ? GROUP BY claim_id, finding_type`,
		project, since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]time.Time)
	for rows.Next() {
		var claimID, findingType, firedAtStr string
		if err := rows.Scan(&claimID, &findingType, &firedAtStr); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, firedAtStr)
		if err != nil {
			// Malformed timestamp in hook_fire_log makes this row invisible
			// to the cooldown — meaning the next fire on this (claim, type)
			// won't be suppressed even if it should be. Log so it's not
			// silent if it ever happens; we still skip the row because we
			// can't apply a cooldown without a valid timestamp.
			fmt.Fprintf(os.Stderr, "slimemold: hook_fire_log row %q|%q has malformed fired_at %q: %v\n", claimID, findingType, firedAtStr, err)
			continue
		}
		out[claimID+"|"+findingType] = t
	}
	return out, rows.Err()
}

// migrate applies incremental schema changes to existing databases.
// Each migration is idempotent — ALTER TABLE ADD COLUMN is ignored if the
// column exists; CHECK rebuilds are gated on marker presence; the
// session_claims backfill is gated on row count.
//
// The three-phase ordering (legacy ALTERs → CHECK rebuilds → inventory
// ALTERs) is load-bearing: CHECK rebuilds use a hardcoded column list that
// predates inventory flags, so running inventory ALTERs first would let
// rebuilds silently drop them. The ordering and column-survival contract
// is exercised by TestInventoryFlagMigration in store_test.go (constructs a
// pre-document/pre-convention/pre-inventory schema and verifies all 20
// columns are present after Open()) and TestSpeakerCheckMigration (verifies
// the document-speaker rebuild path preserves seeded rows).
func migrate(db *sql.DB) {
	// Phase 1: legacy column additions. These MUST run before the
	// CHECK-constraint rebuilds because the rebuilds' INSERT statements
	// reference these columns by name (e.g. terminates_inquiry).
	legacyMigrations := []string{
		`ALTER TABLE claims ADD COLUMN terminates_inquiry INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN closed INTEGER DEFAULT 0`,
	}
	for _, m := range legacyMigrations {
		_, _ = db.Exec(m) // ignore "duplicate column name" errors
	}

	// Phase 2: CHECK-constraint rebuilds. Each is gated on a marker string in
	// the table's CREATE TABLE statement; if the marker is present the
	// rebuild is a no-op. So fresh installs and already-upgraded DBs skip
	// these entirely.
	migrateSpeakerCheck(db)
	migrateBasisCheck(db)
	migrateEdgeRelationCheck(db)

	// Phase 3: Moore et al. 2026 inventory flags + Yang et al. 2026
	// consequential_action. MUST run AFTER the CHECK rebuilds — those
	// rebuilds use a hardcoded column list (see oldSpeakerRebuild and
	// oldBasisRebuild below) and would silently drop these flags if they
	// ran later. Rebuilds are one-shot for old DBs; this ordering means
	// new columns survive both fresh installs and post-rebuild
	// reapplications.
	inventoryMigrations := []string{
		`ALTER TABLE claims ADD COLUMN grand_significance INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN unique_connection INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN dismisses_counterevidence INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN ability_overstatement INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN sentience_claim INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN relational_drift INTEGER DEFAULT 0`,
		`ALTER TABLE claims ADD COLUMN consequential_action INTEGER DEFAULT 0`,
	}
	for _, m := range inventoryMigrations {
		_, _ = db.Exec(m)
	}

	migrateSessionClaims(db)
}

// migrateSessionClaims backfills session_claims for existing databases.
// The table is created by schema.sql (CREATE TABLE IF NOT EXISTS), so it
// exists by the time this runs. If the table is empty but claims exist,
// we seed it from claims.session_id — this handles DBs written before the
// join table was introduced. Subsequent writes go through RecordSessionClaim.
func migrateSessionClaims(db *sql.DB) {
	var scCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_claims`).Scan(&scCount); err != nil || scCount > 0 {
		return
	}
	// Backfill: existing claims use their stored session_id as the sole session membership.
	_, _ = db.Exec(`INSERT OR IGNORE INTO session_claims (session_id, claim_id) SELECT session_id, id FROM claims`)
}

// migrateSpeakerCheck widens the claims.speaker CHECK constraint to include
// 'document'. SQLite can't ALTER a CHECK constraint in place, so we inspect
// sqlite_master and rebuild the table only if the constraint is outdated.
//
// The rebuild runs inside a transaction so a partial failure (disk full,
// process kill between DROP and RENAME) rolls back cleanly — without the
// transaction, an interrupted rebuild could leave the DB without a `claims`
// table at all. PRAGMA foreign_keys must be toggled outside the transaction
// (modernc/sqlite doesn't allow the pragma inside an open tx).
func migrateSpeakerCheck(db *sql.DB) {
	rebuildClaimsIfMissing(db, "'document'", oldSpeakerRebuild)
}

// migrateBasisCheck widens the claims.basis CHECK constraint to include
// 'convention'. Same pattern as migrateSpeakerCheck — inspect sqlite_master,
// rebuild only if the marker is absent. DBs that went through speakerCheck
// first will see their claims table rebuilt once more here; brand-new DBs
// created from schema.sql already have the marker and this is a no-op.
func migrateBasisCheck(db *sql.DB) {
	rebuildClaimsIfMissing(db, "'convention'", oldBasisRebuild)
}

// migrateEdgeRelationCheck widens the edges.relation CHECK constraint to
// include 'questions'. The edges table is structurally simpler than claims
// (no dependent columns), so the rebuild is inlined rather than sharing
// rebuildClaimsIfMissing.
func migrateEdgeRelationCheck(db *sql.DB) {
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='edges'`).Scan(&tableSQL); err != nil {
		return
	}
	if strings.Contains(tableSQL, "'questions'") {
		return
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) }()

	tx, err := db.Begin()
	if err != nil {
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmts := []string{
		`CREATE TABLE edges_new (
			id          TEXT PRIMARY KEY,
			from_id     TEXT NOT NULL REFERENCES claims(id),
			to_id       TEXT NOT NULL REFERENCES claims(id),
			relation    TEXT NOT NULL CHECK(relation IN (
				'supports','depends_on','contradicts','questions'
			)),
			strength    REAL DEFAULT 1.0,
			created_at  TEXT NOT NULL
		)`,
		`INSERT INTO edges_new SELECT id, from_id, to_id, relation, strength, created_at FROM edges`,
		`DROP TABLE edges`,
		`ALTER TABLE edges_new RENAME TO edges`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_edges_unique ON edges(from_id, to_id, relation)`,
		`CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id)`,
		`CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		return
	}
	committed = true
}

// rebuildClaimsIfMissing rebuilds the claims table using the provided DDL
// slice if the current CREATE TABLE statement doesn't contain `marker`.
func rebuildClaimsIfMissing(db *sql.DB, marker string, ddl []string) {
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='claims'`).Scan(&tableSQL); err != nil {
		return
	}
	if strings.Contains(tableSQL, marker) {
		return
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) }()

	tx, err := db.Begin()
	if err != nil {
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, stmt := range ddl {
		if _, err := tx.Exec(stmt); err != nil {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		return
	}
	committed = true
}

// oldSpeakerRebuild is the DDL used when migrating from the pre-document
// schema (speaker CHECK only allows user/assistant). This rebuild includes
// 'document' as an accepted speaker but uses the OLD basis list (without
// 'convention') because it may run against DBs written before the basis
// CHECK widened — migrateBasisCheck runs afterward if needed.
var oldSpeakerRebuild = []string{
	`CREATE TABLE claims_new (
		id          TEXT PRIMARY KEY,
		text        TEXT NOT NULL,
		basis       TEXT NOT NULL CHECK(basis IN (
			'research','empirical','analogy','vibes','llm_output',
			'deduction','assumption','definition'
		)),
		confidence  REAL DEFAULT 0.5 CHECK(confidence BETWEEN 0 AND 1),
		source      TEXT DEFAULT '',
		session_id  TEXT NOT NULL,
		project     TEXT NOT NULL,
		turn_number INTEGER DEFAULT 0,
		speaker     TEXT DEFAULT 'user' CHECK(speaker IN ('user','assistant','document')),
		created_at  TEXT NOT NULL,
		challenged  INTEGER DEFAULT 0,
		verified    INTEGER DEFAULT 0,
		terminates_inquiry INTEGER DEFAULT 0,
		closed      INTEGER DEFAULT 0
	)`,
	`INSERT INTO claims_new SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified, terminates_inquiry, 0 FROM claims`,
	`DROP TABLE claims`,
	`ALTER TABLE claims_new RENAME TO claims`,
	`CREATE INDEX IF NOT EXISTS idx_claims_project ON claims(project)`,
	`CREATE INDEX IF NOT EXISTS idx_claims_basis ON claims(basis)`,
	`CREATE INDEX IF NOT EXISTS idx_claims_text ON claims(text)`,
}

// oldBasisRebuild widens the basis CHECK to include 'convention'. Runs on
// DBs that already have 'document' in the speaker CHECK (i.e., previously
// went through migrateSpeakerCheck) but were created before 'convention'
// was added.
var oldBasisRebuild = []string{
	`CREATE TABLE claims_new (
		id          TEXT PRIMARY KEY,
		text        TEXT NOT NULL,
		basis       TEXT NOT NULL CHECK(basis IN (
			'research','empirical','analogy','vibes','llm_output',
			'deduction','assumption','definition','convention'
		)),
		confidence  REAL DEFAULT 0.5 CHECK(confidence BETWEEN 0 AND 1),
		source      TEXT DEFAULT '',
		session_id  TEXT NOT NULL,
		project     TEXT NOT NULL,
		turn_number INTEGER DEFAULT 0,
		speaker     TEXT DEFAULT 'user' CHECK(speaker IN ('user','assistant','document')),
		created_at  TEXT NOT NULL,
		challenged  INTEGER DEFAULT 0,
		verified    INTEGER DEFAULT 0,
		terminates_inquiry INTEGER DEFAULT 0,
		closed      INTEGER DEFAULT 0
	)`,
	`INSERT INTO claims_new SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified, terminates_inquiry, 0 FROM claims`,
	`DROP TABLE claims`,
	`ALTER TABLE claims_new RENAME TO claims`,
	`CREATE INDEX IF NOT EXISTS idx_claims_project ON claims(project)`,
	`CREATE INDEX IF NOT EXISTS idx_claims_basis ON claims(basis)`,
	`CREATE INDEX IF NOT EXISTS idx_claims_text ON claims(text)`,
}

var projectRe = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

// sanitizeProject strips path separators and unsafe characters from project names.
func sanitizeProject(name string) string {
	name = filepath.Base(name) // strip directory components
	name = projectRe.ReplaceAllString(name, "")
	if name == "" {
		name = "default"
	}
	return name
}
