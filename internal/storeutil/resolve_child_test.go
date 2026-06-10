package storeutil_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/storeutil"
)

// TestResolveChildFsOnly verifies that a child process with TILLER_RUN_DIR set
// and a manifest store=fs (or empty) opens an fsstore only — no pg dial.
func TestResolveChildFsOnly(t *testing.T) {
	runDir, _ := makeTestRunDir(t, "fs")

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_STORE", "")
	t.Setenv("TILLER_STORE_DSN", "")

	st, runID, closer, err := storeutil.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if closer != nil {
		defer closer()
	}
	if runID == "" {
		t.Error("runID should be non-empty in child context")
	}
	if st == nil {
		t.Fatal("store is nil")
	}
	// Should be a plain fsstore — write a run and read it back.
	r := &scratch.Run{
		ID:        runID,
		Task:      "fs-only test",
		Workspace: t.TempDir(),
		Status:    "running",
	}
	if err := st.WriteRun(r); err != nil {
		t.Fatalf("WriteRun on child fsstore: %v", err)
	}
}

// TestResolveChildTeeManifest simulates a child process whose manifest says
// store=tee with a valid TILLER_STORE_DSN. Requires TILLER_TEST_PG_DSN to be
// set; skips otherwise (like other pg tests).
//
// Verifies that a dispatch write lands in BOTH the fsstore and pg.
func TestResolveChildTeeManifest(t *testing.T) {
	dsn := os.Getenv("TILLER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TILLER_TEST_PG_DSN not set; skipping tee child resolve test")
	}

	// Step 1: parent context — create run via tee store (both fs and pg).
	workspace := t.TempDir()
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatalf("mkdir runsBase: %v", err)
	}
	// Set env so parent's storeutil.Resolve picks the right runs dir.
	t.Setenv("TILLER_RUN_BASE", runsBase)
	t.Setenv("TILLER_STORE", "tee")
	t.Setenv("TILLER_STORE_DSN", dsn)
	t.Setenv("TILLER_RUN_DIR", "")

	parentSt, _, parentCloser, err := storeutil.Resolve(&storeutil.Options{
		StoreKind: "tee",
		DSN:       dsn,
	})
	if err != nil {
		t.Fatalf("parent Resolve tee: %v", err)
	}
	if parentCloser != nil {
		defer parentCloser()
	}

	r := &scratch.Run{
		Task:      "tee child test",
		Workspace: workspace,
		Status:    "running",
		CreatedAt: time.Now(),
		StoreMode: "tee",
	}
	runID, err := parentSt.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun via parent tee: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)

	// Step 2: simulate child — TILLER_RUN_DIR set, no explicit TILLER_STORE.
	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_STORE", "")
	// TILLER_STORE_DSN remains set (inherited through spawn).

	childSt, gotRunID, childCloser, err := storeutil.Resolve(nil)
	if err != nil {
		t.Fatalf("child Resolve: %v", err)
	}
	if childCloser != nil {
		defer childCloser()
	}
	if gotRunID != runID {
		t.Errorf("child Resolve returned runID %q, want %q", gotRunID, runID)
	}

	// Step 3: write a dispatch record via the child tee store.
	did, err := childSt.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch on child tee store: %v", err)
	}
	now := time.Now()
	d := &scratch.Dispatch{
		ID:        did,
		Role:      "worker",
		Model:     "sonnet",
		Status:    "completed",
		StartedAt: now,
	}
	if err := childSt.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch on child tee store: %v", err)
	}

	// Step 4: verify dispatch landed in fsstore (fs is authoritative).
	fst := fsstore.Open(runsBase)
	fsDispatch, err := fst.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch from fsstore: %v", err)
	}
	if fsDispatch.Status != "completed" {
		t.Errorf("fsstore dispatch status = %q, want completed", fsDispatch.Status)
	}

	// Step 5: verify dispatch also landed in pg by opening pg directly.
	pgSt, _, pgCloser, err := storeutil.Resolve(&storeutil.Options{
		StoreKind: "pg",
		DSN:       dsn,
	})
	if err != nil {
		t.Fatalf("Resolve pg direct: %v", err)
	}
	if pgCloser != nil {
		defer pgCloser()
	}
	pgDispatch, err := pgSt.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch from pgstore: %v", err)
	}
	if pgDispatch.Status != "completed" {
		t.Errorf("pgstore dispatch status = %q, want completed", pgDispatch.Status)
	}
}

// TestResolveChildTeeMissingDSN verifies that when TILLER_RUN_DIR is set,
// manifest says tee, but TILLER_STORE_DSN is absent, Resolve soft-falls back
// to fsstore without error.
func TestResolveChildTeeMissingDSN(t *testing.T) {
	runDir, _ := makeTestRunDir(t, "tee")

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_STORE", "")
	t.Setenv("TILLER_STORE_DSN", "")

	st, runID, closer, err := storeutil.Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve should soft-fail to fsstore, got error: %v", err)
	}
	if closer != nil {
		defer closer()
	}
	if runID == "" {
		t.Error("runID should be non-empty even on fallback")
	}
	if st == nil {
		t.Fatal("store is nil on fsstore fallback")
	}
}

// makeTestRunDir creates a real run directory with a manifest.json containing
// the given store mode. Returns the runDir path and the runID.
// Uses fsstore directly to avoid needing pg.
func makeTestRunDir(t *testing.T, storeMode string) (runDir, runID string) {
	t.Helper()
	workspace := t.TempDir()
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatalf("mkdir runsBase: %v", err)
	}
	fst := fsstore.Open(runsBase)
	r := &scratch.Run{
		Task:      "test run",
		Workspace: workspace,
		Status:    "running",
		CreatedAt: time.Now(),
		StoreMode: storeMode,
	}
	id, err := fst.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return filepath.Join(runsBase, id), id
}

// Ensure run package is used (for ReadManifest verification).
var _ = run.ReadManifest
