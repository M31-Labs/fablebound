-- tiller pgstore schema — spec.tiller-provider-agnostic §3.1 / Appendix A
--
-- Production guidance:
--   Run exactly ONE host-managed migration singleton (e.g. systemd oneshot).
--   Do NOT run concurrent bootstraps in the same database — the IF NOT EXISTS
--   guards are idempotent but not serialised across separate connections.
--   Docker / docker-compose is the TEST RIG only; never the production deployer.
--   Use TILLER_STORE_DSN (env) or --dsn (flag) to supply the connection string.
--
-- All tables use the public schema. For multi-tenant deployments, set
-- search_path to an isolated schema before running this file.

-- schema_version tracks applied migrations.
CREATE TABLE IF NOT EXISTS schema_version (
    version      INTEGER PRIMARY KEY,
    applied_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    description  TEXT        NOT NULL DEFAULT ''
);

-- run corresponds to spec §3.1 "run" record (manifest row).
CREATE TABLE IF NOT EXISTS run (
    id               TEXT        PRIMARY KEY,
    task             TEXT        NOT NULL DEFAULT '',
    workspace        TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'created',  -- created|running|completed|failed|halted
    reason_budget    INTEGER     NOT NULL DEFAULT 2,
    max_depth        INTEGER     NOT NULL DEFAULT 2,           -- max dispatch depth; root=0, depth-1 may dispatch, depth-2 terminal
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at         TIMESTAMPTZ,
    root_session_id  TEXT        NOT NULL DEFAULT '',
    policy_shas      JSONB       NOT NULL DEFAULT '{}',       -- kind→sha256 map
    hypha_trace_id   TEXT        NOT NULL DEFAULT ''
);

-- Idempotent migration: add max_depth to run if it does not exist (schema version 4).
-- ADD COLUMN IF NOT EXISTS resolves via search_path, so per-test schemas and the
-- production schema are handled identically.
ALTER TABLE run ADD COLUMN IF NOT EXISTS max_depth INTEGER NOT NULL DEFAULT 2;
ALTER TABLE run ALTER COLUMN max_depth SET DEFAULT 2;

-- dispatch corresponds to spec §3.1 "dispatch" record.
-- State machine: pending → claimed → running → completed | failed | expired
--
-- NOTE: dispatch IDs (d01, d02, …) are scoped to their run_id — they are NOT
-- globally unique. The primary key is (run_id, id) to reflect this.
CREATE TABLE IF NOT EXISTS dispatch (
    id               TEXT        NOT NULL,
    run_id           TEXT        NOT NULL REFERENCES run(id),
    parent_id        TEXT        NOT NULL DEFAULT '',
    role             TEXT        NOT NULL DEFAULT '',
    model            TEXT        NOT NULL DEFAULT '',
    profile          TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'pending',  -- pending|claimed|running|completed|failed|expired|halted|stale
    depth            INTEGER     NOT NULL DEFAULT 0,
    supervisor_pid   INTEGER     NOT NULL DEFAULT 0,
    max_turns        INTEGER     NOT NULL DEFAULT 0,
    timeout_minutes  INTEGER     NOT NULL DEFAULT 0,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at         TIMESTAMPTZ,
    exit_code        INTEGER     NOT NULL DEFAULT 0,
    cost_usd         NUMERIC(12,6) NOT NULL DEFAULT 0,
    num_turns        INTEGER     NOT NULL DEFAULT 0,
    session_id       TEXT        NOT NULL DEFAULT '',
    token_usage      JSONB       NOT NULL DEFAULT '{}',
    -- v2 tier / enforcement fields
    tier             TEXT        NOT NULL DEFAULT '',         -- reason|scrutiny|execute
    enforcement      TEXT        NOT NULL DEFAULT 'full',     -- full|degraded|sandboxed
    sandbox_spec     TEXT        NOT NULL DEFAULT '',         -- JSON sandbox.Record
    -- dispatch pool / lease fields (active from P4)
    claimed_by       TEXT        NOT NULL DEFAULT '',
    lease_until      TIMESTAMPTZ,
    -- adapter routing fields (populated by the dispatch requester)
    adapter_name     TEXT        NOT NULL DEFAULT '',  -- stub|claude-headless|…
    provider         TEXT        NOT NULL DEFAULT '',  -- anthropic|openai|local|…
    -- adapter config (settings.json body); stored as TEXT to preserve exact bytes
    adapter_config   TEXT,
    PRIMARY KEY (run_id, id)
);

