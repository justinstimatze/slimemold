CREATE TABLE IF NOT EXISTS claims (
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
    speaker     TEXT DEFAULT 'user' CHECK(speaker IN ('user','assistant')),
    created_at  TEXT NOT NULL,
    challenged  INTEGER DEFAULT 0,
    verified    INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS edges (
    id          TEXT PRIMARY KEY,
    from_id     TEXT NOT NULL REFERENCES claims(id),
    to_id       TEXT NOT NULL REFERENCES claims(id),
    relation    TEXT NOT NULL CHECK(relation IN (
        'supports','depends_on','contradicts','analogous_to','derived_from','refines'
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

CREATE INDEX IF NOT EXISTS idx_claims_project ON claims(project);
CREATE INDEX IF NOT EXISTS idx_claims_basis ON claims(basis);
CREATE INDEX IF NOT EXISTS idx_claims_text ON claims(text);
CREATE UNIQUE INDEX IF NOT EXISTS idx_edges_unique ON edges(from_id, to_id, relation);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
