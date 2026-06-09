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
const claimColumns = `id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified, terminates_inquiry, closed, grand_significance, unique_connection, dismisses_counterevidence, ability_overstatement, sentience_claim, relational_drift, consequential_action, last_referenced_at, archived`

// claimColumnsPrefixed is claimColumns with each name qualified with the alias
// "c." — used in JOINs against session_claims/edges where column names need to
// be disambiguated.
const claimColumnsPrefixed = `c.id, c.text, c.basis, c.confidence, c.source, c.session_id, c.project, c.turn_number, c.speaker, c.created_at, c.challenged, c.verified, c.terminates_inquiry, c.closed, c.grand_significance, c.unique_connection, c.dismisses_counterevidence, c.ability_overstatement, c.sentience_claim, c.relational_drift, c.consequential_action, c.last_referenced_at, c.archived`

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

	if c.LastReferencedAt.IsZero() {
		c.LastReferencedAt = c.CreatedAt
	}

	_, err := d.q.Exec(`
		INSERT INTO claims (`+claimColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Text, string(c.Basis), c.Confidence, c.Source,
		c.SessionID, c.Project, c.TurnNumber, string(c.Speaker),
		c.CreatedAt.Format(time.RFC3339), boolToInt(c.Challenged), boolToInt(c.Verified),
		boolToInt(c.TerminatesInquiry), boolToInt(c.Closed),
		boolToInt(c.GrandSignificance), boolToInt(c.UniqueConnection),
		boolToInt(c.DismissesCounterevidence), boolToInt(c.AbilityOverstatement),
		boolToInt(c.SentienceClaim), boolToInt(c.RelationalDrift),
		boolToInt(c.ConsequentialAction),
		c.LastReferencedAt.Format(time.RFC3339),
		boolToInt(c.Archived),
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
	if n > 0 {
		// New edge → both endpoints are "active" right now. Touch their
		// last_referenced_at so the legacy_load_bearer detector and the
		// archival sweep see the recent reference. Errors swallowed —
		// the edge insert is the primary write; bookkeeping failure
		// shouldn't propagate.
		now := e.CreatedAt.Format(time.RFC3339)
		_, _ = d.q.Exec(`UPDATE claims SET last_referenced_at = ? WHERE id IN (?, ?) AND (last_referenced_at IS NULL OR last_referenced_at < ?)`,
			now, e.FromID, e.ToID, now)
	}
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

// GetClaimsByProject retrieves all non-archived claims for a project. The
// archived filter is applied by default — Analyze, viz, audit, hook findings
// all see only the active working set. Use GetClaimsByProjectAll when you
// need every row (sweep diagnostics, manual unarchive, schema migrations).
//
// The `archived = 0` filter is sargable and uses idx_claims_archived
// (project, archived); migrateArchivedFlag backfills NULL → 0 so this is
// safe without COALESCE.
func (d *DB) GetClaimsByProject(project string) ([]types.Claim, error) {
	rows, err := d.q.Query(`SELECT `+claimColumns+` FROM claims WHERE project = ? AND archived = 0 ORDER BY created_at`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// GetClaimsByProjectAll retrieves every claim for a project including
// archived rows. Used by the sweep CLI (so dry-run reports surface
// already-archived counts) and unarchive paths.
func (d *DB) GetClaimsByProjectAll(project string) ([]types.Claim, error) {
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

// GetClaimsBySession retrieves all non-archived claims associated with a
// session via the session_claims membership table. This includes claims
// that were recognized via cross-batch dedup (which keep a prior session's
// session_id on the claim row but are still logically part of the current
// session).
//
// The archived filter matches GetClaimsByProject's semantics so the
// session-scoped hook path doesn't see claims the global view hides — if
// it did, edges-filtered-by-session would reference claims missing from
// GetClaimsByProject's set, producing spurious orphan/dangling findings.
func (d *DB) GetClaimsBySession(sessionID string) ([]types.Claim, error) {
	rows, err := d.q.Query(`
		SELECT `+claimColumnsPrefixed+`
		FROM claims c
		JOIN session_claims sc ON sc.claim_id = c.id
		WHERE sc.session_id = ?
		  AND c.archived = 0
		ORDER BY c.created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// GetEdgesByProject retrieves all edges for non-archived claims in a project.
// Edges where either endpoint is archived are excluded — keeping them would
// leave dangling references in the adjacency maps that consume this slice
// (findOrphans, findClusters, buildAdjacency would all see endpoints with no
// matching claim).
//
// Edges with endpoints in different projects are also excluded (no project
// cross-edges exist today, but the two-project predicate makes that explicit).
// DISTINCT was removed in favor of the natural unique-per-edge join: cf.id
// and ct.id are PKs, so each edge row matches exactly one (cf, ct) pair —
// the sort cost of DISTINCT was measurable (~190ms on lucida-class graphs).
// archived = 0 (not COALESCE) is sargable; migrateArchivedFlag backfills NULL.
func (d *DB) GetEdgesByProject(project string) ([]types.Edge, error) {
	rows, err := d.q.Query(`
		SELECT e.id, e.from_id, e.to_id, e.relation, e.strength, e.created_at
		FROM edges e
		JOIN claims cf ON e.from_id = cf.id
		JOIN claims ct ON e.to_id = ct.id
		WHERE cf.project = ? AND ct.project = ?
		  AND cf.archived = 0
		  AND ct.archived = 0
		ORDER BY e.created_at`, project, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanEdges(rows)
}

// SearchClaims does a case-insensitive text search across non-archived
// claims. Archived rows are excluded so search results match the active
// working set users see in viz / audit / hook findings — searching for
// claims that have been swept would return rows the user can't act on
// anywhere else.
func (d *DB) SearchClaims(project, query string, basis string) ([]types.Claim, error) {
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(query)
	q := `SELECT ` + claimColumns + `
		FROM claims WHERE project = ? AND archived = 0 AND text LIKE ? ESCAPE '\'`
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

// FindClaimByText finds the best matching non-archived claim by normalized
// text. The archived filter is essential here — resolveEdgeByText
// (mcp/core.go) uses this to wire user-requested edges by text, and
// wiring an edge to an archived target produces an edge that's invisible
// in GetEdgesByProject (which excludes edges with archived endpoints).
func (d *DB) FindClaimByText(project, text string) (*types.Claim, error) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	row := d.q.QueryRow(`
		SELECT `+claimColumns+`
		FROM claims WHERE project = ? AND archived = 0 AND LOWER(text) = ?
		LIMIT 1`, project, normalized)
	c, err := scanClaim(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// FindClaimBySubstring finds non-archived claims containing the given text.
// Archived filter applied for the same reason as FindClaimByText — its
// primary caller (resolveEdgeByText) shouldn't wire edges to swept rows.
func (d *DB) FindClaimBySubstring(project, text string) ([]types.Claim, error) {
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(strings.ToLower(strings.TrimSpace(text)))
	normalized := "%" + escaped + "%"
	rows, err := d.q.Query(`
		SELECT `+claimColumns+`
		FROM claims WHERE project = ? AND archived = 0 AND LOWER(text) LIKE ? ESCAPE '\'
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

// ArchiveClaims sets archived=1 on the provided claim IDs scoped to the
// given project (the project filter is defensive — prevents an accidental
// archive across projects if the caller passes IDs from elsewhere). Returns
// the number of rows updated. Idempotent: re-archiving an archived claim
// is a no-op.
func (d *DB) ArchiveClaims(project string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, project)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := d.q.Exec(`UPDATE claims SET archived = 1 WHERE project = ? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UnarchiveClaims clears the archived flag on the provided claim IDs and
// bumps last_referenced_at to now. Used by `slimemold unarchive` to recover
// from false-positive archives and by CoreParseTranscript/ingestOneChunk
// when a paraphrased re-assertion resurrects a swept claim.
//
// The last_referenced_at touch is load-bearing: without it, an unarchived
// claim with an old created_at would immediately re-qualify for the next
// sweep (idle >= 30d still holds), forcing the user to keep unarchiving
// the same claim every fire cycle. The touch makes "unarchive" mean "active
// again now" — consistent with how a fresh edge would touch a claim.
//
// If ids is empty, this is a no-op; pass UnarchiveAll for "everything in
// this project."
func (d *DB) UnarchiveClaims(project string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	now := time.Now().Format(time.RFC3339)
	args := make([]any, 0, len(ids)+2)
	args = append(args, now, project)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := d.q.Exec(`UPDATE claims SET archived = 0, last_referenced_at = ? WHERE project = ? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UnarchiveAll clears the archived flag on every archived claim in a project
// and bumps last_referenced_at to now (same rationale as UnarchiveClaims —
// see that doc comment). Recovery hatch for "sweep was too aggressive, give
// me everything back."
func (d *DB) UnarchiveAll(project string) (int64, error) {
	now := time.Now().Format(time.RFC3339)
	res, err := d.q.Exec(`UPDATE claims SET archived = 0, last_referenced_at = ? WHERE project = ? AND archived = 1`, now, project)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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

// CountClaims returns the number of *active* (non-archived) claims in a
// project. Matches the semantics of GetClaimsByProject — when callers
// report "graph size" they want the working set, not raw row count.
// Use CountClaimsAll for the unfiltered count.
func (d *DB) CountClaims(project string) (int, error) {
	var n int
	err := d.q.QueryRow(`SELECT COUNT(*) FROM claims WHERE project = ? AND archived = 0`, project).Scan(&n)
	return n, err
}

// CountClaimsAll returns the number of claims in a project including
// archived ones. Used by sweep diagnostics and tests that need the raw count.
func (d *DB) CountClaimsAll(project string) (int, error) {
	var n int
	err := d.q.QueryRow(`SELECT COUNT(*) FROM claims WHERE project = ?`, project).Scan(&n)
	return n, err
}

// CountEdges returns the number of edges with both endpoints active and in
// the given project. Matches GetEdgesByProject semantics (archived endpoints
// excluded). DISTINCT removed — the cf/ct joins on PKs are 1:1 per edge.
func (d *DB) CountEdges(project string) (int, error) {
	var n int
	err := d.q.QueryRow(`
		SELECT COUNT(*) FROM edges e
		JOIN claims cf ON e.from_id = cf.id
		JOIN claims ct ON e.to_id = ct.id
		WHERE cf.project = ? AND ct.project = ?
		  AND cf.archived = 0
		  AND ct.archived = 0`, project, project).Scan(&n)
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
	var lastReferencedAt sql.NullString
	var archived sql.NullInt64
	err := s.Scan(&c.ID, &c.Text, &basis, &c.Confidence, &c.Source,
		&c.SessionID, &c.Project, &c.TurnNumber, &speaker, &createdAt,
		&challenged, &verified, &terminatesInquiry, &closed,
		&grandSig, &uniqueConn, &dismissesCE, &abilityOver, &sentience, &relational,
		&consequential, &lastReferencedAt, &archived)
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
	// Fallback to CreatedAt if the column is null/empty (pre-backfill rows
	// or partially-migrated DBs). Same semantics: "claim has never been
	// referenced since creation" == "last referenced at creation time."
	if lastReferencedAt.Valid && lastReferencedAt.String != "" {
		c.LastReferencedAt, _ = time.Parse(time.RFC3339, lastReferencedAt.String)
	}
	if c.LastReferencedAt.IsZero() {
		c.LastReferencedAt = c.CreatedAt
	}
	c.Archived = archived.Valid && archived.Int64 != 0
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
	migrateLastReferencedAt(db)
	migrateArchivedFlag(db)
}

// migrateArchivedFlag adds the archived column to claims for older DBs. New
// installs already have it from schema.sql; this ALTER is a no-op (and
// errors out silently) on those. The index supports the archived=0 filter
// in GetClaimsByProject without scanning the full table.
func migrateArchivedFlag(db *sql.DB) {
	_, _ = db.Exec(`ALTER TABLE claims ADD COLUMN archived INTEGER DEFAULT 0`)
	// Defensive backfill: ALTER ADD COLUMN ... DEFAULT 0 populates 0 for
	// existing rows in modern SQLite (verified empirically with modernc.org/
	// sqlite), so this UPDATE is a no-op on the happy path. Kept anyway so
	// queries can use `archived = 0` (sargable, index-friendly) instead of
	// `COALESCE(archived, 0) = 0` (which forces a scan on the second key of
	// idx_claims_archived). If a DB ever lands here with NULL archived
	// (interim version, foreign tool import), this guarantees the index is
	// usable from the next query forward.
	_, _ = db.Exec(`UPDATE claims SET archived = 0 WHERE archived IS NULL`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_claims_archived ON claims(project, archived)`)
}

// migrateLastReferencedAt adds the last_referenced_at column to claims and
// backfills it for existing rows. Used by the legacy-load-bearer detector
// and the archival sweep to distinguish "old and stale" from "old but still
// actively touched" — see benchmarks/perf/README.md for the rationale.
//
// Backfill semantics: last_referenced_at = MAX(created_at, latest edge
// created_at touching this claim). A naive backfill from created_at alone
// would over-count staleness on the first sweep — a 35-day-old claim with
// an edge added 10 days ago would appear "35 days idle." This backfill
// captures the pre-trigger edge history so the sweep starts honest.
//
// Idempotent: ALTER TABLE ADD COLUMN errors out if the column exists (we
// swallow the error); the UPDATE is gated on `last_referenced_at IS NULL OR
// last_referenced_at = ”` so it's a no-op once backfill has run. The
// post-backfill UPDATE refines stale rows that the naive backfill already
// set — gated on the column being equal to created_at AND a later edge
// existing — so an old DB upgraded from an interim version (where naive
// backfill already populated the column) still gets the correction.
func migrateLastReferencedAt(db *sql.DB) {
	_, _ = db.Exec(`ALTER TABLE claims ADD COLUMN last_referenced_at TEXT`)

	// Pass 1: any NULL/empty row gets a naive backfill from created_at as
	// a fallback. The next pass refines it using edge history.
	_, _ = db.Exec(`UPDATE claims SET last_referenced_at = created_at WHERE last_referenced_at IS NULL OR last_referenced_at = ''`)

	// Pass 2: refine using edges. For each claim, set last_referenced_at to
	// MAX(claim.created_at, MAX(edge.created_at) for edges touching this
	// claim). Only applies when the column currently equals created_at —
	// avoids clobbering values already updated by the trigger / CreateEdge
	// post-deploy. The SUBSELECT is fine for the one-shot migration; the
	// idx_claims_referenced index exists from the index creation below.
	_, _ = db.Exec(`
		UPDATE claims SET last_referenced_at = (
			SELECT COALESCE(MAX(e.created_at), claims.created_at)
			FROM edges e
			WHERE (e.from_id = claims.id OR e.to_id = claims.id)
			  AND e.created_at > claims.created_at
		)
		WHERE last_referenced_at = created_at
		  AND EXISTS (
			SELECT 1 FROM edges e
			WHERE (e.from_id = claims.id OR e.to_id = claims.id)
			  AND e.created_at > claims.created_at
		)
	`)

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_claims_referenced ON claims(project, last_referenced_at)`)
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
//
// MAINTAINER: when adding a new CHECK widening that triggers a rebuild,
// the new rebuild's INSERT must preserve EVERY current claim column,
// including those added by post-rebuild migrations:
//   - terminates_inquiry, closed (Phase 1)
//   - grand_significance, unique_connection, dismisses_counterevidence,
//     ability_overstatement, sentience_claim, relational_drift,
//     consequential_action (Phase 3 inventory)
//   - last_referenced_at (migrateLastReferencedAt — drives sweep + legacy_load_bearer)
//   - archived (migrateArchivedFlag — drives soft-archive)
//
// Rebuilds that ship without these columns will reset archived state and
// re-derive last_referenced_at from edges (which is approximate, not exact).
// The current oldSpeakerRebuild / oldBasisRebuild predate those columns
// intentionally — they're meant to fire only against pre-inventory DBs and
// rely on Phase 3 + the bookkeeping migrations to re-add columns afterward.
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
//
// MAINTAINER: same contract as oldSpeakerRebuild — see that var's docstring
// for the column-preservation requirements on any future rebuild added here.
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

// SanitizeProject strips path separators and unsafe characters from
// project names. Exported so packages that derive per-project state
// dirs (internal/verify, future internal/hookevents callers) use the
// same rule and land their files in the same directory as graph.sqlite
// — no split-brain layouts on disk if the sanitizer rule evolves.
func SanitizeProject(name string) string {
	name = filepath.Base(name) // strip directory components
	name = projectRe.ReplaceAllString(name, "")
	if name == "" {
		name = "default"
	}
	return name
}

// sanitizeProject is the internal lowercase alias kept for backward
// compatibility with the in-package call sites. New external callers
// should use SanitizeProject.
func sanitizeProject(name string) string { return SanitizeProject(name) }
