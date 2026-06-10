package run_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ── helpers shared with spawn_test ────────────────────────────────────────────

func runCmdProjectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../internal/run/runcmd_test.go → root is 3 levels up
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	return root
}

func buildTiller(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tiller")
	cmd := exec.Command("go", "build", "-o", bin, "m31labs.dev/tiller/cmd/tiller")
	cmd.Dir = runCmdProjectRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tiller: %v\n%s", err, out)
	}
	return bin
}

func dispatchClaudeStub(t *testing.T) string {
	t.Helper()
	root := runCmdProjectRoot(t)
	stub := filepath.Join(root, "testdata", "bin", "claude-dispatch-stub")
	if _, err := os.Stat(stub); err != nil {
		t.Fatalf("claude-dispatch-stub not found at %s: %v", stub, err)
	}
	return stub
}

// copyPoliciesForRun copies policy/*.arb to the project's .tiller/policy/
// dir (which tiller run uses after changing cwd to workspace).
func copyPoliciesForRun(t *testing.T, workspace string) {
	t.Helper()
	root := runCmdProjectRoot(t)
	policyDir := filepath.Join(workspace, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(workspace, ".tiller", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dispatch.arb", "toolgate.arb"} {
		src := filepath.Join(root, "policy", name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read policy %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(policyDir, name), data, 0o644); err != nil {
			t.Fatalf("write policy %s: %v", name, err)
		}
	}
}

// ── T1.6 acceptance test ──────────────────────────────────────────────────────

// TestRunCommand exercises the full `tiller run "demo"` flow with a stub
// that dispatches a child investigator.
//
// Acceptance criteria (plan T1.6):
//   - manifest completed
//   - dispatches/root and d01 both exist
//   - both audit files non-empty
func TestRunCommand(t *testing.T) {
	binary := buildTiller(t)
	stub := dispatchClaudeStub(t)

	workspace := t.TempDir()
	copyPoliciesForRun(t, workspace)

	// Build the manifest runs dir.
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_CLAUDE_BIN="+stub,
		"TILLER_BIN="+binary, // for claude-dispatch-stub
	)

	cmd := exec.Command(binary, "run", "demo")
	cmd.Dir = workspace
	cmd.Env = env

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// binary run blocks until the orchestrator completes.
	// Give it 30 seconds to finish.
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tiller run failed: %v\nstdout=%s\nstderr=%s",
				err, stdoutBuf.String(), stderrBuf.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("tiller run timed out after 30s\nstdout=%s\nstderr=%s",
			stdoutBuf.String(), stderrBuf.String())
	}

	t.Logf("stdout: %s", stdoutBuf.String())
	t.Logf("stderr: %s", stderrBuf.String())

	// Find the run directory (should be exactly one).
	entries, err := os.ReadDir(runsBase)
	if err != nil {
		t.Fatalf("read runs dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 run dir, got %d", len(entries))
	}
	runID := entries[0].Name()
	runDir := filepath.Join(runsBase, runID)

	// 1. manifest.json exists and status = completed.
	manifestData, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	if manifest["status"] != "completed" {
		t.Errorf("manifest.status = %v, want completed", manifest["status"])
	}
	if manifest["task"] != "demo" {
		t.Errorf("manifest.task = %v, want demo", manifest["task"])
	}
	if manifest["policy_shas"] == nil {
		t.Error("manifest.policy_shas missing")
	}

	// 2. dispatches/root exists.
	rootDir := filepath.Join(runDir, "dispatches", "root")
	if _, err := os.Stat(rootDir); err != nil {
		t.Errorf("dispatches/root missing: %v", err)
	}

	// 3. At least one child dispatch exists (d01).
	d01Dir := filepath.Join(runDir, "dispatches", "d01")
	if _, err := os.Stat(d01Dir); err != nil {
		t.Errorf("dispatches/d01 missing: %v", err)
	}

	// 4. Both audit files non-empty.
	for _, name := range []string{"dispatch.jsonl", "toolgate.jsonl"} {
		p := filepath.Join(runDir, "audit", name)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("audit/%s missing: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("audit/%s is empty", name)
		}
	}

	// 5. task.md was written.
	taskMD, err := os.ReadFile(filepath.Join(runDir, "task.md"))
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if strings.TrimSpace(string(taskMD)) != "demo" {
		t.Errorf("task.md content = %q, want demo", strings.TrimSpace(string(taskMD)))
	}
}

// ── T1.7 acceptance test ──────────────────────────────────────────────────────

// TestRunsShowAndList verifies that after a run, `runs show` and `runs list`
// produce the correct output.
func TestRunsShowAndList(t *testing.T) {
	binary := buildTiller(t)
	stub := dispatchClaudeStub(t)

	workspace := t.TempDir()
	copyPoliciesForRun(t, workspace)

	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_CLAUDE_BIN="+stub,
		"TILLER_BIN="+binary,
	)

	// Run binary run.
	runCmd := exec.Command(binary, "run", "show me the money")
	runCmd.Dir = workspace
	runCmd.Env = env

	done := make(chan error, 1)
	go func() { done <- runCmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tiller run failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = runCmd.Process.Kill()
		t.Fatal("tiller run timed out")
	}

	// Find the run ID.
	entries, err := os.ReadDir(runsBase)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected run dir: %v", err)
	}
	runID := entries[0].Name()

	// Test `runs list` — must include the run ID and status.
	listCmd := exec.Command(binary, "runs", "list")
	listCmd.Dir = workspace
	listCmd.Env = env
	listOut, err := listCmd.Output()
	if err != nil {
		t.Fatalf("runs list: %v", err)
	}
	listStr := string(listOut)
	t.Logf("runs list output:\n%s", listStr)
	if !strings.Contains(listStr, runID) {
		t.Errorf("runs list missing run ID %s", runID)
	}
	if !strings.Contains(listStr, "completed") {
		t.Errorf("runs list missing 'completed' status")
	}

	// Test `runs show <id>`.
	showCmd := exec.Command(binary, "runs", "show", runID)
	showCmd.Dir = workspace
	showCmd.Env = env
	showOut, err := showCmd.Output()
	if err != nil {
		t.Fatalf("runs show: %v", err)
	}
	showStr := string(showOut)
	t.Logf("runs show output:\n%s", showStr)

	// Must contain root and d01 with statuses.
	if !strings.Contains(showStr, "root") {
		t.Error("runs show missing 'root'")
	}
	if !strings.Contains(showStr, "d01") {
		t.Error("runs show missing 'd01'")
	}
	if !strings.Contains(showStr, "completed") {
		t.Error("runs show missing 'completed' status")
	}

	// Test `runs show <id> --json`.
	showJSONCmd := exec.Command(binary, "runs", "show", runID, "--json")
	showJSONCmd.Dir = workspace
	showJSONCmd.Env = env
	showJSONOut, err := showJSONCmd.Output()
	if err != nil {
		t.Fatalf("runs show --json: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(showJSONOut, &summary); err != nil {
		t.Fatalf("parse runs show --json output: %v\nraw: %s", err, showJSONOut)
	}
	if summary["run_id"] != runID {
		t.Errorf("json run_id = %v, want %s", summary["run_id"], runID)
	}
	if summary["status"] != "completed" {
		t.Errorf("json status = %v, want completed", summary["status"])
	}
	if summary["dispatches"] == nil {
		t.Error("json missing 'dispatches'")
	}
}
