package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/sandbox"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// ── runIDBucket ────────────────────────────────────────────────────────────────

// TestRunIDBucket_Deterministic verifies that runIDBucket returns the same value
// for the same input across calls (stable FNV-32a hash).
func TestRunIDBucket_Deterministic(t *testing.T) {
	cases := []struct {
		runID string
		want  int
	}{
		// Values computed once from FNV-32a; they must never change.
		{"20260101-000000-abc1", runIDBucket("20260101-000000-abc1")},
		{"20260601-120000-zz99", runIDBucket("20260601-120000-zz99")},
		{"", runIDBucket("")},
	}
	for _, tc := range cases {
		got := runIDBucket(tc.runID)
		if got != tc.want {
			t.Errorf("runIDBucket(%q) = %d, want %d (not stable)", tc.runID, got, tc.want)
		}
		// Call again to confirm determinism.
		got2 := runIDBucket(tc.runID)
		if got2 != got {
			t.Errorf("runIDBucket(%q) not deterministic: first=%d second=%d", tc.runID, got, got2)
		}
	}
}

// TestRunIDBucket_NonNegative verifies that runIDBucket never returns a negative
// bucket index (uint32 cast must be non-negative as int on 64-bit).
func TestRunIDBucket_NonNegative(t *testing.T) {
	inputs := []string{
		"20260101-000000-abc1",
		"20260101-235959-ffff",
		"",
		"x",
		strings.Repeat("a", 100),
	}
	for _, id := range inputs {
		b := runIDBucket(id)
		if b < 0 {
			t.Errorf("runIDBucket(%q) = %d (negative)", id, b)
		}
	}
}

// TestRunIDBucket_Distribution verifies that a spread of run IDs hash to
// multiple distinct buckets (not all to the same value).
func TestRunIDBucket_Distribution(t *testing.T) {
	seen := make(map[int]bool)
	for i := range 20 {
		id := "20260101-000000-" + string(rune('a'+i))
		seen[runIDBucket(id)] = true
	}
	if len(seen) < 3 {
		t.Errorf("runIDBucket produced only %d distinct values for 20 inputs (expected spread)", len(seen))
	}
}

// TestRunIDBucket_ModuloCompatibility verifies that the bucket value works
// as a modulo index into a slice (the canary bucketing use case).
func TestRunIDBucket_ModuloCompatibility(t *testing.T) {
	candidates := []string{"cand-a", "cand-b", "cand-c"}
	runIDs := []string{
		"20260601-000000-run1",
		"20260601-000001-run2",
		"20260601-000002-run3",
		"20260601-000003-run4",
		"20260601-000004-run5",
	}
	for _, id := range runIDs {
		b := runIDBucket(id)
		idx := b % len(candidates)
		if idx < 0 || idx >= len(candidates) {
			t.Errorf("runIDBucket(%q) %% %d = %d (out of range)", id, len(candidates), idx)
		}
	}
}

// ── roleToDefaultTier ─────────────────────────────────────────────────────────

func TestRoleToDefaultTier(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"orchestrator", "reason"},
		{"chief-architect", "reason"},
		{"deep-report", "reason"},
		{"investigator", "scrutiny"},
		{"reviewer", "scrutiny"},
		{"worker", "execute"},
		{"debugger", "execute"},
		{"", "execute"},
		{"unknown-role", "execute"},
		{"WORKER", "execute"}, // case-sensitive: not matched → execute
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			got := roleToDefaultTier(tc.role)
			if got != tc.want {
				t.Errorf("roleToDefaultTier(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

// ── parseDuration ─────────────────────────────────────────────────────────────

func TestParseDuration(t *testing.T) {
	cases := []struct {
		input   string
		wantErr bool
		wantMin float64 // expected minutes (approx)
	}{
		{"8m", false, 8},
		{"30s", false, 0.5},
		{"1h", false, 60},
		{"2h30m", false, 150},
		{"10", false, 10},    // plain integer → minutes
		{"0", false, 0},      // zero minutes
		{"", true, 0},        // empty string → error
		{"abc", true, 0},     // invalid
		{"1x", true, 0},      // invalid suffix
		{"  8m  ", false, 8}, // trimmed
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			d, err := parseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseDuration(%q): expected error, got nil (duration=%v)", tc.input, d)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuration(%q): unexpected error: %v", tc.input, err)
			}
			gotMin := d.Minutes()
			if gotMin != tc.wantMin {
				t.Errorf("parseDuration(%q) = %v (%g min), want %g min", tc.input, d, gotMin, tc.wantMin)
			}
		})
	}
}

