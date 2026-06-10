package run

import (
	"fmt"
	"os"
	"path/filepath"
)

// Store represents a fablebound run store rooted at a base directory
// (typically <workspace>/.fablebound/runs/).
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

// CurrentRunDir resolves the active run directory from the FABLEBOUND_RUN_DIR
// environment variable.  Returns an error if the variable is unset or the path
// does not exist.
func CurrentRunDir() (string, error) {
	d := os.Getenv("FABLEBOUND_RUN_DIR")
	if d == "" {
		return "", fmt.Errorf("FABLEBOUND_RUN_DIR is not set")
	}
	if _, err := os.Stat(d); err != nil {
		return "", fmt.Errorf("FABLEBOUND_RUN_DIR %q: %w", d, err)
	}
	return d, nil
}

// CurrentRunID returns the run id from FABLEBOUND_RUN_DIR (the last path
// component).
func CurrentRunID() (string, error) {
	d, err := CurrentRunDir()
	if err != nil {
		return "", err
	}
	return filepath.Base(d), nil
}
