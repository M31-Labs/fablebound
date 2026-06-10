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
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at         TIMESTAMPTZ,
    root_session_id  TEXT        NOT NULL DEFAULT '',
    policy_shas      JSONB       NOT NULL DEFAULT '{}',       -- kind→sha256 map
    hypha_trace_id   TEXT        NOT NULL DEFAULT ''
);

-- dispatch corresponds to spec §3.1 "dispatch" record.
-- State machine: pending → claimed → running → completed | failed | expired
CREATE TABLE IF NOT EXISTS dispatch (
    id               TEXT        PRIMARY KEY,
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
    -- v2 tier / enforcement fields
    tier             TEXT        NOT NULL DEFAULT '',         -- reason|scrutiny|execute
    enforcement      TEXT        NOT NULL DEFAULT 'full',     -- full|degraded
    -- dispatch pool / lease fields (active from P4)
    claimed_by       TEXT        NOT NULL DEFAULT '',
    lease_until      TIMESTAMPTZ,
    -- adapter config (settings.json body)
    adapter_config   JSONB
);

CREATE INDEX IF NOT EXISTS dispatch_run_id_idx    ON dispatch (run_id);
CREATE INDEX IF NOT EXISTS dispatch_status_idx    ON dispatch (status);
CREATE INDEX IF NOT EXISTS dispatch_lease_idx     ON dispatch (lease_until) WHERE status = 'claimed';

-- doc stores brief, report, and note bodies.
-- unique constraint: one (kind, run_id, dispatch_id) tuple per record.
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
    UNIQUE (kind, run_id, dispatch_id)
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

-- Seed the version row (idempotent via ON CONFLICT DO NOTHING).
INSERT INTO schema_version (version, description)
VALUES (1, 'initial tiller scratch bus schema')
ON CONFLICT (version) DO NOTHING;
