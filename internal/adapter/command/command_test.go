package command_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/adapter/command"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/tier"
)

// buildTierConfig creates a tier.Config from inline TOML content written to a
// temp project directory.
func buildTierConfig(t *testing.T, adapterTOML string) *tier.Config {
	t.Helper()
	tmpDir := t.TempDir()
	tillerDir := filepath.Join(tmpDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0o755); err != nil {
		t.Fatalf("mkdir .tiller: %v", err)
	}
	// Minimal models.toml: a command candidate in execute tier + the adapter section.
	content := "[tiers.execute]\ncandidates = [\"command:test-cmd/-\"]\n\n" + adapterTOML
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write models.toml: %v", err)
	}
	cfg, err := tier.Load(tmpDir)
	if err != nil {
		t.Fatalf("tier.Load: %v", err)
	}
	return cfg
}

// setupFixture creates a minimal run+dispatch in-mem fixture using fsstore.
func setupFixture(t *testing.T) (scratch.Store, string, string) {
	t.Helper()
	workspace := t.TempDir()
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}
	st := fsstore.Open(runsBase)
	run := &scratch.Run{
		Task:        "command adapter test",
		Workspace:   workspace,
		Status:      "running",
		FableBudget: 2,
		CreatedAt:   time.Now(),
	}
	runID, err := st.CreateRun(run)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(runsBase, runID)
	return st, runID, runDir
}

// writeScript writes a POSIX shell script to dir/name and makes it executable.
func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+content), 0o755); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
	return path
}

func TestName(t *testing.T) {
	cfg := buildTierConfig(t, "")
	a := command.New(cfg)
	if got := a.Name(); got != "command" {
		t.Errorf("Name() = %q; want %q", got, "command")
	}
}

func TestEnforcement(t *testing.T) {
	cfg := buildTierConfig(t, "")
	a := command.New(cfg)
	if got := a.Enforcement(); got != "degraded" {
		t.Errorf("Enforcement() = %q; want %q", got, "degraded")
	}
}

func TestPrepare_Idempotent(t *testing.T) {
	cfg := buildTierConfig(t, "")
	a := command.New(cfg)
	st, runID, runDir := setupFixture(t)

	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("test brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Provider:   "test-cmd",
		WorkDir:    runDir,
		Depth:      1,
	}
	// Prepare should succeed twice without error.
	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare (first): %v", err)
	}
	if err := a.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare (second): %v", err)
	}
}

// TestRun_StdoutCapture verifies that a script's stdout becomes the report.
func TestRun_StdoutCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	script := writeScript(t, scriptDir, "echo-agent.sh", `cat "$1"
echo "result: ok"
`)
	// {brief} is the first argument; script cats it then prints "result: ok".
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\", \"{brief}\"]\nreport = \"stdout\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("hello brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Provider:   "test-cmd",
		Role:       "worker",
		WorkDir:    runDir,
		Depth:      1,
	}

	result, err := a.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q; want completed", result.Status)
	}
	if result.CostUSD != 0 {
		t.Errorf("CostUSD = %f; want 0", result.CostUSD)
	}

	// Verify report was written.
	report, err := st.ReadReport(runID, dispatchID)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if !strings.Contains(string(report), "result: ok") {
		t.Errorf("report does not contain expected output; got: %s", string(report))
	}
}

// TestRun_FileCapture verifies that {report} placeholder and file-based capture work.
func TestRun_FileCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	// Script writes to $2 (the {report} placeholder).
	script := writeScript(t, scriptDir, "file-agent.sh", `echo "file output" > "$2"
`)
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\", \"{brief}\", \"{report}\"]\nreport = \"file\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("brief content")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Provider:   "test-cmd",
		Role:       "worker",
		WorkDir:    runDir,
		Depth:      1,
	}

	result, err := a.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q; want completed", result.Status)
	}
	report, err := st.ReadReport(runID, dispatchID)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if !strings.Contains(string(report), "file output") {
		t.Errorf("report does not contain file output; got: %s", string(report))
	}
}

// TestRun_NonZeroExitIsFailure verifies that a non-zero exit becomes "failed".
func TestRun_NonZeroExitIsFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	script := writeScript(t, scriptDir, "fail-agent.sh", `echo "error output"; exit 1
`)
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\"]\nreport = \"stdout\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store: st, RunID: runID, DispatchID: dispatchID,
		Provider: "test-cmd", Role: "worker", WorkDir: runDir, Depth: 1,
	}

	result, err := a.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("Status = %q; want failed", result.Status)
	}
}

