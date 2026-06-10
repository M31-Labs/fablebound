package run

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Store represents a tiller run store rooted at a base directory
// (typically <workspace>/.tiller/runs/).
type Store struct {
	Base string // absolute path to the runs/ directory
}

// NewStore returns a Store rooted at base. The directory must already exist
// or be creatable via EnsureBase.
func NewStore(base string) *Store {
	return &Store{Base: base}
}

// EnsureBase creates the base runs directory if it does not exist.
func (s *Store) EnsureBase() error {
	return os.MkdirAll(s.Base, 0o755)
}

// RunDir returns the absolute path of the run directory for runID.
func (s *Store) RunDir(runID string) string {
	return filepath.Join(s.Base, runID)
}

// CreateRun creates the full directory tree for a new run and returns its
// absolute path.  The run id is freshly generated.
func (s *Store) CreateRun() (string, error) {
	id := NewRunID()
	return id, s.CreateRunWithID(id)
}

// CreateRunWithID creates the directory tree for a run with a specific id.
func (s *Store) CreateRunWithID(id string) error {
	root := s.RunDir(id)

	dirs := []string{
		root,
		filepath.Join(root, "audit"),
		filepath.Join(root, "notes"),
		filepath.Join(root, "dispatches"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create run dir %s: %w", d, err)
		}
	}
	return nil
}

// DispatchDir returns the absolute path of a dispatch sub-directory.
func (s *Store) DispatchDir(runID, dispatchID string) string {
	return filepath.Join(s.RunDir(runID), "dispatches", dispatchID)
}

// CreateDispatch creates the directory for a dispatch and returns its path.
func (s *Store) CreateDispatch(runID, dispatchID string) (string, error) {
	dir := s.DispatchDir(runID, dispatchID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dispatch dir %s: %w", dir, err)
	}
	return dir, nil
}

// dispatchAllocMu is an in-process mutex that guards AllocDispatch across
// goroutines.  Linux flock(2) is per-open-file-description and grants a lock
// to any fd in the same process immediately, so the flock alone does not
// serialise concurrent goroutines within one process.  The two locks together
// cover both in-process (mutex) and cross-process (flock) concurrent callers.
var dispatchAllocMu sync.Mutex

// AllocDispatch atomically allocates the next dNN dispatch ID, creates its
// directory, and returns the ID and directory path.  It holds an in-process
// mutex AND an exclusive flock on <runDir>/.dispatch-alloc.lock across the
// scan+mkdir sequence so that concurrent callers (goroutines within the same
// process, or separate tiller processes) cannot compute the same ID.
//
// The dispatch directory is created with os.Mkdir (not MkdirAll) so that a
// collision between separate processes not sharing the lock file — an
// unexpected condition — surfaces as an error rather than a silent clobber.
func (s *Store) AllocDispatch(runID string) (dispatchID, dispatchDir string, err error) {
	// In-process serialisation (goroutines share the same process and cannot
	// use flock to block each other).
	dispatchAllocMu.Lock()
	defer dispatchAllocMu.Unlock()

	lockPath := filepath.Join(s.RunDir(runID), ".dispatch-alloc.lock")
	lf, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("open dispatch-alloc lock %s: %w", lockPath, err)
	}
	defer lf.Close()

	// Cross-process serialisation via advisory flock.
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return "", "", fmt.Errorf("flock dispatch-alloc lock: %w", err)
	}
	// Lock released on lf.Close() in deferred call above.

	// Compute the next dispatch ID by counting existing dispatch subdirectories
	// (not just those with parseable metas).  Using directory presence rather
	// than meta content is correct here: a directory created by a concurrent
	// AllocDispatch that hasn't written its meta yet still reserves the slot.
	id, err := nextDispatchIDFromDirs(s.RunDir(runID))
	if err != nil {
		return "", "", fmt.Errorf("scan dispatch dirs for alloc: %w", err)
	}

	dir := s.DispatchDir(runID, id)
	// Use os.Mkdir (not MkdirAll) — atomic at the kernel level: if another
	// process somehow produced the same id, Mkdir returns an error rather than
	// silently clobbering.
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir dispatch dir %s: %w", dir, err)
	}

	return id, dir, nil
}

// CurrentRunDir resolves the active run directory from the TILLER_RUN_DIR
// environment variable.  Returns an error if the variable is unset or the path
// does not exist.
func CurrentRunDir() (string, error) {
	d := os.Getenv("TILLER_RUN_DIR")
	if d == "" {
		return "", fmt.Errorf("TILLER_RUN_DIR is not set")
	}
	if _, err := os.Stat(d); err != nil {
		return "", fmt.Errorf("TILLER_RUN_DIR %q: %w", d, err)
	}
	return d, nil
}

// CurrentRunID returns the run id from TILLER_RUN_DIR (the last path
// component).
func CurrentRunID() (string, error) {
	d, err := CurrentRunDir()
	if err != nil {
		return "", err
	}
	return filepath.Base(d), nil
}
