package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/run"
)

// makeTestRun creates a run fixture with the given status and age offset.
// age is applied as a negative offset to CreatedAt for sorting purposes.
func makeTestRun(t *testing.T, runsBase string, status string, ageOffset time.Duration) string {
	t.Helper()
	store := run.NewStore(runsBase)
	runID, err := store.CreateRun()
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	runDir := store.RunDir(runID)

	now := time.Now().Add(-ageOffset)
	manifest := &run.Manifest{
		RunID:       runID,
		Task:        fmt.Sprintf("test task (%s)", status),
		Workspace:   runsBase,
		Status:      status,
		FableBudget: 2,
		CreatedAt:   now,
	}
	if status != "running" {
		ended := now.Add(1 * time.Second)
		manifest.EndedAt = &ended
	}
	if err := run.WriteManifest(runDir, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return runID
}

// TestRunsGC_KeepsRunningRuns verifies that `runs gc` never deletes running runs.
func TestRunsGC_KeepsRunningRuns(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	runsBase := filepath.Join(tmpDir, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 3 completed runs and 1 running run.
	makeTestRun(t, runsBase, "completed", 3*time.Hour)
	makeTestRun(t, runsBase, "completed", 2*time.Hour)
	makeTestRun(t, runsBase, "completed", 1*time.Hour)
	runningID := makeTestRun(t, runsBase, "running", 30*time.Minute)

	// gc --keep 1 should delete 2 oldest completed; keep the newest and the running.
	if err := runRunsGC([]string{"--keep", "1"}); err != nil {
		t.Fatalf("runRunsGC: %v", err)
	}

	// Running run must still exist.
	runningDir := filepath.Join(runsBase, runningID)
	if _, err := os.Stat(runningDir); err != nil {
		t.Errorf("running run %s was deleted by gc (must never happen): %v", runningID, err)
	}

	// Count remaining runs.
	entries, err := os.ReadDir(runsBase)
	if err != nil {
		t.Fatal(err)
	}
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		}
	}

	// Should have: 1 kept completed + 1 running = 2 total.
	if dirCount != 2 {
		t.Errorf("expected 2 runs after gc --keep 1, got %d", dirCount)
	}
}

// TestRunsGC_DryRun verifies that --dry-run lists victims without deleting.
func TestRunsGC_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	runsBase := filepath.Join(tmpDir, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 5 completed runs.
	for i := 0; i < 5; i++ {
		makeTestRun(t, runsBase, "completed", time.Duration(5-i)*time.Hour)
	}

	// --dry-run with --keep 2: should list 3 victims without deleting anything.
	if err := runRunsGC([]string{"--keep", "2", "--dry-run"}); err != nil {
		t.Fatalf("runRunsGC --dry-run: %v", err)
	}

	// All 5 runs should still exist.
	entries, err := os.ReadDir(runsBase)
	if err != nil {
		t.Fatal(err)
	}
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		}
	}
	if dirCount != 5 {
		t.Errorf("expected 5 runs after --dry-run, got %d (dry-run should not delete)", dirCount)
	}
}

// TestRunsGC_NoVictims verifies gc is a no-op when below keep threshold.
func TestRunsGC_NoVictims(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	runsBase := filepath.Join(tmpDir, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	makeTestRun(t, runsBase, "completed", 1*time.Hour)
	makeTestRun(t, runsBase, "failed", 2*time.Hour)

	// --keep 10: nothing to delete.
	if err := runRunsGC([]string{"--keep", "10"}); err != nil {
		t.Fatalf("runRunsGC: %v", err)
	}

	entries, err := os.ReadDir(runsBase)
	if err != nil {
		t.Fatal(err)
	}
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		}
	}
	if dirCount != 2 {
		t.Errorf("expected 2 runs, got %d", dirCount)
	}
}
