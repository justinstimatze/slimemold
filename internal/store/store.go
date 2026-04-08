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
		INSERT INTO claims (id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Text, string(c.Basis), c.Confidence, c.Source,
		c.SessionID, c.Project, c.TurnNumber, string(c.Speaker),
		c.CreatedAt.Format(time.RFC3339), boolToInt(c.Challenged), boolToInt(c.Verified),
	)
	return err
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
	row := d.q.QueryRow(`SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified FROM claims WHERE id = ?`, id)
	return scanClaim(row)
}

// GetClaimsByProject retrieves all claims for a project.
func (d *DB) GetClaimsByProject(project string) ([]types.Claim, error) {
	rows, err := d.q.Query(`SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified FROM claims WHERE project = ? ORDER BY created_at`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanClaims(rows)
}

// GetEdgesByProject retrieves all edges for claims in a project.
func (d *DB) GetEdgesByProject(project string) ([]types.Edge, error) {
	rows, err := d.q.Query(`
		SELECT e.id, e.from_id, e.to_id, e.relation, e.strength, e.created_at
		FROM edges e
		JOIN claims c ON e.from_id = c.id
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
	q := `SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified
		FROM claims WHERE project = ? AND text LIKE ?`
	args := []any{project, "%" + query + "%"}

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
		SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified
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
	normalized := "%" + strings.ToLower(strings.TrimSpace(text)) + "%"
	rows, err := d.q.Query(`
		SELECT id, text, basis, confidence, source, session_id, project, turn_number, speaker, created_at, challenged, verified
		FROM claims WHERE project = ? AND LOWER(text) LIKE ?
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
		SELECT COUNT(*) FROM edges e JOIN claims c ON e.from_id = c.id WHERE c.project = ?`, project).Scan(&n)
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
		if _, err := q.Exec(`DELETE FROM edges WHERE from_id IN (SELECT id FROM claims WHERE project = ?)`, project); err != nil {
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
	var challenged, verified int
	err := s.Scan(&c.ID, &c.Text, &basis, &c.Confidence, &c.Source,
		&c.SessionID, &c.Project, &c.TurnNumber, &speaker, &createdAt,
		&challenged, &verified)
	if err != nil {
		return nil, err
	}
	c.Basis = types.Basis(basis)
	c.Speaker = types.Speaker(speaker)
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	c.Challenged = challenged != 0
	c.Verified = verified != 0
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

// migrate applies incremental schema changes to existing databases.
// Each migration is idempotent — ALTER TABLE ADD COLUMN is ignored if the column exists.
func migrate(db *sql.DB) {
	migrations := []string{
		// Future migrations go here. Example:
		// `ALTER TABLE claims ADD COLUMN challenge_result TEXT DEFAULT ''`,
	}
	for _, m := range migrations {
		_, _ = db.Exec(m) // ignore "duplicate column name" errors
	}
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