// TestRun_TimeoutKill verifies that a slow script is killed on timeout.
func TestRun_TimeoutKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	script := writeScript(t, scriptDir, "slow-agent.sh", `sleep 30
echo "should not reach here"
`)
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\"]\nreport = \"stdout\"\ntimeout = \"200ms\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store: st, RunID: runID, DispatchID: dispatchID,
		Provider: "test-cmd", Role: "worker", WorkDir: runDir, Depth: 1,
	}

	start := time.Now()
	result, err := a.Run(context.Background(), spec)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("Status = %q; want failed (timeout)", result.Status)
	}
	// Should have exited well under 2 seconds (timeout was 200ms).
	if elapsed > 2*time.Second {
		t.Errorf("took %s; expected timeout to kill process quickly", elapsed)
	}
}

// TestRun_PlaceholderSubstitution verifies that {brief} and {report} are substituted.
func TestRun_PlaceholderSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	// The script prints the brief path and report path, which we can verify.
	script := writeScript(t, scriptDir, "placeholder-agent.sh", `echo "brief=$1 report=$2"
`)
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\", \"{brief}\", \"{report}\"]\nreport = \"stdout\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("placeholder test")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store: st, RunID: runID, DispatchID: dispatchID,
		Provider: "test-cmd", Role: "worker", WorkDir: runDir, Depth: 1,
	}

	result, err := a.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q; want completed", result.Status)
	}
	report, _ := st.ReadReport(runID, dispatchID)
	// Report should contain actual file paths (not {brief} literal).
	reportStr := string(report)
	if strings.Contains(reportStr, "{brief}") {
		t.Error("report contains {brief} literal — placeholder was not substituted")
	}
	if strings.Contains(reportStr, "{report}") {
		t.Error("report contains {report} literal — placeholder was not substituted")
	}
	if !strings.Contains(reportStr, "brief.md") {
		t.Errorf("report should mention brief.md path; got: %s", reportStr)
	}
}

// TestRun_TraceEventWritten verifies that a kind:"report" TraceEvent is appended.
func TestRun_TraceEventWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sh")
	}
	scriptDir := t.TempDir()
	script := writeScript(t, scriptDir, "trace-agent.sh", `echo "trace test output"
`)
	toml := "[adapter.test-cmd]\nargv = [\"" + script + "\"]\nreport = \"stdout\"\n"
	cfg := buildTierConfig(t, toml)
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("trace brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store: st, RunID: runID, DispatchID: dispatchID,
		Provider: "test-cmd", Role: "worker", WorkDir: runDir, Depth: 1,
	}

	result, err := a.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q; want completed", result.Status)
	}

	// The trace event is written to context_trace.jsonl (kind:"report").
	// Verify indirectly that the file exists and contains "report".
	tracePath := filepath.Join(runDir, "dispatches", dispatchID, "context_trace.jsonl")
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read context_trace.jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"report"`) {
		t.Errorf("context_trace.jsonl does not contain report event; got: %s", string(data))
	}
}

// TestRun_DegradedEnforcementOnAdapter verifies the adapter reports "degraded".
func TestRun_DegradedEnforcementOnAdapter(t *testing.T) {
	cfg := buildTierConfig(t, "")
	a := command.New(cfg)
	if got := a.Enforcement(); got != "degraded" {
		t.Errorf("Enforcement() = %q; want degraded", got)
	}
}

// TestRun_MissingConfig verifies that Run returns an error when no adapter
// config section exists.
func TestRun_MissingConfig(t *testing.T) {
	cfg := buildTierConfig(t, "") // no [adapter.test-cmd] section
	a := command.New(cfg)

	st, runID, runDir := setupFixture(t)
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("brief")); err != nil {
		t.Fatal(err)
	}
	spec := &adapter.DispatchSpec{
		Store: st, RunID: runID, DispatchID: dispatchID,
		Provider: "test-cmd", Role: "worker", WorkDir: runDir, Depth: 1,
	}

	_, err = a.Run(context.Background(), spec)
	if err == nil {
		t.Fatal("expected error for missing adapter config, got nil")
	}
	if !strings.Contains(err.Error(), "no [adapter.test-cmd]") {
		t.Errorf("error should mention missing section; got: %v", err)
	}
}
