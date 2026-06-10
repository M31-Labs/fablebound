package fsstore

import (
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/tiller/internal/scratch"
)

// Resolve opens a scratch.Store for the current invocation and returns the
// current run ID. It is the single production constructor; tests should call
// Open directly.
//
// Resolution order:
//  1. TILLER_RUN_DIR — set by `tiller run` / `tiller dispatch`.
//     The parent directory is the runs/ base.
//  2. TILLER_RUN_BASE — explicit override for the runs/ base directory.
//  3. <cwd>/.tiller/runs — default derived from the working directory.
//
// RunID is the last path component of TILLER_RUN_DIR when set, or empty when
// there is no current run context (e.g. top-level CLI commands).
func Resolve() (scratch.Store, string, error) {
	if runDir := os.Getenv("TILLER_RUN_DIR"); runDir != "" {
		if _, err := os.Stat(runDir); err != nil {
			return nil, "", fmt.Errorf("fsstore.Resolve: TILLER_RUN_DIR %q: %w", runDir, err)
		}
		runsBase := filepath.Dir(runDir)
		runID := filepath.Base(runDir)
		return Open(runsBase), runID, nil
	}

	runsBase, err := runsBaseFromEnv()
	if err != nil {
		return nil, "", fmt.Errorf("fsstore.Resolve: %w", err)
	}
	return Open(runsBase), "", nil
}

// ResolveForRun opens the Store and resolves runID. If runID is non-empty
// it is used as-is. Otherwise the run ID is taken from TILLER_RUN_DIR.
// Returns an error if neither is available.
func ResolveForRun(runID string) (scratch.Store, string, error) {
	st, currentID, err := Resolve()
	if err != nil {
		return nil, "", err
	}
	if runID != "" {
		return st, runID, nil
	}
	if currentID == "" {
		return nil, "", fmt.Errorf("fsstore.ResolveForRun: no run context (TILLER_RUN_DIR unset) and no runID provided")
	}
	return st, currentID, nil
}

// runsBaseFromEnv returns the runs/ base directory from TILLER_RUN_BASE or
// the default <cwd>/.tiller/runs.
func runsBaseFromEnv() (string, error) {
	if base := os.Getenv("TILLER_RUN_BASE"); base != "" {
		return base, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return filepath.Join(cwd, ".tiller", "runs"), nil
}
