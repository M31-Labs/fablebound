// Package pgstore implements scratch.Store backed by PostgreSQL.
//
// Connection pool: small (max 10 open, 5 idle), ephemeral-per-operation semantics —
// no resident daemon, no long-held transactions. Open once; call Close when done.
//
// AuditSink async/spool: each sink owns a buffered channel (capacity 256) drained
// by a single writer goroutine. On DB error the event is appended to a local JSONL
// spool file under os.TempDir()/tiller-audit-spool/<runID>-<kind>.jsonl. The caller
// is never blocked. Close() drains the channel and stops the goroutine.
//
// Materialize writes brief.md and settings.json into <dir>/ so that file-needing
// adapters (claudeheadless) can run against a remote store.
package pgstore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/sandbox"
	"m31labs.dev/tiller/internal/scratch"

	"m31labs.dev/arbiter/audit"
)

// ── Store ─────────────────────────────────────────────────────────────────────

// Store implements scratch.Store over PostgreSQL via database/sql + pgx stdlib.
type Store struct {
	db *DB
}

// NewStore wraps an opened *DB as a scratch.Store.
func NewStore(db *DB) *Store {
	return &Store{db: db}
}

// OpenStore opens a pgstore against dsn, runs migrations, and returns a Store.
// The caller must call Close when done.
func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	db, err := Open(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pgstore: migrate: %w", err)
	}
	return NewStore(db), nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error { return s.db.Close() }

// ── Run lifecycle ─────────────────────────────────────────────────────────────