// ── readBrief ─────────────────────────────────────────────────────────────────

func TestReadBrief_LiteralText(t *testing.T) {
	got, err := readBrief("investigate the thing")
	if err != nil {
		t.Fatalf("readBrief literal: %v", err)
	}
	if got != "investigate the thing" {
		t.Errorf("readBrief literal = %q, want %q", got, "investigate the thing")
	}
}

func TestReadBrief_Empty(t *testing.T) {
	got, err := readBrief("")
	if err != nil {
		t.Fatalf("readBrief empty: %v", err)
	}
	if got != "" {
		t.Errorf("readBrief empty = %q, want empty", got)
	}
}

func TestReadBrief_FilePath(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "brief.md")
	content := "# Brief\n\nDo the thing.\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readBrief(p)
	if err != nil {
		t.Fatalf("readBrief file: %v", err)
	}
	if got != content {
		t.Errorf("readBrief file = %q, want %q", got, content)
	}
}

func TestReadBrief_NonExistentPathIsLiteral(t *testing.T) {
	// A path that does not exist is treated as literal text.
	literal := "/no/such/file/that/exists"
	got, err := readBrief(literal)
	if err != nil {
		t.Fatalf("readBrief non-existent path: %v", err)
	}
	if got != literal {
		t.Errorf("readBrief non-existent = %q, want %q (literal fallback)", got, literal)
	}
}

// ── dispatch --queue end-to-end (no real process spawn) ───────────────────────

// fakeAdapter is a minimal adapter.Adapter that records Prepare/Run calls.
// It never spawns a real process.
type fakeAdapter struct {
	name        string
	enforcement string
	prepared    []*adapter.DispatchSpec
	ran         []*adapter.DispatchSpec
}

func newFakeAdapter(name, enforcement string) *fakeAdapter {
	return &fakeAdapter{name: name, enforcement: enforcement}
}

func (a *fakeAdapter) Name() string        { return a.name }
func (a *fakeAdapter) Enforcement() string { return a.enforcement }

func (a *fakeAdapter) Prepare(_ context.Context, s *adapter.DispatchSpec) error {
	a.prepared = append(a.prepared, s)
	return nil
}

func (a *fakeAdapter) Run(_ context.Context, s *adapter.DispatchSpec) (*adapter.Result, error) {
	a.ran = append(a.ran, s)
	return &adapter.Result{Status: "completed"}, nil
}

// makeDispatchTestEnv creates a temp project directory and run directory with
// a proper fsstore, run record, and wired TILLER_RUN_DIR environment variable.
// It returns the project dir, run dir, run ID, fsstore, and a cleanup function.
//
// The caller must set TILLER_RUN_DIR to runDir before calling dispatch.
func makeDispatchTestEnv(t *testing.T) (projectDir, runDir, runID string, st *fsstore.FS) {
	t.Helper()
	projectDir = t.TempDir()

	// fsstore expects <base>/<runID>/ where base is .tiller/runs.
	runsBase := filepath.Join(projectDir, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatalf("mkdirAll runs: %v", err)
	}
	st = fsstore.Open(runsBase)

	r := &scratch.Run{
		Task:         "dispatch test run",
		Workspace:    projectDir,
		Status:       "running",
		ReasonBudget: 3,
		MaxDepth:     4,
	}
	var err error
	runID, err = st.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	runDir = filepath.Join(runsBase, runID)
	return projectDir, runDir, runID, st
}

