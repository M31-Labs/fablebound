package claudeheadless_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/adapter/claudeheadless"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// projectRoot returns the repository root directory.
func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../internal/adapter/claudeheadless/claudeheadless_test.go
	// root = 4 levels up
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// buildTiller compiles the tiller binary into a temp dir and returns its path.
func buildTiller(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tiller")
	cmd := exec.Command("go", "build", "-o", bin, "m31labs.dev/tiller/cmd/tiller")
	cmd.Dir = projectRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tiller: %v\n%s", err, out)
	}
	return bin
}

// claudeStub returns the path to the fast-exit claude stub.
func claudeStub(t *testing.T) string {
	t.Helper()
	stub := filepath.Join(projectRoot(t), "testdata", "bin", "claude-stub")
	if _, err := os.Stat(stub); err != nil {
		t.Fatalf("claude-stub not found at %s: %v", stub, err)
	}
	return stub
}

// setupFixture creates a minimal run+dispatch directory tree using fsstore.
// Returns the store, runID, and runDir.
func setupFixture(t *testing.T) (scratch.Store, string, string) {
	t.Helper()
	workspace := t.TempDir()
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Copy policies so dispatch can evaluate them.
	policyDir := filepath.Join(workspace, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := projectRoot(t)
	for _, name := range []string{"dispatch.arb", "toolgate.arb"} {
		data, err := os.ReadFile(filepath.Join(root, "policy", name))
		if err != nil {
			t.Fatalf("read policy %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(policyDir, name), data, 0o644); err != nil {
			t.Fatalf("write policy %s: %v", name, err)
		}
	}

	st := fsstore.Open(runsBase)
	run := &scratch.Run{
		Task:         "claudeheadless adapter test",
		Workspace:    workspace,
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    time.Now(),
	}
	runID, err := st.CreateRun(run)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(runsBase, runID)

	return st, runID, runDir
}

// TestName verifies the adapter returns the correct name.
func TestName(t *testing.T) {
	a := claudeheadless.New("")
	if got := a.Name(); got != "claude-headless" {
		t.Errorf("Name() = %q; want %q", got, "claude-headless")
	}
}

// TestEnforcement verifies the adapter reports "full" enforcement.
func TestEnforcement(t *testing.T) {
	a := claudeheadless.New("")
	if got := a.Enforcement(); got != "full" {
		t.Errorf("Enforcement() = %q; want %q", got, "full")
	}
}

// TestPrepare_WritesSettingsJSON verifies that Prepare writes a valid
// settings.json with BOTH PreToolUse and PostToolUse tiller hook blocks.
func TestPrepare_WritesSettingsJSON(t *testing.T) {
	st, runID, runDir := setupFixture(t)

	dispatchID := "d01"
	dispDir := filepath.Join(runDir, "dispatches", dispatchID)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Role:       "investigator",
		Model:      "opus",
		Profile:    "insight",
		WorkDir:    runDir,
		Depth:      1,
	}

	a := claudeheadless.New("")
	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Read back the written settings.json.
	settingsData, err := st.ReadAdapterConfig(runID, dispatchID)
	if err != nil {
		t.Fatalf("ReadAdapterConfig: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(settingsData, &doc); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}

	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatal("settings.json missing hooks object")
	}

	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		list, ok := hooks[event].([]any)
		if !ok || len(list) == 0 {
			t.Errorf("missing %s hook block", event)
			continue
		}
		block, ok := list[0].(map[string]any)
		if !ok {
			continue
		}
		inner, ok := block["hooks"].([]any)
		if !ok || len(inner) == 0 {
			t.Errorf("%s missing inner hooks list", event)
			continue
		}
		h, ok := inner[0].(map[string]any)
		if !ok {
			continue
		}
		if h["command"] != "tiller hook" {
			t.Errorf("%s hook command = %v; want \"tiller hook\"", event, h["command"])
		}
	}

	// settings.json must contain ≥ 2 occurrences of "tiller hook".
	count := strings.Count(string(settingsData), "tiller hook")
	if count < 2 {
		t.Errorf("settings.json contains %d 'tiller hook' occurrence(s); want ≥ 2", count)
	}
}

// TestPrepare_SetsTier verifies that Prepare passes through the caller-supplied
// Tier verbatim. Since P2.6, Tier is resolved by the caller (dispatch.go via
// tier.Resolve) before Prepare is called; Prepare does not derive it from Model.
func TestPrepare_SetsTier(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		tierInput string
		wantTier  string
	}{
		{"reason tier preserved", "fable", "reason", "reason"},
		{"scrutiny tier preserved", "opus", "scrutiny", "scrutiny"},
		{"execute tier preserved", "sonnet", "execute", "execute"},
		{"explicit execute override", "fable", "execute", "execute"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, runID, runDir := setupFixture(t)

			dispatchID := "d01"
			dispDir := filepath.Join(runDir, "dispatches", dispatchID)
			if err := os.MkdirAll(dispDir, 0o755); err != nil {
				t.Fatal(err)
			}

			spec := &adapter.DispatchSpec{
				Store:      st,
				RunID:      runID,
				DispatchID: dispatchID,
				Role:       "investigator",
				Model:      tc.model,
				Tier:       tc.tierInput,
				Profile:    "insight",
				WorkDir:    runDir,
				Depth:      1,
			}

			a := claudeheadless.New("")
			if err := a.Prepare(context.Background(), spec); err != nil {
				t.Fatalf("Prepare: %v", err)
			}

			if spec.Tier != tc.wantTier {
				t.Errorf("spec.Tier after Prepare = %q; want %q", spec.Tier, tc.wantTier)
			}
		})
	}
}