CREATE INDEX IF NOT EXISTS dispatch_run_id_idx    ON dispatch (run_id);
CREATE INDEX IF NOT EXISTS dispatch_status_idx    ON dispatch (status);
CREATE INDEX IF NOT EXISTS dispatch_lease_idx     ON dispatch (lease_until) WHERE status = 'claimed';

-- Idempotent migration: add adapter_name and provider to dispatch (schema version 5).
-- These ALTER TABLE statements must appear AFTER the CREATE TABLE dispatch above so
-- that fresh-schema creation (test isolation schemas) succeeds in a single ExecContext.
ALTER TABLE dispatch ADD COLUMN IF NOT EXISTS adapter_name TEXT NOT NULL DEFAULT '';
ALTER TABLE dispatch ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';

-- Idempotent migration: add deny_reason to dispatch (schema version 6).
-- Populated when a pool-time gate denies a dispatch (status='denied').
-- Empty string for all other statuses; NOT NULL DEFAULT '' so v1 rows are unaffected.
ALTER TABLE dispatch ADD COLUMN IF NOT EXISTS deny_reason TEXT NOT NULL DEFAULT '';

-- Idempotent migration: add sandbox_spec to dispatch (schema version 8).
-- Stores a JSON sandbox.Record so queryable mirrors retain sandbox provenance.
ALTER TABLE dispatch ADD COLUMN IF NOT EXISTS sandbox_spec TEXT NOT NULL DEFAULT '';

-- Idempotent migration: add provider-neutral token accounting metadata.
ALTER TABLE dispatch ADD COLUMN IF NOT EXISTS token_usage JSONB NOT NULL DEFAULT '{}';

-- agent_run stores backend lifecycle metadata for abstracted interactive and
-- non-interactive agents.
CREATE TABLE IF NOT EXISTS agent_run (
    run_id             TEXT        NOT NULL REFERENCES run(id),
    id                 TEXT        NOT NULL,
    dispatch_id        TEXT        NOT NULL DEFAULT '',
    backend            TEXT        NOT NULL DEFAULT '',
    backend_agent_id   TEXT        NOT NULL DEFAULT '',
    role               TEXT        NOT NULL DEFAULT '',
    tier               TEXT        NOT NULL DEFAULT '',
    model              TEXT        NOT NULL DEFAULT '',
    effort             TEXT        NOT NULL DEFAULT '',
    token_usage        JSONB       NOT NULL DEFAULT '{}',
    parent_run_id      TEXT        NOT NULL DEFAULT '',
    parent_agent_id    TEXT        NOT NULL DEFAULT '',
    base_git_rev       TEXT        NOT NULL DEFAULT '',
    base_dirty_hash    TEXT        NOT NULL DEFAULT '',
    claimed_paths      JSONB       NOT NULL DEFAULT '[]',
    spawned_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at       TIMESTAMPTZ,
    reported_at        TIMESTAMPTZ,
    status             TEXT        NOT NULL DEFAULT 'spawned', -- requested|spawned|running|completed|failed|halted|late|stale|superseded|closed
    changed_files      JSONB       NOT NULL DEFAULT '[]',
    verification       JSONB       NOT NULL DEFAULT '[]',
    caveats            JSONB       NOT NULL DEFAULT '[]',
    diff_hash          TEXT        NOT NULL DEFAULT '',
    summary            TEXT        NOT NULL DEFAULT '',
    refs               JSONB       NOT NULL DEFAULT '[]',
    PRIMARY KEY (run_id, id)
);

CREATE INDEX IF NOT EXISTS agent_run_run_id_idx ON agent_run (run_id);
CREATE INDEX IF NOT EXISTS agent_run_status_idx ON agent_run (status);

ALTER TABLE agent_run ALTER COLUMN status SET DEFAULT 'spawned';
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS token_usage JSONB NOT NULL DEFAULT '{}';