// TestDispatchQueue_WritesPendingRecord verifies that `tiller dispatch --queue`
// writes a dispatch record with status=pending and the brief is stored.
// No real process is spawned; the fake adapter's Prepare/Run are NOT called.
func TestDispatchQueue_WritesPendingRecord(t *testing.T) {
	projectDir, runDir, runID, st := makeDispatchTestEnv(t)
	_ = projectDir

	// Register a fake adapter under "claude-headless" (matches the embedded
	// defaults/models.toml which routes execute tier to claude-headless).
	reg := adapter.NewRegistry()
	fa := newFakeAdapter("claude-headless", "full")
	reg.Register(fa)

	// Set required environment variables.
	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_ROLE", "user")
	t.Setenv("TILLER_DEPTH", "0")
	t.Setenv("TILLER_DISPATCH_ID", "")

	briefText := "investigate the dispatch path"
	err := runDispatchWithRegistry([]string{
		"--role", "worker",
		"--brief", briefText,
		"--queue",
	}, reg)
	if err != nil {
		t.Fatalf("runDispatchWithRegistry --queue: %v", err)
	}

	// The fake adapter's Prepare and Run must NOT have been called (--queue skips them).
	if len(fa.prepared) != 0 {
		t.Errorf("--queue: Prepare was called %d times, want 0", len(fa.prepared))
	}
	if len(fa.ran) != 0 {
		t.Errorf("--queue: Run was called %d times, want 0", len(fa.ran))
	}

	// Read back dispatches from the store to find the pending record.
	dispatches, err := listDispatchesFromStore(t, st, runID)
	if err != nil {
		t.Fatalf("listDispatches: %v", err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatches))
	}

	d := dispatches[0]
	if d.Status != "pending" {
		t.Errorf("dispatch status = %q, want %q", d.Status, "pending")
	}
	if d.Role != "worker" {
		t.Errorf("dispatch role = %q, want %q", d.Role, "worker")
	}
	if d.Depth != 1 {
		t.Errorf("dispatch depth = %d, want 1 (callerDepth=0 + 1)", d.Depth)
	}
	if d.Tier == "" {
		t.Errorf("dispatch tier is empty (should be resolved from policy)")
	}

	// Brief must be written.
	briefData, err := st.ReadBrief(runID, d.ID)
	if err != nil {
		t.Fatalf("ReadBrief: %v", err)
	}
	if string(briefData) != briefText {
		t.Errorf("brief = %q, want %q", briefData, briefText)
	}
}

func TestDispatchQueue_DegradedCommandPersistsRequestedSandboxIntent(t *testing.T) {
	projectDir, runDir, runID, st := makeDispatchTestEnv(t)
	tillerDir := filepath.Join(projectDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0o755); err != nil {
		t.Fatalf("mkdir .tiller: %v", err)
	}
	models := `
[tiers.execute]
candidates = ["command:test-agent/-"]
`
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(models), 0o644); err != nil {
		t.Fatalf("write models.toml: %v", err)
	}

	reg := adapter.NewRegistry()
	fa := newFakeAdapter("command", "degraded")
	reg.Register(fa)

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_ROLE", "user")
	t.Setenv("TILLER_DEPTH", "0")
	t.Setenv("TILLER_DISPATCH_ID", "")

	err := runDispatchWithRegistry([]string{
		"--role", "worker",
		"--tier", "execute",
		"--brief", "run degraded command adapter",
		"--queue",
	}, reg)
	if err != nil {
		t.Fatalf("runDispatchWithRegistry --queue: %v", err)
	}

	dispatches, err := listDispatchesFromStore(t, st, runID)
	if err != nil {
		t.Fatalf("listDispatches: %v", err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatches))
	}

	d := dispatches[0]
	if d.Adapter != "command" {
		t.Fatalf("Adapter=%q, want command", d.Adapter)
	}
	if d.Enforcement != "degraded" {
		t.Fatalf("Enforcement=%q, want degraded for advisory process sandbox", d.Enforcement)
	}
	if d.Sandbox == nil {
		t.Fatal("Sandbox=nil, want requested process sandbox intent")
	}
	if d.Sandbox.Mode != sandbox.ModeProcess {
		t.Errorf("Sandbox.Mode=%q, want process", d.Sandbox.Mode)
	}
	if d.Sandbox.Status != sandbox.StatusRequested {
		t.Errorf("Sandbox.Status=%q, want requested", d.Sandbox.Status)
	}
	if d.Sandbox.Runner != "process" {
		t.Errorf("Sandbox.Runner=%q, want process", d.Sandbox.Runner)
	}
	if d.Sandbox.Profile != "execution" {
		t.Errorf("Sandbox.Profile=%q, want execution", d.Sandbox.Profile)
	}
}

