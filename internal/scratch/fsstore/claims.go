package fsstore

// claims.go — fsstore implementation of scratch.Store claim semantics (P4.1).
//
// CAS mechanism (2-line summary):
//   os.OpenFile(claimPath, O_WRONLY|O_CREATE|O_EXCL, 0o644) races all claimants;
//   only one succeeds (EEXIST for losers), then meta.json is updated under flock.
//
// The claim file lives at dispatches/<id>/claim and contains a JSON payload
// with executor and lease deadline. The dispatch meta.json is always the
// authoritative status record; the claim file is a sentinel for the CAS race.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/scratch"
)

// claimPayload is the JSON written to the claim file.
type claimPayload struct {
	Executor   string    `json:"executor"`
	LeaseUntil time.Time `json:"lease_until"`
}

// claimPath returns the path to the claim sentinel file for a dispatch.
func (fs *FS) claimPath(runID, dispatchID string) string {
	return filepath.Join(fs.dispatchDir(runID, dispatchID), "claim")
}

// metaPath returns the path to meta.json for a dispatch.
func (fs *FS) metaPath(runID, dispatchID string) string {
	return filepath.Join(fs.runDir(runID), "dispatches", dispatchID, "meta.json")
}

// ClaimDispatch attempts an O_CREAT|O_EXCL CAS on the claim sentinel file.
// If this process wins the race, it writes the claim payload and updates
// meta.json under a write lock. Losers receive EEXIST and return (false, nil).
func (fs *FS) ClaimDispatch(runID, dispatchID, executor string, lease time.Duration) (bool, error) {
	deadline := time.Now().UTC().Add(lease)
	cp := fs.claimPath(runID, dispatchID)

	// Ensure dispatch directory exists.
	if err := os.MkdirAll(filepath.Dir(cp), 0o755); err != nil {
		return false, fmt.Errorf("fsstore.ClaimDispatch: mkdir: %w", err)
	}

	// CAS: O_CREAT|O_EXCL — exactly one caller succeeds.
	f, err := os.OpenFile(cp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil // lost the race
		}
		return false, fmt.Errorf("fsstore.ClaimDispatch: create claim file: %w", err)
	}

	// Write the claim payload to the sentinel file.
	payload := claimPayload{Executor: executor, LeaseUntil: deadline}
	enc := json.NewEncoder(f)
	if err := enc.Encode(payload); err != nil {
		f.Close()
		os.Remove(cp) // clean up on write failure
		return false, fmt.Errorf("fsstore.ClaimDispatch: write claim payload: %w", err)
	}
	f.Close()

	// Update meta.json under flock to record status=claimed, claimed_by, lease_until.
	if err := fs.updateDispatchClaimed(runID, dispatchID, executor, deadline); err != nil {
		os.Remove(cp) // roll back claim file if meta update fails
		return false, fmt.Errorf("fsstore.ClaimDispatch: update meta: %w", err)
	}
	return true, nil
}

// updateDispatchClaimed reads meta.json, verifies status==pending, and writes
// back with status=claimed. Uses flock via flockUpdateMeta.
func (fs *FS) updateDispatchClaimed(runID, dispatchID, executor string, deadline time.Time) error {
	return fs.flockUpdateMeta(runID, dispatchID, func(d *scratch.Dispatch) error {
		if d.Status != "pending" {
			return fmt.Errorf("dispatch %s status=%q (want pending)", dispatchID, d.Status)
		}
		d.Status = "claimed"
		d.ClaimedBy = executor
		d.LeaseUntil = &deadline
		return nil
	})
}

// RenewLease extends the lease deadline for the current holder.
func (fs *FS) RenewLease(runID, dispatchID, executor string, lease time.Duration) error {
	deadline := time.Now().UTC().Add(lease)

	if err := fs.flockUpdateMeta(runID, dispatchID, func(d *scratch.Dispatch) error {
		if d.ClaimedBy != executor {
			return fmt.Errorf("fsstore.RenewLease: claimed_by=%q, not %q", d.ClaimedBy, executor)
		}
		if d.Status != "claimed" {
			return fmt.Errorf("fsstore.RenewLease: status=%q (want claimed)", d.Status)
		}
		d.LeaseUntil = &deadline
		return nil
	}); err != nil {
		return err
	}

	// Also update the claim file so inspectors see the new deadline.
	cp := fs.claimPath(runID, dispatchID)
	payload := claimPayload{Executor: executor, LeaseUntil: deadline}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fsstore.RenewLease: marshal payload: %w", err)
	}
	// O_TRUNC so we replace in-place; this is idempotent.
	return os.WriteFile(cp, data, 0o644)
}