// CreateRun inserts a new run row. If r.ID is empty a fresh run ID is generated.
func (s *Store) CreateRun(r *scratch.Run) (string, error) {
	if r.ID == "" {
		r.ID = run.NewRunID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.Status == "" {
		r.Status = "created"
	}

	policySHAs, err := marshalJSONB(r.PolicySHAs)
	if err != nil {
		return "", fmt.Errorf("pgstore.CreateRun: marshal policy_shas: %w", err)
	}

	maxDepth := r.MaxDepth
	if maxDepth == 0 {
		maxDepth = run.DefaultMaxDepth
	}
	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO run (id, task, workspace, status, reason_budget, max_depth, created_at, ended_at,
		                 root_session_id, policy_shas, hypha_trace_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		r.ID, r.Task, r.Workspace, r.Status, r.ReasonBudget, maxDepth,
		r.CreatedAt, r.EndedAt,
		r.RootSessionID, policySHAs, r.HyphaTraceID,
	)
	if err != nil {
		return "", fmt.Errorf("pgstore.CreateRun: insert: %w", err)
	}
	return r.ID, nil
}

// ReadRun fetches the run record for runID.
func (s *Store) ReadRun(runID string) (*scratch.Run, error) {
	row := s.db.db.QueryRowContext(context.Background(), `
		SELECT id, task, workspace, status, reason_budget, max_depth, created_at, ended_at,
		       root_session_id, policy_shas, hypha_trace_id
		FROM run WHERE id = $1`, runID)

	r := &scratch.Run{}
	var policySHAsRaw []byte
	if err := row.Scan(
		&r.ID, &r.Task, &r.Workspace, &r.Status, &r.ReasonBudget, &r.MaxDepth,
		&r.CreatedAt, &r.EndedAt,
		&r.RootSessionID, &policySHAsRaw, &r.HyphaTraceID,
	); err == sql.ErrNoRows {
		return nil, fmt.Errorf("pgstore.ReadRun: not found: %s", runID)
	} else if err != nil {
		return nil, fmt.Errorf("pgstore.ReadRun: scan: %w", err)
	}
	if err := json.Unmarshal(policySHAsRaw, &r.PolicySHAs); err != nil {
		r.PolicySHAs = nil
	}
	// Apply default when column is zero (pre-migration rows).
	if r.MaxDepth == 0 {
		r.MaxDepth = run.DefaultMaxDepth
	}
	return r, nil
}

// WriteRun upserts the run record (status, budget, finalization).
func (s *Store) WriteRun(r *scratch.Run) error {
	policySHAs, err := marshalJSONB(r.PolicySHAs)
	if err != nil {
		return fmt.Errorf("pgstore.WriteRun: marshal policy_shas: %w", err)
	}
	maxDepth := r.MaxDepth
	if maxDepth == 0 {
		maxDepth = run.DefaultMaxDepth
	}
	_, err = s.db.db.ExecContext(context.Background(), `
		UPDATE run SET task=$2, workspace=$3, status=$4, reason_budget=$5, max_depth=$6,
		               created_at=$7, ended_at=$8, root_session_id=$9,
		               policy_shas=$10, hypha_trace_id=$11
		WHERE id=$1`,
		r.ID, r.Task, r.Workspace, r.Status, r.ReasonBudget, maxDepth,
		r.CreatedAt, r.EndedAt,
		r.RootSessionID, policySHAs, r.HyphaTraceID,
	)
	if err != nil {
		return fmt.Errorf("pgstore.WriteRun %s: %w", r.ID, err)
	}
	return nil
}

// ListRuns returns summary rows for all runs.
func (s *Store) ListRuns() ([]scratch.RunSummary, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT r.id, r.status, r.task,
		       COUNT(d.id)          AS dispatch_count,
		       COALESCE(SUM(d.cost_usd), 0) AS total_cost
		FROM run r
		LEFT JOIN dispatch d ON d.run_id = r.id
		GROUP BY r.id, r.status, r.task
		ORDER BY r.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListRuns: %w", err)
	}
	defer rows.Close()

	var out []scratch.RunSummary
	for rows.Next() {
		var s scratch.RunSummary
		var task string
		if err := rows.Scan(&s.RunID, &s.Status, &task, &s.DispatchCount, &s.TotalCostUSD); err != nil {
			return nil, fmt.Errorf("pgstore.ListRuns: scan: %w", err)
		}
		s.TaskFirstLine = run.FirstLine(task)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── Dispatch records ──────────────────────────────────────────────────────────

// AllocDispatch atomically allocates the next dNN dispatch ID.
//
// Uses a single atomic Postgres statement against dispatch_seq:
//
//	INSERT INTO dispatch_seq (run_id, next_n) VALUES ($1, 2)
//	ON CONFLICT (run_id) DO UPDATE SET next_n = dispatch_seq.next_n + 1
//	RETURNING next_n - 1
//
// This returns the *previous* value (the ordinal for the new dispatch) in one
// round-trip, with no advisory locks or in-process mutexes required. The
// run_id TEXT PRIMARY KEY on dispatch_seq provides row-level locking for the
// upsert via Postgres's standard row-lock-on-conflict semantics.
func (s *Store) AllocDispatch(runID string) (string, error) {
	ctx := context.Background()

	// Atomic counter increment: returns the ordinal for this new dispatch (1-based).
	var n int
	err := s.db.db.QueryRowContext(ctx, `
		INSERT INTO dispatch_seq (run_id, next_n)
		VALUES ($1, 2)
		ON CONFLICT (run_id) DO UPDATE
		  SET next_n = dispatch_seq.next_n + 1
		RETURNING next_n - 1`,
		runID).Scan(&n)
	if err != nil {
		return "", fmt.Errorf("pgstore.AllocDispatch: seq upsert: %w", err)
	}

	id := fmt.Sprintf("d%02d", n)

	// Insert a placeholder dispatch row so the slot is reserved.
	now := time.Now().UTC()
	_, err = s.db.db.ExecContext(ctx, `
		INSERT INTO dispatch (run_id, id, status, started_at)
		VALUES ($1, $2, 'pending', $3)
		ON CONFLICT (run_id, id) DO NOTHING`,
		runID, id, now)
	if err != nil {
		return "", fmt.Errorf("pgstore.AllocDispatch: insert placeholder: %w", err)
	}

	return id, nil
}

// ReadDispatch fetches a dispatch record.
func (s *Store) ReadDispatch(runID, dispatchID string) (*scratch.Dispatch, error) {
	row := s.db.db.QueryRowContext(context.Background(), `
		SELECT id, parent_id, role, model, profile, status, depth,
		       supervisor_pid, max_turns, timeout_minutes, started_at, ended_at,
		       exit_code, cost_usd, num_turns, session_id, tier, enforcement,
		       sandbox_spec, claimed_by, lease_until, adapter_name, provider, deny_reason
		FROM dispatch WHERE run_id=$1 AND id=$2`, runID, dispatchID)
	return scanDispatch(row)
}

// WriteDispatch upserts a dispatch record.
func (s *Store) WriteDispatch(runID string, d *scratch.Dispatch) error {
	sandboxSpec, err := encodeSandboxRecord(d.Sandbox)
	if err != nil {
		return fmt.Errorf("pgstore.WriteDispatch %s/%s: sandbox: %w", runID, d.ID, err)
	}
	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO dispatch (
			run_id, id, parent_id, role, model, profile, status, depth,
			supervisor_pid, max_turns, timeout_minutes, started_at, ended_at,
			exit_code, cost_usd, num_turns, session_id, tier, enforcement,
			sandbox_spec, claimed_by, lease_until, adapter_name, provider, deny_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		ON CONFLICT (run_id, id) DO UPDATE SET
			parent_id=$3, role=$4, model=$5, profile=$6, status=$7, depth=$8,
			supervisor_pid=$9, max_turns=$10, timeout_minutes=$11, started_at=$12,
			ended_at=$13, exit_code=$14, cost_usd=$15, num_turns=$16, session_id=$17,
			tier=$18, enforcement=$19, sandbox_spec=$20, claimed_by=$21, lease_until=$22,
			adapter_name=$23, provider=$24, deny_reason=$25`,
		runID, d.ID, d.Parent, d.Role, d.Model, d.Profile, d.Status, d.Depth,
		d.SupervisorPID, d.MaxTurns, d.TimeoutMinutes, d.StartedAt, d.EndedAt,
		d.Exit, d.CostUSD, d.NumTurns, d.SessionID,
		d.Tier, d.Enforcement, sandboxSpec, d.ClaimedBy, d.LeaseUntil,
		d.Adapter, d.Provider, d.DenyReason,
	)
	if err != nil {
		return fmt.Errorf("pgstore.WriteDispatch %s/%s: %w", runID, d.ID, err)
	}
	return nil
}

// ListDispatches returns all dispatch records for a run, in dNN order.
func (s *Store) ListDispatches(runID string) ([]*scratch.Dispatch, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT id, parent_id, role, model, profile, status, depth,
		       supervisor_pid, max_turns, timeout_minutes, started_at, ended_at,
		       exit_code, cost_usd, num_turns, session_id, tier, enforcement,
		       sandbox_spec, claimed_by, lease_until, adapter_name, provider, deny_reason
		FROM dispatch WHERE run_id=$1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListDispatches %s: %w", runID, err)
	}
	defer rows.Close()

	var out []*scratch.Dispatch
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore.ListDispatches scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DispatchFacts returns the aggregate active/reason counters for dispatch.arb.
//
// SQL logic mirrors fsstore:
//   - active  = status IN ('running','pending','claimed')
//   - reason  = tier='reason' OR (tier=” AND model='fable')  [v1 compat]
func (s *Store) DispatchFacts(runID string) (scratch.Facts, error) {
	const q = `
		SELECT
		  COUNT(*) FILTER (WHERE status IN ('running','pending','claimed'))           AS active,
		  COUNT(*) FILTER (WHERE tier = 'reason'
		                      OR (tier = '' AND model = 'fable'))                     AS reason_count
		FROM dispatch
		WHERE run_id = $1`

	var f scratch.Facts
	err := s.db.db.QueryRowContext(context.Background(), q, runID).
		Scan(&f.Active, &f.ReasonCount)
	if err != nil {
		return scratch.Facts{}, fmt.Errorf("pgstore.DispatchFacts %s: %w", runID, err)
	}
	return f, nil
}

// ── Agent / checkpoint lifecycle records ─────────────────────────────────────

// CreateAgentRun stores a newly spawned agent lifecycle record.
func (s *Store) CreateAgentRun(runID string, ar *scratch.AgentRun) error {
	return s.WriteAgentRun(runID, ar)
}

// WriteAgentRun upserts an agent lifecycle record.
func (s *Store) WriteAgentRun(runID string, ar *scratch.AgentRun) error {
	if ar == nil {
		return fmt.Errorf("pgstore.WriteAgentRun: nil agent run")
	}
	if ar.ID == "" {
		return fmt.Errorf("pgstore.WriteAgentRun: id is required")
	}
	if ar.Status == "" {
		ar.Status = scratch.AgentRunStatusSpawned
	}
	if ar.Status != "" && !scratch.ValidAgentRunStatus(ar.Status) {
		return fmt.Errorf("pgstore.WriteAgentRun: unknown status %q", ar.Status)
	}
	if ar.RunID == "" {
		ar.RunID = runID
	}
	if ar.SpawnedAt.IsZero() {
		ar.SpawnedAt = time.Now().UTC()
	}
	claimedPaths, err := marshalStringSlice(ar.ClaimedPaths)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun: claimed_paths: %w", err)
	}
	changedFiles, err := marshalStringSlice(ar.ChangedFiles)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun: changed_files: %w", err)
	}
	verification, err := marshalStringSlice(ar.Verification)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun: verification: %w", err)
	}
	caveats, err := marshalStringSlice(ar.Caveats)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun: caveats: %w", err)
	}
	refs, err := marshalStringSlice(ar.Refs)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun: refs: %w", err)
	}

	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO agent_run (
			run_id, id, dispatch_id, backend, backend_agent_id, role, tier, model,
			effort, parent_run_id, parent_agent_id, base_git_rev, base_dirty_hash,
			claimed_paths, spawned_at, completed_at, reported_at, status,
			changed_files, verification, caveats, diff_hash, summary, refs)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		ON CONFLICT (run_id, id) DO UPDATE SET
			dispatch_id=$3, backend=$4, backend_agent_id=$5, role=$6, tier=$7, model=$8,
			effort=$9, parent_run_id=$10, parent_agent_id=$11, base_git_rev=$12,
			base_dirty_hash=$13, claimed_paths=$14, spawned_at=$15, completed_at=$16,
			reported_at=$17, status=$18, changed_files=$19, verification=$20,
			caveats=$21, diff_hash=$22, summary=$23, refs=$24`,
		runID, ar.ID, ar.DispatchID, ar.Backend, ar.BackendAgentID, ar.Role, ar.Tier,
		ar.Model, ar.Effort, ar.ParentRunID, ar.ParentAgentID, ar.BaseGitRev,
		ar.BaseDirtyHash, claimedPaths, ar.SpawnedAt, ar.CompletedAt, ar.ReportedAt,
		ar.Status, changedFiles, verification, caveats, ar.DiffHash, ar.Summary, refs,
	)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAgentRun %s/%s: %w", runID, ar.ID, err)
	}
	return nil
}

// ListAgentRuns returns all agent lifecycle records for a run.
func (s *Store) ListAgentRuns(runID string) ([]*scratch.AgentRun, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT id, dispatch_id, backend, backend_agent_id, role, tier, model,
		       effort, parent_run_id, parent_agent_id, base_git_rev, base_dirty_hash,
		       claimed_paths, spawned_at, completed_at, reported_at, status,
		       changed_files, verification, caveats, diff_hash, summary, refs
		FROM agent_run WHERE run_id=$1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListAgentRuns %s: %w", runID, err)
	}
	defer rows.Close()

	var out []*scratch.AgentRun
	for rows.Next() {
		ar := &scratch.AgentRun{RunID: runID}
		var claimedPaths, changedFiles, verification, caveats, refs []byte
		if err := rows.Scan(
			&ar.ID, &ar.DispatchID, &ar.Backend, &ar.BackendAgentID, &ar.Role,
			&ar.Tier, &ar.Model, &ar.Effort, &ar.ParentRunID, &ar.ParentAgentID,
			&ar.BaseGitRev, &ar.BaseDirtyHash, &claimedPaths, &ar.SpawnedAt,
			&ar.CompletedAt, &ar.ReportedAt, &ar.Status, &changedFiles,
			&verification, &caveats, &ar.DiffHash, &ar.Summary, &refs,
		); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns scan: %w", err)
		}
		if err := unmarshalStringSlice(claimedPaths, &ar.ClaimedPaths); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns claimed_paths: %w", err)
		}
		if err := unmarshalStringSlice(changedFiles, &ar.ChangedFiles); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns changed_files: %w", err)
		}
		if err := unmarshalStringSlice(verification, &ar.Verification); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns verification: %w", err)
		}
		if err := unmarshalStringSlice(caveats, &ar.Caveats); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns caveats: %w", err)
		}
		if err := unmarshalStringSlice(refs, &ar.Refs); err != nil {
			return nil, fmt.Errorf("pgstore.ListAgentRuns refs: %w", err)
		}
		out = append(out, ar)
	}
	return out, rows.Err()
}