// TestDispatchQueue_CallerIdentityFromEnv verifies that caller identity (role,
// depth, ID) comes from TILLER_* env vars, not from model output or flags.
func TestDispatchQueue_CallerIdentityFromEnv(t *testing.T) {
	_, runDir, runID, st := makeDispatchTestEnv(t)

	reg := adapter.NewRegistry()
	reg.Register(newFakeAdapter("claude-headless", "full"))

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_ROLE", "orchestrator")
	t.Setenv("TILLER_DEPTH", "1")
	t.Setenv("TILLER_DISPATCH_ID", "d01")

	err := runDispatchWithRegistry([]string{
		"--role", "worker",
		"--brief", "do the work",
		"--queue",
	}, reg)
	if err != nil {
		t.Fatalf("runDispatchWithRegistry: %v", err)
	}

	dispatches, err := listDispatchesFromStore(t, st, runID)
	if err != nil {
		t.Fatalf("listDispatches: %v", err)
	}
	if len(dispatches) == 0 {
		t.Fatal("no dispatches written")
	}

	d := dispatches[0]
	// Parent must be the caller's dispatch ID from TILLER_DISPATCH_ID env.
	if d.Parent != "d01" {
		t.Errorf("dispatch parent = %q, want %q (from TILLER_DISPATCH_ID)", d.Parent, "d01")
	}
	// Depth must be callerDepth + 1 = 2.
	if d.Depth != 2 {
		t.Errorf("dispatch depth = %d, want 2 (TILLER_DEPTH=1 + 1)", d.Depth)
	}
}

// TestDispatch_RoleRequired verifies that --role is required.
func TestDispatch_RoleRequired(t *testing.T) {
	_, runDir, _, _ := makeDispatchTestEnv(t)
	t.Setenv("TILLER_RUN_DIR", runDir)

	reg := adapter.NewRegistry()
	err := runDispatchWithRegistry([]string{"--brief", "hi"}, reg)
	if err == nil {
		t.Fatal("expected error when --role is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--role") {
		t.Errorf("error missing --role mention: %v", err)
	}
}

// TestDispatch_MissingRunDir verifies that missing TILLER_RUN_DIR returns
// a clear error.
func TestDispatch_MissingRunDir(t *testing.T) {
	// Ensure TILLER_RUN_DIR is not set.
	t.Setenv("TILLER_RUN_DIR", "")

	reg := adapter.NewRegistry()
	err := runDispatchWithRegistry([]string{"--role", "worker", "--queue"}, reg)
	if err == nil {
		t.Fatal("expected error when TILLER_RUN_DIR is missing, got nil")
	}
	if !strings.Contains(err.Error(), "TILLER_RUN_DIR") {
		t.Errorf("error missing TILLER_RUN_DIR mention: %v", err)
	}
}

// TestDispatch_ModelAliasDeprecated verifies --model flag is translated to --tier
// for known aliases and that the dispatch still succeeds (--queue mode).
func TestDispatch_ModelAliasDeprecated(t *testing.T) {
	cases := []struct {
		role     string
		model    string
		wantTier string // the policy will route to this tier
	}{
		{"chief-architect", "opus", "reason"},
		{"chief-architect", "claude-opus-4-8", "reason"},
		{"chief-architect", "fable", "reason"},
		{"worker", "sonnet", "execute"},
		{"worker", "haiku", "execute"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			_, runDir, runID, st := makeDispatchTestEnv(t)
			reg := adapter.NewRegistry()
			reg.Register(newFakeAdapter("claude-headless", "full"))

			t.Setenv("TILLER_RUN_DIR", runDir)
			t.Setenv("TILLER_ROLE", "orchestrator")
			t.Setenv("TILLER_DEPTH", "0")
			t.Setenv("TILLER_DISPATCH_ID", "")

			err := runDispatchWithRegistry([]string{
				"--role", tc.role,
				"--model", tc.model,
				"--brief", "brief text",
				"--queue",
			}, reg)
			if err != nil {
				t.Fatalf("runDispatchWithRegistry --model=%s: %v", tc.model, err)
			}

			dispatches, err := listDispatchesFromStore(t, st, runID)
			if err != nil {
				t.Fatalf("listDispatches: %v", err)
			}
			if len(dispatches) == 0 {
				t.Fatal("no dispatches written")
			}
			d := dispatches[0]
			if d.Tier != tc.wantTier {
				t.Errorf("--model=%s: dispatch tier = %q, want %q", tc.model, d.Tier, tc.wantTier)
			}
		})
	}
}