-- checkpoint_candidate stores append-only checkpoint reports that must be
-- freshness-checked against base_git_rev/base_dirty_hash before commit.
CREATE TABLE IF NOT EXISTS checkpoint_candidate (
    seq                BIGSERIAL   PRIMARY KEY,
    id                 TEXT        NOT NULL DEFAULT '',
    run_id             TEXT        NOT NULL REFERENCES run(id),
    agent_run_id       TEXT        NOT NULL DEFAULT '',
    dispatch_id        TEXT        NOT NULL DEFAULT '',
    backend            TEXT        NOT NULL DEFAULT '',
    role               TEXT        NOT NULL DEFAULT '',
    tier               TEXT        NOT NULL DEFAULT '',
    model              TEXT        NOT NULL DEFAULT '',
    effort             TEXT        NOT NULL DEFAULT '',
    parent_run_id      TEXT        NOT NULL DEFAULT '',
    parent_agent_id    TEXT        NOT NULL DEFAULT '',
    base_git_rev       TEXT        NOT NULL DEFAULT '',
    base_dirty_hash    TEXT        NOT NULL DEFAULT '',
    claimed_paths      JSONB       NOT NULL DEFAULT '[]',
    reported_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    status             TEXT        NOT NULL DEFAULT 'proposed', -- proposed|fresh|late_valid|late_stale|conflicting|accepted|rejected
    changed_files      JSONB       NOT NULL DEFAULT '[]',
    verification       JSONB       NOT NULL DEFAULT '[]',
    caveats            JSONB       NOT NULL DEFAULT '[]',
    diff_hash          TEXT        NOT NULL DEFAULT '',
    summary            TEXT        NOT NULL DEFAULT '',
    refs               JSONB       NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS checkpoint_candidate_run_id_idx ON checkpoint_candidate (run_id);
CREATE INDEX IF NOT EXISTS checkpoint_candidate_status_idx ON checkpoint_candidate (status);

ALTER TABLE checkpoint_candidate ALTER COLUMN status SET DEFAULT 'proposed';

-- ledger_event is an append-only lifecycle stream for cross-dispatch events.
CREATE TABLE IF NOT EXISTS ledger_event (
    seq                     BIGSERIAL   PRIMARY KEY,
    id                      TEXT        NOT NULL DEFAULT '',
    run_id                  TEXT        NOT NULL REFERENCES run(id),
    agent_run_id            TEXT        NOT NULL DEFAULT '',
    checkpoint_candidate_id TEXT        NOT NULL DEFAULT '',
    dispatch_id             TEXT        NOT NULL DEFAULT '',
    backend                 TEXT        NOT NULL DEFAULT '',
    kind                    TEXT        NOT NULL DEFAULT '',
    status                  TEXT        NOT NULL DEFAULT '',
    at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    token_usage             JSONB       NOT NULL DEFAULT '{}',
    summary                 TEXT        NOT NULL DEFAULT '',
    refs                    JSONB       NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS ledger_event_run_id_idx ON ledger_event (run_id);
CREATE INDEX IF NOT EXISTS ledger_event_kind_idx   ON ledger_event (kind);
ALTER TABLE ledger_event ADD COLUMN IF NOT EXISTS token_usage JSONB NOT NULL DEFAULT '{}';

-- dispatch_seq is an atomic per-run dispatch counter for AllocDispatch.
-- One row per run; next_n is incremented atomically via INSERT ... ON CONFLICT.
CREATE TABLE IF NOT EXISTS dispatch_seq (
    run_id  TEXT    PRIMARY KEY REFERENCES run(id),
    next_n  INTEGER NOT NULL DEFAULT 1
);

-- doc stores brief, report, and note bodies.
-- Unique constraint: for brief/report (filename='') one row per (kind, run_id,
-- dispatch_id). For notes (kind='note', filename≠'') the filename makes the row
-- unique. The compound key (kind, run_id, dispatch_id, filename) covers both cases.
CREATE TABLE IF NOT EXISTS doc (
    id           BIGSERIAL   PRIMARY KEY,
    kind         TEXT        NOT NULL,                        -- brief|report|note
    run_id       TEXT        NOT NULL REFERENCES run(id),
    dispatch_id  TEXT        NOT NULL DEFAULT '',             -- '' for run-level notes
    author       TEXT        NOT NULL DEFAULT '',
    written_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- mdpp frontmatter fields mirrored as columns
    role         TEXT        NOT NULL DEFAULT '',
    tier         TEXT        NOT NULL DEFAULT '',
    filename     TEXT        NOT NULL DEFAULT '',             -- for notes: relative filename
    -- body (inline for ≤64KB; future: ref to object store)
    body         TEXT        NOT NULL DEFAULT '',
    UNIQUE (kind, run_id, dispatch_id, filename)
);

CREATE INDEX IF NOT EXISTS doc_run_id_idx ON doc (run_id);
CREATE INDEX IF NOT EXISTS doc_kind_idx   ON doc (kind);

-- trace_event is the append-only tool/context trace stream.
-- spec §3.1 "trace-event" record.
CREATE TABLE IF NOT EXISTS trace_event (
    id            BIGSERIAL   PRIMARY KEY,
    ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind          TEXT        NOT NULL,                       -- tool|read|dispatch|report
    run_id        TEXT        NOT NULL REFERENCES run(id),
    dispatch_id   TEXT        NOT NULL DEFAULT '',
    role          TEXT        NOT NULL DEFAULT '',
    depth         INTEGER     NOT NULL DEFAULT 0,
    tool          TEXT        NOT NULL DEFAULT '',
    input_summary TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT '',            -- ok|error
    child_id      TEXT        NOT NULL DEFAULT '',
    model         TEXT        NOT NULL DEFAULT '',
    profile       TEXT        NOT NULL DEFAULT '',
    cost_usd      NUMERIC(12,6) NOT NULL DEFAULT 0,
    num_turns     INTEGER     NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS trace_event_run_id_idx ON trace_event (run_id);
CREATE INDEX IF NOT EXISTS trace_event_ts_idx     ON trace_event (ts);

-- audit_event stores the full audit.DecisionEvent (arbiter governance decisions).
-- spec §3.1 "audit-event" record.
CREATE TABLE IF NOT EXISTS audit_event (
    id          BIGSERIAL   PRIMARY KEY,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
    run_id      TEXT        NOT NULL REFERENCES run(id),
    kind        TEXT        NOT NULL DEFAULT '',              -- dispatch|toolgate
    event       JSONB       NOT NULL                         -- full audit.DecisionEvent
);

CREATE INDEX IF NOT EXISTS audit_event_run_id_idx ON audit_event (run_id);
CREATE INDEX IF NOT EXISTS audit_event_kind_idx   ON audit_event (kind);
CREATE INDEX IF NOT EXISTS audit_event_ts_idx     ON audit_event (ts);

-- dispatch_facts is a SQL view exposing per-run active/reason dispatch aggregates.
-- It is the factsource backing for arbiter governance: the postgres factsource
-- loader queries it as a read-only "table" (type/key/fields/version columns).
--
-- active = status IN ('running','pending','claimed')  — mirrors fsstore semantics
-- reason = tier='reason' OR (tier='' AND model='fable')  — v1 model fallback
--
-- The view is idempotent: CREATE OR REPLACE is safe to run multiple times.
CREATE OR REPLACE VIEW dispatch_facts AS
SELECT
    'dispatch_facts'::TEXT                                            AS type,
    d.run_id                                                          AS key,
    jsonb_build_object(
        'active_dispatches',
        COUNT(*) FILTER (WHERE d.status IN ('running','pending','claimed')),
        'reason_dispatches',
        COUNT(*) FILTER (WHERE d.tier = 'reason'
                           OR  (d.tier = '' AND d.model = 'fable')),
        'reason_budget',
        MAX(r.reason_budget)
    )                                                                 AS fields,
    1::BIGINT                                                         AS version
FROM dispatch d
JOIN run r ON r.id = d.run_id
GROUP BY d.run_id;

-- Seed the version rows (idempotent via ON CONFLICT DO NOTHING).
INSERT INTO schema_version (version, description)
VALUES (1, 'initial tiller scratch bus schema')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (2, 'add dispatch_seq; widen doc unique key to include filename')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (3, 'add dispatch_facts view for arbiter factsource governance')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (4, 'add max_depth column to run table (spec §4.3)')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (5, 'add adapter_name and provider columns to dispatch table')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (6, 'add deny_reason column to dispatch table for pool-time gate denials')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (7, 'change default max_depth to 2 for terminal depth-2 agents')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (8, 'add sandbox_spec column to dispatch table for runtime isolation metadata')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (9, 'add agent/checkpoint lifecycle tables')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (10, 'align agent and checkpoint lifecycle status vocabulary')
ON CONFLICT (version) DO NOTHING;

INSERT INTO schema_version (version, description)
VALUES (11, 'add provider-neutral token usage metadata')
ON CONFLICT (version) DO NOTHING;