// ReleaseDispatch sets the dispatch to a terminal status and removes the claim.
func (fs *FS) ReleaseDispatch(runID, dispatchID, executor, terminalStatus string) error {
	if !isTerminalStatus(terminalStatus) {
		return fmt.Errorf("fsstore.ReleaseDispatch: %q is not a terminal status", terminalStatus)
	}

	now := time.Now().UTC()
	if err := fs.flockUpdateMeta(runID, dispatchID, func(d *scratch.Dispatch) error {
		if d.ClaimedBy != executor {
			return fmt.Errorf("fsstore.ReleaseDispatch: claimed_by=%q, not %q", d.ClaimedBy, executor)
		}
		d.Status = terminalStatus
		d.ClaimedBy = ""
		d.LeaseUntil = nil
		d.EndedAt = &now
		return nil
	}); err != nil {
		return err
	}

	// Remove the claim sentinel.
	cp := fs.claimPath(runID, dispatchID)
	if err := os.Remove(cp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstore.ReleaseDispatch: remove claim file: %w", err)
	}
	return nil
}

// ExpireLeases scans all dispatch directories for claimed dispatches whose
// lease_until is in the past, and re-queues them to "pending".
// It reads raw meta.json (via readDispatchRaw) rather than going through
// ListDispatches, because ListDispatches delegates to run.ScanMetas which
// maps to run.Meta — a struct that does not carry LeaseUntil/ClaimedBy.
func (fs *FS) ExpireLeases(runID string) ([]string, error) {
	dispatchesDir := filepath.Join(fs.runDir(runID), "dispatches")
	entries, err := os.ReadDir(dispatchesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsstore.ExpireLeases %s: readdir: %w", runID, err)
	}

	now := time.Now().UTC()
	var requeued []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dispatchID := e.Name()

		// Read the raw dispatch (preserves v2 fields: ClaimedBy, LeaseUntil).
		d, err := readDispatchRaw(fs.runDir(runID), dispatchID)
		if err != nil {
			continue // skip corrupt / partial writes
		}
		if d.Status != "claimed" {
			continue
		}
		if d.LeaseUntil == nil || !d.LeaseUntil.Before(now) {
			continue
		}

		// Re-queue under flock; re-check under lock to avoid races.
		var requeued1 bool
		if err := fs.flockUpdateMeta(runID, dispatchID, func(cur *scratch.Dispatch) error {
			if cur.Status != "claimed" || cur.LeaseUntil == nil || !cur.LeaseUntil.Before(now) {
				return nil // raced with a renewal or release; skip
			}
			cur.Status = "pending"
			cur.ClaimedBy = ""
			cur.LeaseUntil = nil
			requeued1 = true
			return nil
		}); err != nil {
			return requeued, fmt.Errorf("fsstore.ExpireLeases: requeue %s: %w", dispatchID, err)
		}
		if requeued1 {
			// Remove the claim sentinel.
			_ = os.Remove(fs.claimPath(runID, dispatchID))
			requeued = append(requeued, dispatchID)
		}
	}
	sort.Strings(requeued)
	return requeued, nil
}

// ListPendingDispatches returns all pending dispatches in ascending ID order.
// Reads raw meta.json to preserve Status accurately (ListDispatches uses
// run.ScanMetas which maps to run.Meta and loses claimed_by/lease_until; the
// status field itself is preserved by run.Meta, so the filter is correct, but
// we read raw here for consistency with ExpireLeases).
func (fs *FS) ListPendingDispatches(runID string) ([]*scratch.Dispatch, error) {
	dispatchesDir := filepath.Join(fs.runDir(runID), "dispatches")
	entries, err := os.ReadDir(dispatchesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsstore.ListPendingDispatches %s: readdir: %w", runID, err)
	}

	var out []*scratch.Dispatch
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, err := readDispatchRaw(fs.runDir(runID), e.Name())
		if err != nil {
			continue
		}
		if d.Status == "pending" {
			out = append(out, d)
		}
	}
	// entries from os.ReadDir are already in lexicographic order (d01, d02, …).
	return out, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// flockUpdateMeta reads meta.json, calls fn(d) to mutate d, then writes it
// back — all under an exclusive flock on the meta.json file itself.
// fn must not return an error for a no-op update (just leave d unchanged).
func (fs *FS) flockUpdateMeta(runID, dispatchID string, fn func(*scratch.Dispatch) error) error {
	path := fs.metaPath(runID, dispatchID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("flockUpdateMeta mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("flockUpdateMeta open %s: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flockUpdateMeta flock: %w", err)
	}

	// Read current state.
	var d scratch.Dispatch
	dec := json.NewDecoder(f)
	if err := dec.Decode(&d); err != nil {
		// If file is empty (just created), start with a zero-value dispatch.
		if err.Error() != "EOF" {
			return fmt.Errorf("flockUpdateMeta decode: %w", err)
		}
	}

	// Mutate.
	if err := fn(&d); err != nil {
		return err
	}

	// Truncate and rewrite.
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("flockUpdateMeta truncate: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("flockUpdateMeta seek: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// isTerminalStatus returns true for terminal dispatch status values.
func isTerminalStatus(s string) bool {
	switch s {
	case "completed", "failed", "halted", "stale":
		return true
	}
	return false
}