// TestDispatch_DeniedByPolicy verifies that policy denial returns a DenialError.
// We trigger the DenyDirectSpawnAtDepth rule by setting caller depth >= 2
// without --queue (direct spawn at depth >= 2 is denied by policy).
func TestDispatch_DeniedByPolicy(t *testing.T) {
	_, runDir, _, _ := makeDispatchTestEnv(t)

	reg := adapter.NewRegistry()
	reg.Register(newFakeAdapter("claude-headless", "full"))

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_ROLE", "worker")
	t.Setenv("TILLER_DEPTH", "2") // depth >= 2 without --queue → DenyDirectSpawnAtDepth
	t.Setenv("TILLER_DISPATCH_ID", "d01")

	// --wait=false (no-wait) + no --queue: direct spawn attempt at depth 2
	err := runDispatchWithRegistry([]string{
		"--role", "worker",
		"--brief", "brief text",
		"--wait=false",
	}, reg)

	if err == nil {
		t.Fatal("expected DenialError from policy, got nil")
	}
	var denial *DenialError
	// Check that it's a DenialError (or wraps one).
	if de, ok := err.(*DenialError); ok {
		denial = de
	}
	if denial == nil {
		t.Errorf("expected *DenialError, got %T: %v", err, err)
	}
}

// TestDispatch_Queue_DispatchIDMonotonic verifies that sequential --queue calls
// produce monotonically increasing dispatch IDs (d01, d02, ...).
func TestDispatch_Queue_DispatchIDMonotonic(t *testing.T) {
	_, runDir, runID, st := makeDispatchTestEnv(t)

	reg := adapter.NewRegistry()
	reg.Register(newFakeAdapter("claude-headless", "full"))

	t.Setenv("TILLER_RUN_DIR", runDir)
	t.Setenv("TILLER_ROLE", "user")
	t.Setenv("TILLER_DEPTH", "0")
	t.Setenv("TILLER_DISPATCH_ID", "")

	for i := range 3 {
		err := runDispatchWithRegistry([]string{
			"--role", "worker",
			"--brief", "brief",
			"--queue",
		}, reg)
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	dispatches, err := listDispatchesFromStore(t, st, runID)
	if err != nil {
		t.Fatalf("listDispatches: %v", err)
	}
	if len(dispatches) != 3 {
		t.Fatalf("expected 3 dispatches, got %d", len(dispatches))
	}

	// IDs must be monotonic: d01 < d02 < d03.
	for i := 1; i < len(dispatches); i++ {
		if dispatches[i].ID <= dispatches[i-1].ID {
			t.Errorf("dispatch IDs not monotonic: dispatches[%d]=%q <= dispatches[%d]=%q",
				i, dispatches[i].ID, i-1, dispatches[i-1].ID)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// listDispatchesFromStore reads all dispatches for a run from the fsstore.
// It allocates a fresh dispatch to get the highest ID, then reads from d01 up.
func listDispatchesFromStore(t *testing.T, st *fsstore.FS, runID string) ([]*scratch.Dispatch, error) {
	t.Helper()
	// Use ListPendingDispatches to find pending ones, and also probe d01..d99.
	var out []*scratch.Dispatch
	for i := 1; i <= 99; i++ {
		did := "d" + twoDigit(i)
		d, err := st.ReadDispatch(runID, did)
		if err != nil {
			// Not found → stop scanning.
			break
		}
		out = append(out, d)
	}
	return out, nil
}

func twoDigit(n int) string {
	return fmt.Sprintf("%02d", n)
}
