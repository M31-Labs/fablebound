package hyphae

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// makeFixtureRun creates a minimal run fixture under tmpDir and returns the
// run directory path.
func makeFixtureRun(t *testing.T, tmpDir string) string {
	t.Helper()

	runsBase := filepath.Join(tmpDir, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatalf("mkdir runsBase: %v", err)
	}
	st := fsstore.Open(runsBase)

	now := time.Now()
	ended := now.Add(5 * time.Second)
	r := &scratch.Run{
		Task:        "Summarize the codebase architecture.\n\nAdditional context here.",
		Workspace:   tmpDir,
		Status:      "completed",
		FableBudget: 2,
		CreatedAt:   now,
		EndedAt:     &ended,
	}
	runID, err := st.CreateRun(r)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)

	// Create root dispatch.
	rootD := &scratch.Dispatch{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "orchestrator",
		Status:    "completed",
		Depth:     0,
		CostUSD:   0.05,
		StartedAt: now,
	}
	if err := st.WriteBrief(runID, "root", []byte(r.Task)); err != nil {
		t.Fatalf("write root brief: %v", err)
	}
	if err := st.WriteDispatch(runID, rootD); err != nil {
		t.Fatalf("write root dispatch: %v", err)
	}
	// Write root report via the Store.
	if err := st.WriteReport(runID, "root", []byte("The codebase uses a layered architecture with policy-governed dispatch.\n")); err != nil {
		t.Fatalf("write root report: %v", err)
	}

	// Create d01 dispatch via AllocDispatch (produces "d01" as the first allocation).
	d01ID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("alloc d01 dispatch: %v", err)
	}
	d01 := &scratch.Dispatch{
		ID:        d01ID,
		Parent:    "root",
		Role:      "investigator",
		Model:     "sonnet",
		Profile:   "readonly",
		Status:    "completed",
		Depth:     1,
		CostUSD:   0.02,
		StartedAt: now,
	}
	if err := st.WriteDispatch(runID, d01); err != nil {
		t.Fatalf("write d01 dispatch: %v", err)
	}
	if err := st.WriteReport(runID, d01ID, []byte("Investigation complete: the main entry point is cmd/tiller/main.go.\n")); err != nil {
		t.Fatalf("write d01 report: %v", err)
	}

	return runDir
}

// TestPromoteDryRun verifies that --dry-run composes spore.md with the
// dispatch tree and report excerpts, and does NOT call hypha.
func TestPromoteDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	runDir := makeFixtureRun(t, tmpDir)

	// Ensure hypha is not available so we can confirm dry-run doesn't call it.
	orig := os.Getenv("PATH")
	t.Setenv("PATH", "/dev/null/nonexistent")
	defer os.Setenv("PATH", orig)

	opts := SporeOptions{DryRun: true}
	var logs []string
	log := func(format string, _ ...any) { logs = append(logs, format) }

	sporePath, err := Promote(runDir, opts, log)
	if err != nil {
		t.Fatalf("Promote dry-run failed: %v", err)
	}

	if sporePath == "" {
		t.Fatal("Promote should return spore path")
	}

	data, err := os.ReadFile(sporePath)
	if err != nil {
		t.Fatalf("read spore.md: %v", err)
	}
	content := string(data)

	// Must contain required sections.
	for _, section := range []string{"## Task", "## Outcome", "## Dispatch Tree", "## Report Excerpts", "## Lessons"} {
		if !strings.Contains(content, section) {
			t.Errorf("spore.md missing section %q\nContent:\n%s", section, content)
		}
	}

	// Must contain dispatch tree entries.
	if !strings.Contains(content, "root") {
		t.Error("dispatch tree should contain 'root'")
	}
	if !strings.Contains(content, "d01") {
		t.Error("dispatch tree should contain 'd01'")
	}

	// Must contain report excerpts.
	if !strings.Contains(content, "layered architecture") {
		t.Error("spore.md should contain excerpt from root report")
	}
	if !strings.Contains(content, "main entry point") {
		t.Error("spore.md should contain excerpt from d01 report")
	}

	// Task should appear.
	if !strings.Contains(content, "Summarize the codebase") {
		t.Error("spore.md should contain task text")
	}
}

// TestPromoteWithHyphaStub verifies that submit is called with correct argv
// when hypha is available.
func TestPromoteWithHyphaStub(t *testing.T) {
	tmpDir := t.TempDir()
	runDir := makeFixtureRun(t, tmpDir)

	// Create hypha stub.
	stubDir := t.TempDir()
	logFile := filepath.Join(stubDir, "hypha.log")
	stubPath := filepath.Join(stubDir, "hypha")
	stubScript := "#!/bin/sh\necho \"$@\" >> " + logFile + "\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	orig := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+":"+orig)
	defer os.Setenv("PATH", orig)

	opts := SporeOptions{
		Space:  HyphaSpace,
		As:     "identity://odvcencio",
		DryRun: false,
	}
	var logs []string
	log := func(format string, _ ...any) { logs = append(logs, format) }

	sporePath, err := Promote(runDir, opts, log)
	if err != nil {
		t.Fatalf("Promote failed: %v", err)
	}
	if sporePath == "" {
		t.Fatal("expected spore path")
	}

	// Verify hypha stub was called with spore submit args.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read hypha log: %v", err)
	}
	argv := string(data)
	if !strings.Contains(argv, "spore submit") {
		t.Errorf("expected 'spore submit' in hypha argv, got: %s", argv)
	}
	if !strings.Contains(argv, "--sign") {
		t.Errorf("expected '--sign' in hypha argv, got: %s", argv)
	}
	// Note: hypha spore submit does not accept --space (v0.1.9); the space URI
	// must NOT appear in argv.
	if strings.Contains(argv, HyphaSpace) {
		t.Errorf("spore submit argv must not contain --space (unsupported in v0.1.9): %s", argv)
	}
	if !strings.Contains(argv, "identity://odvcencio") {
		t.Errorf("expected --as in hypha argv, got: %s", argv)
	}
}
