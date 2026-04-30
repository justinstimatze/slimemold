CREATE TABLE IF NOT EXISTS claims (
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
    closed      INTEGER DEFAULT 0,
    -- Moore et al. 2026 inventory flags (sycophancy / misrepresentation /
    -- relational drift). See types.Claim docs for the codebook reference.
    grand_significance        INTEGER DEFAULT 0,
    unique_connection         INTEGER DEFAULT 0,
    dismisses_counterevidence INTEGER DEFAULT 0,
    ability_overstatement     INTEGER DEFAULT 0,
    sentience_claim           INTEGER DEFAULT 0,
    relational_drift          INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS edges (
    id          TEXT PRIMARY KEY,
    from_id     TEXT NOT NULL REFERENCES claims(id),
    to_id       TEXT NOT NULL REFERENCES claims(id),
    relation    TEXT NOT NULL CHECK(relation IN (
        'supports','depends_on','contradicts','questions'
    )),
    strength    REAL DEFAULT 1.0,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audits (
    id          TEXT PRIMARY KEY,
    project     TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    timestamp   TEXT NOT NULL,
    findings    TEXT NOT NULL,
    claim_count INTEGER,
    edge_count  INTEGER,
    critical_count INTEGER
);

CREATE TABLE IF NOT EXISTS extract_cache (
    content_hash    TEXT NOT NULL,
    model           TEXT NOT NULL,
    prompt_version  INTEGER NOT NULL,
    result_json     TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (content_hash, model, prompt_version)
);

CREATE TABLE IF NOT EXISTS hook_fire_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT NOT NULL,
    claim_id        TEXT NOT NULL,
    finding_type    TEXT NOT NULL,
    fired_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hook_fire_log_project_time ON hook_fire_log(project, fired_at);

-- session_claims tracks which sessions have "seen" each claim — including
-- claims recognized via cross-batch dedup (which keep the original session_id
-- on the claim row). Querying this table instead of claims.session_id gives
-- accurate per-session membership even when claims are shared across sessions.
CREATE TABLE IF NOT EXISTS session_claims (
    session_id  TEXT NOT NULL,
    claim_id    TEXT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
    PRIMARY KEY (session_id, claim_id)
);
CREATE INDEX IF NOT EXISTS idx_session_claims_session ON session_claims(session_id);

CREATE INDEX IF NOT EXISTS idx_claims_project ON claims(project);
CREATE INDEX IF NOT EXISTS idx_claims_basis ON claims(basis);
CREATE INDEX IF NOT EXISTS idx_claims_text ON claims(text);
CREATE UNIQUE INDEX IF NOT EXISTS idx_edges_unique ON edges(from_id, to_id, relation);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