// AppendCheckpointCandidate records a checkpoint candidate.
func (s *Store) AppendCheckpointCandidate(runID string, c scratch.CheckpointCandidate) error {
	if c.Status == "" {
		c.Status = scratch.CheckpointStatusProposed
	}
	if c.Status != "" && !scratch.ValidCheckpointStatus(c.Status) {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: unknown status %q", c.Status)
	}
	if c.RunID == "" {
		c.RunID = runID
	}
	if c.ReportedAt.IsZero() {
		c.ReportedAt = time.Now().UTC()
	}
	claimedPaths, err := marshalStringSlice(c.ClaimedPaths)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: claimed_paths: %w", err)
	}
	changedFiles, err := marshalStringSlice(c.ChangedFiles)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: changed_files: %w", err)
	}
	verification, err := marshalStringSlice(c.Verification)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: verification: %w", err)
	}
	caveats, err := marshalStringSlice(c.Caveats)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: caveats: %w", err)
	}
	refs, err := marshalStringSlice(c.Refs)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate: refs: %w", err)
	}

	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO checkpoint_candidate (
			id, run_id, agent_run_id, dispatch_id, backend, role, tier, model, effort,
			parent_run_id, parent_agent_id, base_git_rev, base_dirty_hash, claimed_paths,
			reported_at, status, changed_files, verification, caveats, diff_hash, summary, refs)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		c.ID, runID, c.AgentRunID, c.DispatchID, c.Backend, c.Role, c.Tier,
		c.Model, c.Effort, c.ParentRunID, c.ParentAgentID, c.BaseGitRev,
		c.BaseDirtyHash, claimedPaths, c.ReportedAt, c.Status, changedFiles,
		verification, caveats, c.DiffHash, c.Summary, refs,
	)
	if err != nil {
		return fmt.Errorf("pgstore.AppendCheckpointCandidate %s/%s: %w", runID, c.ID, err)
	}
	return nil
}

