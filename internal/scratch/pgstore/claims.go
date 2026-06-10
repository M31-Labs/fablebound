package pgstore

// claims.go — pgstore implementation of scratch.Store claim semantics (P4.1).
//
// All operations use single-statement CAS UPDATEs with WHERE-clause guards so
// that concurrent claimants remain race-free without advisory locks.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"m31labs.dev/tiller/internal/scratch"
)

// ClaimDispatch attempts to claim dispatchID for executor via a single CAS UPDATE.
// The UPDATE only matches rows with status='pending'; exactly one concurrent
// caller will see rowsAffected==1.
func (s *Store) ClaimDispatch(runID, dispatchID, executor string, lease time.Duration) (bool, error) {
	deadline := time.Now().UTC().Add(lease)
	res, err := s.db.db.ExecContext(context.Background(), `
		UPDATE dispatch
		   SET status      = 'claimed',
		       claimed_by  = $3,
		       lease_until = $4
		 WHERE run_id = $1 AND id = $2 AND status = 'pending'`,
		runID, dispatchID, executor, deadline,
	)
	if err != nil {
		return false, fmt.Errorf("pgstore.ClaimDispatch %s/%s: %w", runID, dispatchID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("pgstore.ClaimDispatch: rows affected: %w", err)
	}
	return n == 1, nil
}

// RenewLease extends the lease for the current holder (executor must match claimed_by).
func (s *Store) RenewLease(runID, dispatchID, executor string, lease time.Duration) error {
	deadline := time.Now().UTC().Add(lease)
	res, err := s.db.db.ExecContext(context.Background(), `
		UPDATE dispatch
		   SET lease_until = $4
		 WHERE run_id = $1 AND id = $2 AND claimed_by = $3 AND status = 'claimed'`,
		runID, dispatchID, executor, deadline,
	)
	if err != nil {
		return fmt.Errorf("pgstore.RenewLease %s/%s: %w", runID, dispatchID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pgstore.RenewLease: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("pgstore.RenewLease %s/%s: not claimed by %q (or not in claimed status)", runID, dispatchID, executor)
	}
	return nil
}

// ReleaseDispatch sets terminalStatus and clears claim fields.
// Returns an error if terminalStatus is not a terminal value.
func (s *Store) ReleaseDispatch(runID, dispatchID, executor, terminalStatus string) error {
	if !pgIsTerminalStatus(terminalStatus) {
		return fmt.Errorf("pgstore.ReleaseDispatch: %q is not a terminal status", terminalStatus)
	}
	now := time.Now().UTC()
	res, err := s.db.db.ExecContext(context.Background(), `
		UPDATE dispatch
		   SET status      = $4,
		       claimed_by  = '',
		       lease_until = NULL,
		       ended_at    = $5
		 WHERE run_id = $1 AND id = $2 AND claimed_by = $3`,
		runID, dispatchID, executor, terminalStatus, now,
	)
	if err != nil {
		return fmt.Errorf("pgstore.ReleaseDispatch %s/%s: %w", runID, dispatchID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pgstore.ReleaseDispatch: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("pgstore.ReleaseDispatch %s/%s: dispatch not found or not claimed by %q", runID, dispatchID, executor)
	}
	return nil
}

// ExpireLeases re-queues all claimed dispatches for runID whose lease has expired.
// Returns the IDs of re-queued dispatches.
func (s *Store) ExpireLeases(runID string) ([]string, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		UPDATE dispatch
		   SET status      = 'pending',
		       claimed_by  = '',
		       lease_until = NULL
		 WHERE run_id = $1
		   AND status = 'claimed'
		   AND lease_until < now()
		 RETURNING id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ExpireLeases %s: %w", runID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, fmt.Errorf("pgstore.ExpireLeases: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return ids, fmt.Errorf("pgstore.ExpireLeases: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

// ListPendingDispatches returns all dispatches with status "pending" in alloc order.
func (s *Store) ListPendingDispatches(runID string) ([]*scratch.Dispatch, error) {
	rows, err := s.db.db.QueryContext(context.Background(), `
		SELECT id, parent_id, role, model, profile, status, depth,
		       supervisor_pid, max_turns, timeout_minutes, started_at, ended_at,
		       exit_code, cost_usd, num_turns, session_id, tier, enforcement,
		       claimed_by, lease_until, adapter_name, provider
		  FROM dispatch
		 WHERE run_id = $1 AND status = 'pending'
		 ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore.ListPendingDispatches %s: %w", runID, err)
	}
	defer rows.Close()

	var out []*scratch.Dispatch
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore.ListPendingDispatches scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// pgIsTerminalStatus mirrors the fsstore helper.
func pgIsTerminalStatus(s string) bool {
	switch s {
	case "completed", "failed", "halted", "stale":
		return true
	}
	return false
}