// TestPrepare_Idempotent verifies that calling Prepare twice on the same spec
// does not error and produces consistent settings.json.
func TestPrepare_Idempotent(t *testing.T) {
	st, runID, runDir := setupFixture(t)

	dispatchID := "d01"
	dispDir := filepath.Join(runDir, "dispatches", dispatchID)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Role:       "investigator",
		Model:      "opus",
		Profile:    "insight",
		WorkDir:    runDir,
		Depth:      1,
	}

	a := claudeheadless.New("")
	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare (first): %v", err)
	}
	first, err := st.ReadAdapterConfig(runID, dispatchID)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare (second): %v", err)
	}
	second, err := st.ReadAdapterConfig(runID, dispatchID)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("Prepare is not idempotent: settings differ between calls")
	}
}

// TestRun_SpawnsAndPolls is an integration test that builds tiller, prepares
// a dispatch, calls Run, and verifies the dispatch completes with the expected
// artifacts on disk.
func TestRun_SpawnsAndPolls(t *testing.T) {
	binary := buildTiller(t)
	stub := claudeStub(t)

	st, runID, runDir := setupFixture(t)

	// Create a root dispatch record (the caller).
	rootDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootDispatch := &scratch.Dispatch{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "orchestrator",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now(),
	}
	if err := st.WriteDispatch(runID, rootDispatch); err != nil {
		t.Fatal(err)
	}

	// Allocate a d01 dispatch.
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	if dispatchID != "d01" {
		t.Fatalf("expected d01, got %s", dispatchID)
	}

	// Write brief.md (simulating what dispatch.go does before calling Prepare).
	if err := st.WriteBrief(runID, dispatchID, []byte("test brief for claudeheadless adapter")); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}

	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Role:       "investigator",
		Model:      "opus",
		Profile:    "insight",
		WorkDir:    runDir,
		Depth:      1,
		MaxTurns:   10,
		Timeout:    0, // no timeout
	}

	a := claudeheadless.New(binary)

	// Prepare: write settings.json.
	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Write dispatch record with tier/enforcement (simulating dispatch.go).
	d := &scratch.Dispatch{
		ID:          dispatchID,
		Parent:      "root",
		Role:        "investigator",
		Model:       "opus",
		Profile:     "insight",
		Status:      "running",
		Depth:       1,
		MaxTurns:    10,
		StartedAt:   time.Now(),
		Tier:        spec.Tier,
		Enforcement: a.Enforcement(),
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	// Run: spawn + poll.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Setenv("TILLER_CLAUDE_BIN", stub)
	result, err := a.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if result.Status != "completed" {
		t.Errorf("result.Status = %q; want completed", result.Status)
	}

	// Verify dispatch record is terminal.
	final, err := st.ReadDispatch(runID, dispatchID)
	if err != nil {
		t.Fatalf("ReadDispatch: %v", err)
	}
	if !final.IsTerminal() {
		t.Errorf("dispatch status = %q; want terminal", final.Status)
	}

	// Verify settings.json ≥ 2 occurrences of "tiller hook" (acceptance criterion).
	settingsData, err := st.ReadAdapterConfig(runID, dispatchID)
	if err != nil {
		t.Fatalf("ReadAdapterConfig: %v", err)
	}
	count := strings.Count(string(settingsData), "tiller hook")
	if count < 2 {
		t.Errorf("settings.json contains %d 'tiller hook'; want ≥ 2", count)
	}
}