// ListCheckpointCandidates returns checkpoint candidates in append order.
func (s *Store) ListCheckpointCandidates(runID string) ([]scratch.CheckpointCandidate, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT id, agent_run_id, dispatch_id, backend, role, tier, model, effort,
		       parent_run_id, parent_agent_id, base_git_rev, base_dirty_hash,
		       claimed_paths, reported_at, status, changed_files, verification,
		       caveats, diff_hash, summary, refs
		FROM checkpoint_candidate WHERE run_id=$1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListCheckpointCandidates %s: %w", runID, err)
	}
	defer rows.Close()

	var out []scratch.CheckpointCandidate
	for rows.Next() {
		c := scratch.CheckpointCandidate{RunID: runID}
		var claimedPaths, changedFiles, verification, caveats, refs []byte
		if err := rows.Scan(
			&c.ID, &c.AgentRunID, &c.DispatchID, &c.Backend, &c.Role, &c.Tier,
			&c.Model, &c.Effort, &c.ParentRunID, &c.ParentAgentID, &c.BaseGitRev,
			&c.BaseDirtyHash, &claimedPaths, &c.ReportedAt, &c.Status,
			&changedFiles, &verification, &caveats, &c.DiffHash, &c.Summary, &refs,
		); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates scan: %w", err)
		}
		if err := unmarshalStringSlice(claimedPaths, &c.ClaimedPaths); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates claimed_paths: %w", err)
		}
		if err := unmarshalStringSlice(changedFiles, &c.ChangedFiles); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates changed_files: %w", err)
		}
		if err := unmarshalStringSlice(verification, &c.Verification); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates verification: %w", err)
		}
		if err := unmarshalStringSlice(caveats, &c.Caveats); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates caveats: %w", err)
		}
		if err := unmarshalStringSlice(refs, &c.Refs); err != nil {
			return nil, fmt.Errorf("pgstore.ListCheckpointCandidates refs: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppendLedgerEvent records an append-only lifecycle event.
func (s *Store) AppendLedgerEvent(runID string, ev scratch.LedgerEvent) error {
	if ev.Kind == "" {
		return fmt.Errorf("pgstore.AppendLedgerEvent: kind is required")
	}
	if ev.RunID == "" {
		ev.RunID = runID
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	refs, err := marshalStringSlice(ev.Refs)
	if err != nil {
		return fmt.Errorf("pgstore.AppendLedgerEvent: refs: %w", err)
	}
	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO ledger_event (
			id, run_id, agent_run_id, checkpoint_candidate_id, dispatch_id,
			backend, kind, status, at, summary, refs)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		ev.ID, runID, ev.AgentRunID, ev.CheckpointCandidate, ev.DispatchID,
		ev.Backend, ev.Kind, ev.Status, ev.At, ev.Summary, refs,
	)
	if err != nil {
		return fmt.Errorf("pgstore.AppendLedgerEvent %s/%s: %w", runID, ev.ID, err)
	}
	return nil
}

// ListLedgerEvents returns ledger events in append order.
func (s *Store) ListLedgerEvents(runID string) ([]scratch.LedgerEvent, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT id, agent_run_id, checkpoint_candidate_id, dispatch_id, backend,
		       kind, status, at, summary, refs
		FROM ledger_event WHERE run_id=$1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListLedgerEvents %s: %w", runID, err)
	}
	defer rows.Close()

	var out []scratch.LedgerEvent
	for rows.Next() {
		ev := scratch.LedgerEvent{RunID: runID}
		var refs []byte
		if err := rows.Scan(
			&ev.ID, &ev.AgentRunID, &ev.CheckpointCandidate, &ev.DispatchID,
			&ev.Backend, &ev.Kind, &ev.Status, &ev.At, &ev.Summary, &refs,
		); err != nil {
			return nil, fmt.Errorf("pgstore.ListLedgerEvents scan: %w", err)
		}
		if err := unmarshalStringSlice(refs, &ev.Refs); err != nil {
			return nil, fmt.Errorf("pgstore.ListLedgerEvents refs: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── Document records ──────────────────────────────────────────────────────────

// WriteBrief stores the brief body (brief.md) for a dispatch.
func (s *Store) WriteBrief(runID, dispatchID string, body []byte) error {
	return s.upsertDoc(runID, dispatchID, "brief", "", body)
}

// ReadBrief reads the brief body for a dispatch.
func (s *Store) ReadBrief(runID, dispatchID string) ([]byte, error) {
	return s.readDoc(runID, dispatchID, "brief")
}

// WriteReport stores the report body for a dispatch.
func (s *Store) WriteReport(runID, dispatchID string, body []byte) error {
	return s.upsertDoc(runID, dispatchID, "report", "", body)
}

// ReadReport reads the report body for a dispatch.
func (s *Store) ReadReport(runID, dispatchID string) ([]byte, error) {
	return s.readDoc(runID, dispatchID, "report")
}

// AppendNote appends a note document to the run notes store.
func (s *Store) AppendNote(runID, author string, body []byte) (scratch.NoteRef, error) {
	now := time.Now().UTC()
	safeAuthor := strings.NewReplacer("/", "-", " ", "-").Replace(author)
	filename := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405.000000000"), safeAuthor)

	_, err := s.db.db.ExecContext(context.Background(), `
		INSERT INTO doc (kind, run_id, dispatch_id, author, written_at, filename, body)
		VALUES ('note', $1, '', $2, $3, $4, $5)
		ON CONFLICT (kind, run_id, dispatch_id, filename) DO NOTHING`,
		runID, author, now, filename, string(body),
	)
	if err != nil {
		return scratch.NoteRef{}, fmt.Errorf("pgstore.AppendNote %s: %w", runID, err)
	}
	return scratch.NoteRef{Filename: filename, Author: author, WrittenAt: now}, nil
}

// ListNotes returns all note references for a run, ordered by filename.
func (s *Store) ListNotes(runID string) ([]scratch.NoteRef, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT filename, author, written_at
		FROM doc WHERE kind='note' AND run_id=$1
		ORDER BY filename`, runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListNotes %s: %w", runID, err)
	}
	defer rows.Close()

	var out []scratch.NoteRef
	for rows.Next() {
		var r scratch.NoteRef
		if err := rows.Scan(&r.Filename, &r.Author, &r.WrittenAt); err != nil {
			return nil, fmt.Errorf("pgstore.ListNotes scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Adapter config ────────────────────────────────────────────────────────────

// WriteAdapterConfig stores settings.json for a dispatch (stored as TEXT to preserve exact bytes).
func (s *Store) WriteAdapterConfig(runID, dispatchID string, cfg []byte) error {
	_, err := s.db.db.ExecContext(context.Background(), `
		UPDATE dispatch SET adapter_config=$1 WHERE run_id=$2 AND id=$3`,
		string(cfg), runID, dispatchID,
	)
	if err != nil {
		return fmt.Errorf("pgstore.WriteAdapterConfig %s/%s: %w", runID, dispatchID, err)
	}
	return nil
}

// ReadAdapterConfig reads settings.json for a dispatch.
func (s *Store) ReadAdapterConfig(runID, dispatchID string) ([]byte, error) {
	var cfg sql.NullString
	err := s.db.db.QueryRowContext(context.Background(), `
		SELECT adapter_config FROM dispatch WHERE run_id=$1 AND id=$2`,
		runID, dispatchID).Scan(&cfg)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("pgstore.ReadAdapterConfig: not found %s/%s", runID, dispatchID)
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore.ReadAdapterConfig %s/%s: %w", runID, dispatchID, err)
	}
	if !cfg.Valid {
		return nil, nil
	}
	return []byte(cfg.String), nil
}

// ── Trace events ──────────────────────────────────────────────────────────────

// AppendTraceEvent inserts one trace event. Failures are non-fatal (best-effort).
func (s *Store) AppendTraceEvent(runID, dispatchID string, ev scratch.TraceEvent) error {
	ts := ev.Ts
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.db.ExecContext(context.Background(), `
		INSERT INTO trace_event
		  (ts, kind, run_id, dispatch_id, role, depth, tool, input_summary,
		   status, child_id, model, profile, cost_usd, num_turns)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		ts, ev.Kind, runID, dispatchID, ev.Role, ev.Depth, ev.Tool,
		ev.InputSummary, ev.Status, ev.ChildID, ev.Model, ev.Profile,
		ev.CostUSD, ev.NumTurns,
	)
	if err != nil {
		return fmt.Errorf("pgstore.AppendTraceEvent %s/%s: %w", runID, dispatchID, err)
	}
	return nil
}

// ── AuditSink ─────────────────────────────────────────────────────────────────

// asyncAuditSink wraps auditlog.Sink to make writes async with a JSONL spool fallback.
// The returned io.Closer must be called to drain and stop the goroutine.
type asyncAuditSink struct {
	sink     *auditlog.Sink
	ch       chan audit.DecisionEvent
	done     chan struct{}
	spoolDir string
	runID    string
	kind     string
}

// AuditSink opens an async audit sink for the given run/kind.
// Writes are buffered (capacity 256) and flushed by a single background goroutine.
// On DB INSERT error the event is appended to a JSONL spool file at:
//
//	os.TempDir()/tiller-audit-spool/<runID>-<kind>.jsonl
//
// The caller MUST call Close() on the returned io.Closer to drain the channel.
func (s *Store) AuditSink(runID, kind string) (*auditlog.Sink, io.Closer, error) {
	if kind != "dispatch" && kind != "toolgate" {
		return nil, nil, fmt.Errorf("pgstore.AuditSink: unknown kind %q (want dispatch|toolgate)", kind)
	}

	spoolDir := filepath.Join(os.TempDir(), "tiller-audit-spool")
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("pgstore.AuditSink: mkdir spool: %w", err)
	}

	// Build a thin sink backed by a spool file so auditlog.Sink remains reusable.
	spoolPath := filepath.Join(spoolDir, runID+"-"+kind+".jsonl")
	sink, err := auditlog.Open(spoolPath)
	if err != nil {
		return nil, nil, fmt.Errorf("pgstore.AuditSink: open spool sink: %w", err)
	}

	a := &asyncAuditSink{
		sink:     sink,
		ch:       make(chan audit.DecisionEvent, 256),
		done:     make(chan struct{}),
		spoolDir: spoolDir,
		runID:    runID,
		kind:     kind,
	}

	go a.loop(s)
	return sink, a, nil
}

// WriteDecision enqueues an event for async INSERT. Never blocks the caller;
// if the channel is full, the event is silently dropped (overflow safety).
func (a *asyncAuditSink) WriteDecision(ev audit.DecisionEvent) {
	select {
	case a.ch <- ev:
	default:
		// Channel full — drop rather than block.
	}
}

// Close drains the channel, stops the goroutine, and waits for it to exit.
func (a *asyncAuditSink) Close() error {
	close(a.ch)
	<-a.done
	return nil
}

// loop drains the channel and attempts a DB INSERT per event.
// On failure it spools to a local JSONL file.
func (a *asyncAuditSink) loop(s *Store) {
	defer close(a.done)
	for ev := range a.ch {
		if err := insertAuditEvent(s, a.runID, a.kind, ev); err != nil {
			// Fallback: append to spool file (never block).
			_ = spoolAuditEvent(a.spoolDir, a.runID, a.kind, ev)
		}
	}
}

func insertAuditEvent(s *Store, runID, kind string, ev audit.DecisionEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = s.db.db.ExecContext(context.Background(), `
		INSERT INTO audit_event (run_id, kind, event) VALUES ($1,$2,$3)`,
		runID, kind, data,
	)
	return err
}

func spoolAuditEvent(spoolDir, runID, kind string, ev audit.DecisionEvent) error {
	path := filepath.Join(spoolDir, runID+"-"+kind+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(ev)
}

// ── Materialize ───────────────────────────────────────────────────────────────

// Materialize writes brief.md and settings.json from the store into dir
// so that file-needing adapters (claudeheadless) can run against a remote store.
// dir is the dispatch-level spool directory (e.g. <workspace>/.tiller/runs/<runID>/dispatches/<dispID>/).
func (s *Store) Materialize(runID, dispatchID, dir string) error {
	if dir == "" {
		// No directory provided; skip file materialization.
		// In pgstore, adapters that need on-disk files must supply a non-empty dir.
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pgstore.Materialize: mkdir %s: %w", dir, err)
	}

	// Write brief.md.
	brief, err := s.ReadBrief(runID, dispatchID)
	if err == nil && len(brief) > 0 {
		if werr := os.WriteFile(filepath.Join(dir, "brief.md"), brief, 0o644); werr != nil {
			return fmt.Errorf("pgstore.Materialize: write brief.md: %w", werr)
		}
	}

	// Write settings.json.
	cfg, err := s.ReadAdapterConfig(runID, dispatchID)
	if err == nil && len(cfg) > 0 {
		if werr := os.WriteFile(filepath.Join(dir, "settings.json"), cfg, 0o644); werr != nil {
			return fmt.Errorf("pgstore.Materialize: write settings.json: %w", werr)
		}
	}

	return nil
}

// ── Display / tree helpers ────────────────────────────────────────────────────

// RenderTree renders the dispatch tree for a run as a human-readable string.
// Builds a run.Node tree from DB records and delegates to run.Render.
func (s *Store) RenderTree(runID string) (string, error) {
	root, err := s.buildRunNodeTree(runID)
	if err != nil {
		return "", fmt.Errorf("pgstore.RenderTree %s: %w", runID, err)
	}
	return run.Render(root), nil
}

// BuildRunSummaryJSON builds the derived run summary and marshals it to JSON.
func (s *Store) BuildRunSummaryJSON(runID string) ([]byte, error) {
	r, err := s.ReadRun(runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.BuildRunSummaryJSON: read run: %w", err)
	}
	dispatches, err := s.ListDispatches(runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.BuildRunSummaryJSON: list dispatches: %w", err)
	}

	summary := buildRunSummaryFromRecords(r, dispatches)
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("pgstore.BuildRunSummaryJSON: marshal: %w", err)
	}
	return data, nil
}

// BuildDispatchTree returns the full dispatch tree as a *scratch.DispatchNode.
func (s *Store) BuildDispatchTree(runID string) (*scratch.DispatchNode, error) {
	dispatches, err := s.ListDispatches(runID)
	if err != nil {
		return nil, fmt.Errorf("pgstore.BuildDispatchTree %s: %w", runID, err)
	}
	return buildDispatchNodeTree(dispatches), nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// scanDispatch scans a dispatch row from a *sql.Row or *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDispatch(row rowScanner) (*scratch.Dispatch, error) {
	d := &scratch.Dispatch{}
	var sandboxSpec string
	err := row.Scan(
		&d.ID, &d.Parent, &d.Role, &d.Model, &d.Profile,
		&d.Status, &d.Depth, &d.SupervisorPID, &d.MaxTurns, &d.TimeoutMinutes,
		&d.StartedAt, &d.EndedAt, &d.Exit, &d.CostUSD, &d.NumTurns,
		&d.SessionID, &d.Tier, &d.Enforcement, &sandboxSpec, &d.ClaimedBy, &d.LeaseUntil,
		&d.Adapter, &d.Provider, &d.DenyReason,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dispatch not found")
	}
	if err != nil {
		return nil, err
	}
	if sandboxSpec != "" {
		var rec sandbox.Record
		if err := json.Unmarshal([]byte(sandboxSpec), &rec); err != nil {
			return nil, fmt.Errorf("decode sandbox_spec: %w", err)
		}
		d.Sandbox = &rec
	}
	return d, nil
}

func encodeSandboxRecord(rec *sandbox.Record) (string, error) {
	if rec == nil {
		return "", nil
	}
	if err := rec.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// upsertDoc inserts or updates a doc row (kind brief|report; filename=”).
func (s *Store) upsertDoc(runID, dispatchID, kind, author string, body []byte) error {
	now := time.Now().UTC()
	_, err := s.db.db.ExecContext(context.Background(), `
		INSERT INTO doc (kind, run_id, dispatch_id, author, written_at, filename, body)
		VALUES ($1,$2,$3,$4,$5,'',$6)
		ON CONFLICT (kind, run_id, dispatch_id, filename)
		DO UPDATE SET body=$6, written_at=$5`,
		kind, runID, dispatchID, author, now, string(body),
	)
	if err != nil {
		return fmt.Errorf("pgstore.upsertDoc %s %s/%s: %w", kind, runID, dispatchID, err)
	}
	return nil
}

// readDoc reads the body for a (kind, run_id, dispatch_id) doc row.
func (s *Store) readDoc(runID, dispatchID, kind string) ([]byte, error) {
	var body string
	err := s.db.db.QueryRowContext(context.Background(), `
		SELECT body FROM doc WHERE kind=$1 AND run_id=$2 AND dispatch_id=$3`,
		kind, runID, dispatchID).Scan(&body)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("pgstore.readDoc: not found %s %s/%s", kind, runID, dispatchID)
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore.readDoc %s %s/%s: %w", kind, runID, dispatchID, err)
	}
	return []byte(body), nil
}

// marshalJSONB encodes v to JSON bytes suitable for a JSONB column.
// Returns '{}' for nil maps.
func marshalJSONB(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

func marshalStringSlice(v []string) ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v)
}

func unmarshalStringSlice(data []byte, out *[]string) error {
	if len(data) == 0 {
		*out = nil
		return nil
	}
	return json.Unmarshal(data, out)
}

// buildRunNodeTree builds a run.Node tree from dispatch records for rendering.
func (s *Store) buildRunNodeTree(runID string) (*run.Node, error) {
	dispatches, err := s.ListDispatches(runID)
	if err != nil {
		return nil, err
	}

	// Convert scratch.Dispatch → run.Meta for run.Node.
	byID := make(map[string]*run.Node, len(dispatches))
	for _, d := range dispatches {
		byID[d.ID] = &run.Node{Meta: dispatchToRunMeta(d)}
	}

	var roots []*run.Node
	for _, n := range byID {
		parentID := n.Meta.Parent
		if parentID == "" {
			roots = append(roots, n)
		} else if parent, ok := byID[parentID]; ok {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}

	// Sort children.
	sort.Slice(roots, func(i, j int) bool {
		return runNodeID(roots[i]) < runNodeID(roots[j])
	})
	for _, n := range byID {
		sort.Slice(n.Children, func(i, j int) bool {
			return runNodeID(n.Children[i]) < runNodeID(n.Children[j])
		})
	}

	if len(roots) == 0 {
		return &run.Node{}, nil
	}
	if len(roots) == 1 {
		return roots[0], nil
	}
	return &run.Node{Children: roots}, nil
}

func runNodeID(n *run.Node) string {
	if n.Meta != nil {
		return n.Meta.ID
	}
	return ""
}

// dispatchToRunMeta converts scratch.Dispatch to run.Meta for rendering.
func dispatchToRunMeta(d *scratch.Dispatch) *run.Meta {
	return &run.Meta{
		ID:             d.ID,
		Parent:         d.Parent,
		Role:           d.Role,
		Model:          d.Model,
		Tier:           d.Tier,
		Profile:        d.Profile,
		Status:         d.Status,
		Depth:          d.Depth,
		SupervisorPID:  d.SupervisorPID,
		MaxTurns:       d.MaxTurns,
		TimeoutMinutes: d.TimeoutMinutes,
		StartedAt:      d.StartedAt,
		EndedAt:        d.EndedAt,
		Exit:           d.Exit,
		CostUSD:        d.CostUSD,
		NumTurns:       d.NumTurns,
		SessionID:      d.SessionID,
	}
}

// buildDispatchNodeTree builds a *scratch.DispatchNode tree from a flat list.
func buildDispatchNodeTree(dispatches []*scratch.Dispatch) *scratch.DispatchNode {
	byID := make(map[string]*scratch.DispatchNode, len(dispatches))
	for _, d := range dispatches {
		byID[d.ID] = &scratch.DispatchNode{Dispatch: d}
	}

	var roots []*scratch.DispatchNode
	for _, n := range byID {
		parentID := n.Dispatch.Parent
		if parentID == "" {
			roots = append(roots, n)
		} else if parent, ok := byID[parentID]; ok {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		return dispNodeID(roots[i]) < dispNodeID(roots[j])
	})
	for _, n := range byID {
		sort.Slice(n.Children, func(i, j int) bool {
			return dispNodeID(n.Children[i]) < dispNodeID(n.Children[j])
		})
	}

	if len(roots) == 0 {
		return &scratch.DispatchNode{}
	}
	if len(roots) == 1 {
		return roots[0]
	}
	return &scratch.DispatchNode{Children: roots}
}

func dispNodeID(n *scratch.DispatchNode) string {
	if n.Dispatch != nil {
		return n.Dispatch.ID
	}
	return ""
}

// buildRunSummaryFromRecords builds a run.RunSummary from scratch records for JSON output.
func buildRunSummaryFromRecords(r *scratch.Run, dispatches []*scratch.Dispatch) *run.RunSummary {
	summary := &run.RunSummary{
		RunID:        r.ID,
		Task:         r.Task,
		Status:       r.Status,
		ReasonBudget: r.ReasonBudget,
		PolicySHAs:   r.PolicySHAs,
	}
	if !r.CreatedAt.IsZero() {
		summary.CreatedAt = r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if r.EndedAt != nil {
		summary.EndedAt = r.EndedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	root := buildDispatchNodeTree(dispatches)
	summary.Dispatches = buildDispatchSummariesFromNode(root)
	return summary
}

func buildDispatchSummariesFromNode(n *scratch.DispatchNode) []*run.DispatchSummary {
	if n.Dispatch == nil {
		// Synthetic container.
		var out []*run.DispatchSummary
		for _, child := range n.Children {
			out = append(out, dispatchNodeToSummary(child))
		}
		return out
	}
	return []*run.DispatchSummary{dispatchNodeToSummary(n)}
}

func dispatchNodeToSummary(n *scratch.DispatchNode) *run.DispatchSummary {
	if n.Dispatch == nil {
		return &run.DispatchSummary{}
	}
	d := n.Dispatch
	ds := &run.DispatchSummary{
		ID:       d.ID,
		Parent:   d.Parent,
		Role:     d.Role,
		Model:    d.Model,
		Profile:  d.Profile,
		Status:   d.Status,
		Depth:    d.Depth,
		CostUSD:  d.CostUSD,
		NumTurns: d.NumTurns,
	}
	for _, child := range n.Children {
		ds.Children = append(ds.Children, dispatchNodeToSummary(child))
	}
	return ds
}

// ── spool file reader (for diagnostics / shipping) ────────────────────────────

// SpoolPath returns the path to the local audit spool file for runID+kind.
// Used by diagnostics and shipping code.
func SpoolPath(runID, kind string) string {
	return filepath.Join(os.TempDir(), "tiller-audit-spool", runID+"-"+kind+".jsonl")
}

// ReadSpoolEvents reads all DecisionEvents from the spool file.
// Returns nil, nil if the file does not exist.
func ReadSpoolEvents(runID, kind string) ([]audit.DecisionEvent, error) {
	path := SpoolPath(runID, kind)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []audit.DecisionEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev audit.DecisionEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}
