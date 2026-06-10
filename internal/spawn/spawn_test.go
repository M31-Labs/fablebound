package spawn_test

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

	"m31labs.dev/tiller/internal/run"
)

// findTiller builds the tiller binary and returns its path.
func findTiller(t *testing.T) string {
	t.Helper()
	// Build into a temp dir.
	dir := t.TempDir()
	bin := filepath.Join(dir, "tiller")
	cmd := exec.Command("go", "build", "-o", bin, "m31labs.dev/tiller/cmd/tiller")
	cmd.Dir = projectRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tiller: %v\n%s", err, out)
	}
	return bin
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../internal/spawn/spawn_test.go
	// root = 3 levels up
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	return root
}

func claudeStub(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	stub := filepath.Join(root, "testdata", "bin", "claude-stub")
	if _, err := os.Stat(stub); err != nil {
		t.Fatalf("claude-stub not found at %s: %v", stub, err)
	}
	return stub
}

// setupFixtureRun creates a minimal .tiller run structure for testing.
func setupFixtureRun(t *testing.T) (runDir string, binary string, stub string) {
	t.Helper()

	binary = findTiller(t)
	stub = claudeStub(t)

	workspace := t.TempDir()

	// Create .tiller structure (like tiller init would).
	runBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a run.
	store := run.NewStore(runBase)
	runID, err := store.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir = store.RunDir(runID)

	// Write a manifest.
	manifest := &run.Manifest{
		RunID:       runID,
		Task:        "test task",
		Workspace:   workspace,
		Status:      "running",
		FableBudget: 2,
		CreatedAt:   time.Now(),
	}
	if err := run.WriteManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}

	// Copy embedded default policies to .tiller/policy/ so dispatch can load them.
	policyDir := filepath.Join(workspace, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyEmbeddedPolicies(t, projectRoot(t), policyDir)

	// Copy embedded default roles.
	rolesDir := filepath.Join(workspace, ".tiller", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	return runDir, binary, stub
}

// copyEmbeddedPolicies copies policy/*.arb from the project root into the target dir.
func copyEmbeddedPolicies(t *testing.T, root, target string) {
	t.Helper()
	for _, name := range []string{"dispatch.arb", "toolgate.arb"} {
		src := filepath.Join(root, "policy", name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read policy %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(target, name), data, 0o644); err != nil {
			t.Fatalf("write policy %s: %v", name, err)
		}
	}
}

// TestDispatchAllowPath exercises the full allow path:
//   - claude-stub emits a valid JSON result
//   - dispatches/d01/{brief.md,report.md,meta.json,settings.json} exist
//   - meta.json status = "completed"
//   - audit/dispatch.jsonl has an Allow event with strategy
//   - caller's context_trace.jsonl gains kind:"dispatch" (child d01)
//   - d01's context_trace.jsonl gains kind:"report"
func TestDispatchAllowPath(t *testing.T) {
	runDir, binary, stub := setupFixtureRun(t)
	runID := filepath.Base(runDir)
	_ = runID

	// Simulate being the "root" caller (orchestrator) by creating a root dispatch dir.
	rootDispatchDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(rootDispatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a root meta so context trace has somewhere to write.
	rootMeta := &run.Meta{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "orchestrator",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, rootMeta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=orchestrator",
		"TILLER_DEPTH=0",
		"TILLER_DISPATCH_ID=root",
		"TILLER_CLAUDE_BIN="+stub,
	)

	cmd := exec.Command(binary, "dispatch", "--role", "investigator", "--brief", "test brief content")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dispatch returned error: %v\nstdout=%s", err, out)
	}

	// Parse dispatch ID from output line "<id> <status> <path>".
	dispatchLine := strings.TrimSpace(string(out))
	parts := strings.Fields(dispatchLine)
	if len(parts) < 1 {
		t.Fatalf("unexpected dispatch output: %q", dispatchLine)
	}
	dispatchID := parts[0]
	t.Logf("dispatch ID: %s, output: %s", dispatchID, dispatchLine)

	// Wait for the dispatch to reach terminal status (it may already be completed
	// since --wait is default, but verify robustly).
	deadline := time.Now().Add(10 * time.Second)
	for {
		m, err := run.ReadMeta(runDir, dispatchID)
		if err == nil && m.IsTerminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor for %s did not complete within 10s", dispatchID)
		}
		time.Sleep(100 * time.Millisecond)
	}

	dispatchDir := filepath.Join(runDir, "dispatches", dispatchID)

	// Check that all files exist.
	for _, name := range []string{"brief.md", "report.md", "meta.json", "settings.json"} {
		p := filepath.Join(dispatchDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing file %s: %v", name, err)
		}
	}

	// Check meta status = "completed".
	meta, err := run.ReadMeta(runDir, dispatchID)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.Status != "completed" {
		t.Errorf("meta.Status = %q, want completed", meta.Status)
	}

	// Check audit/dispatch.jsonl has an event.
	auditPath := filepath.Join(runDir, "audit", "dispatch.jsonl")
	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("dispatch.jsonl missing: %v", err)
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(auditData) == 0 {
		t.Error("audit/dispatch.jsonl is empty")
	}
	// Verify it contains strategy.
	auditStr := string(auditData)
	if !strings.Contains(auditStr, `"strategy"`) {
		t.Errorf("audit event missing 'strategy' field")
	}

	// Check caller's context_trace.jsonl has kind:"dispatch".
	callerTracePath := filepath.Join(runDir, "dispatches", "root", "context_trace.jsonl")
	if _, err := os.Stat(callerTracePath); err != nil {
		t.Fatalf("caller context_trace.jsonl missing: %v", err)
	}
	callerTraceData, err := os.ReadFile(callerTracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(callerTraceData), `"dispatch"`) {
		t.Errorf("caller context_trace.jsonl missing kind:dispatch event")
	}
	if !strings.Contains(string(callerTraceData), `"`+dispatchID+`"`) {
		t.Errorf("caller context_trace.jsonl missing child_id %s", dispatchID)
	}

	// Check d01's context_trace.jsonl has kind:"report".
	d01TracePath := filepath.Join(dispatchDir, "context_trace.jsonl")
	if _, err := os.Stat(d01TracePath); err != nil {
		t.Fatalf("d01 context_trace.jsonl missing: %v", err)
	}
	d01TraceData, err := os.ReadFile(d01TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d01TraceData), `"report"`) {
		t.Errorf("d01 context_trace.jsonl missing kind:report event")
	}
}

// TestDispatchDenyWorkerFable verifies that dispatching a worker with model=fable
// results in exit 3 and "DenyFableForExecution" on stderr.
func TestDispatchDenyWorkerFable(t *testing.T) {
	runDir, binary, stub := setupFixtureRun(t)

	// Create a root dispatch so context trace has somewhere to write.
	rootDispatchDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(rootDispatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootMeta := &run.Meta{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "orchestrator",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, rootMeta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=orchestrator",
		"TILLER_DEPTH=0",
		"TILLER_DISPATCH_ID=root",
		"TILLER_CLAUDE_BIN="+stub,
	)

	cmd := exec.Command(binary, "dispatch", "--role", "worker", "--model", "fable", "--brief", "test")
	cmd.Env = env
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit, got success")
	}

	exitCode := cmd.ProcessState.ExitCode()
	if exitCode != 3 {
		t.Errorf("exit code = %d, want 3", exitCode)
	}

	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "DenyFableForExecution") {
		t.Errorf("stderr missing DenyFableForExecution, got: %s", stderr)
	}
}

// TestDispatchDenyTerminalDepth verifies that TILLER_DEPTH=2 results in
// exit 3 with DenyTerminalDepth.
func TestDispatchDenyTerminalDepth(t *testing.T) {
	runDir, binary, stub := setupFixtureRun(t)

	// Create a d01 dispatch dir to serve as the "caller" at depth 2.
	d01Dir := filepath.Join(runDir, "dispatches", "d01")
	if err := os.MkdirAll(d01Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d01Meta := &run.Meta{
		ID:        "d01",
		Role:      "worker",
		Model:     "sonnet",
		Profile:   "execution",
		Status:    "running",
		Depth:     2,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, d01Meta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=worker",
		"TILLER_DEPTH=2",
		"TILLER_DISPATCH_ID=d01",
		"TILLER_CLAUDE_BIN="+stub,
	)

	cmd := exec.Command(binary, "dispatch", "--role", "investigator", "--brief", "test")
	cmd.Env = env
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit, got success")
	}

	exitCode := cmd.ProcessState.ExitCode()
	if exitCode != 3 {
		t.Errorf("exit code = %d, want 3", exitCode)
	}

	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "DenyTerminalDepth") {
		t.Errorf("stderr missing DenyTerminalDepth, got: %s", stderr)
	}
}

// TestDispatchTimeoutRunning verifies that when the stub sleeps longer than
// --timeout, dispatch returns exit 0 with status "running", and the supervisor
// eventually completes.
func TestDispatchTimeoutRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	runDir, binary, _ := setupFixtureRun(t)

	// Use a slow stub that sleeps 3s.
	slowStubDir := t.TempDir()
	slowStub := filepath.Join(slowStubDir, "claude-slow")
	slowStubContent := `#!/usr/bin/env bash
sleep 3
printf '{"type":"result","result":"slow report","cost_usd":0.001,"num_turns":1,"session_id":"slow-stub","is_error":false}\n'
`
	if err := os.WriteFile(slowStub, []byte(slowStubContent), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a root dispatch.
	rootDispatchDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(rootDispatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootMeta := &run.Meta{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "orchestrator",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, rootMeta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=orchestrator",
		"TILLER_DEPTH=0",
		"TILLER_DISPATCH_ID=root",
		"TILLER_CLAUDE_BIN="+slowStub,
	)

	cmd := exec.Command(binary, "dispatch", "--role", "investigator", "--brief", "test slow", "--timeout", "1s")
	cmd.Env = env
	var stdoutBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("dispatch --timeout 1s returned error: %v", err)
	}

	stdout := stdoutBuf.String()
	if !strings.Contains(stdout, "running") {
		t.Errorf("expected 'running' in stdout, got: %s", stdout)
	}

	// Parse dispatch ID from output.
	parts := strings.Fields(strings.TrimSpace(stdout))
	if len(parts) < 1 {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	dispatchID := parts[0]
	t.Logf("dispatch ID: %s", dispatchID)

	// Wait for supervisor to eventually complete (the stub sleeps 3s total).
	deadline := time.Now().Add(15 * time.Second)
	for {
		m, err := run.ReadMeta(runDir, dispatchID)
		if err == nil && m.IsTerminal() {
			if m.Status != "completed" {
				t.Errorf("expected completed, got %s", m.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor did not complete within 15s after timeout dispatch")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestNoteAdd verifies that `tiller note add "text"` creates a file in notes/.
func TestNoteAdd(t *testing.T) {
	runDir, binary, _ := setupFixtureRun(t)

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=orchestrator",
	)

	cmd := exec.Command(binary, "note", "add", "hello from test")
	cmd.Env = env
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("note add: %v\nstdout=%s\nstderr=%s", err, out, cmdStderr(cmd))
	}

	notesDir := filepath.Join(runDir, "notes")
	entries, err := os.ReadDir(notesDir)
	if err != nil {
		t.Fatalf("read notes dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 note file, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasSuffix(name, "-orchestrator.md") {
		t.Errorf("note filename %q should end with -orchestrator.md", name)
	}
	content, err := os.ReadFile(filepath.Join(notesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello from test" {
		t.Errorf("note content = %q, want %q", string(content), "hello from test")
	}
}

// TestPollAndAwait verifies that poll and await work for a terminal dispatch.
func TestPollAndAwait(t *testing.T) {
	runDir, binary, stub := setupFixtureRun(t)

	// Create a completed dispatch.
	dispatchID := "d01"
	dispDir := filepath.Join(runDir, "dispatches", dispatchID)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ended := now.Add(time.Second)
	meta := &run.Meta{
		ID:        dispatchID,
		Role:      "investigator",
		Model:     "sonnet",
		Status:    "completed",
		Depth:     1,
		StartedAt: now,
		EndedAt:   &ended,
	}
	if err := run.WriteMeta(runDir, meta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_CLAUDE_BIN="+stub,
	)

	// Test poll.
	cmd := exec.Command(binary, "poll", dispatchID)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(string(out), "d01") || !strings.Contains(string(out), "completed") {
		t.Errorf("poll output = %q, want d01 completed ...", string(out))
	}

	// Test await (should return immediately since already terminal).
	cmd2 := exec.Command(binary, "await", dispatchID, "--timeout", "1s")
	cmd2.Env = env
	out2, err := cmd2.Output()
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !strings.Contains(string(out2), "completed") {
		t.Errorf("await output = %q, want completed", string(out2))
	}

	// Test await on a running dispatch with a short timeout — should return
	// exit 0 with "running".
	runningDispatchID := "d99"
	runningDir := filepath.Join(runDir, "dispatches", runningDispatchID)
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runningMeta := &run.Meta{
		ID:        runningDispatchID,
		Role:      "worker",
		Model:     "sonnet",
		Status:    "running",
		Depth:     1,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, runningMeta); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ctx3, cancel3 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel3()
	cmd3 := exec.CommandContext(ctx3, binary, "await", runningDispatchID, "--timeout", "300ms")
	// Use clean env without TILLER_DISPATCH_ID to avoid any side effects.
	cmd3.Env = []string{
		"TILLER_RUN_DIR=" + runDir,
		"TILLER_CLAUDE_BIN=" + stub,
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
	out3, err := cmd3.Output()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("await running --timeout 300ms: %v (elapsed=%v)", err, elapsed)
	}
	if !strings.Contains(string(out3), "running") {
		t.Errorf("await running output = %q, want 'running'", string(out3))
	}
	if elapsed > 2*time.Second {
		t.Errorf("await took %v, should be ~300ms", elapsed)
	}
}

// cmdStderr returns stderr from a cmd's last run.
func cmdStderr(cmd *exec.Cmd) string {
	if cmd.Stderr == nil {
		return ""
	}
	if sb, ok := cmd.Stderr.(*strings.Builder); ok {
		return sb.String()
	}
	return ""
}

// TestAuditEventFormat verifies that a dispatch audit event has the
// expected JSON structure (kind:"rules", non-empty arbitrace, strategy).
func TestAuditEventFormat(t *testing.T) {
	runDir, binary, stub := setupFixtureRun(t)
	runID := filepath.Base(runDir)
	_ = runID

	rootDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootMeta := &run.Meta{
		ID:        "root",
		Role:      "orchestrator",
		Model:     "fable",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now(),
	}
	if err := run.WriteMeta(runDir, rootMeta); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"TILLER_RUN_DIR="+runDir,
		"TILLER_ROLE=orchestrator",
		"TILLER_DEPTH=0",
		"TILLER_DISPATCH_ID=root",
		"TILLER_CLAUDE_BIN="+stub,
	)

	cmd := exec.Command(binary, "dispatch", "--role", "investigator", "--brief", "audit test")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dispatch: %v\nstdout=%s", err, out)
	}

	// Parse dispatch ID from output.
	dispLine := strings.TrimSpace(string(out))
	dispParts := strings.Fields(dispLine)
	if len(dispParts) < 1 {
		t.Fatalf("unexpected dispatch output: %q", dispLine)
	}
	auditDispID := dispParts[0]
	t.Logf("dispatch ID: %s", auditDispID)

	// Wait for completion.
	deadline := time.Now().Add(10 * time.Second)
	for {
		m, err := run.ReadMeta(runDir, auditDispID)
		if err == nil && m.IsTerminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for completion")
		}
		time.Sleep(100 * time.Millisecond)
	}

	auditPath := filepath.Join(runDir, "audit", "dispatch.jsonl")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read dispatch.jsonl: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("dispatch.jsonl is empty")
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("parse audit event: %v", err)
	}

	if event["kind"] != "rules" {
		t.Errorf("event.kind = %v, want 'rules'", event["kind"])
	}
	if event["bundle_id"] == nil || event["bundle_id"] == "" {
		t.Error("event.bundle_id missing")
	}
	if event["strategy"] == nil {
		t.Error("event.strategy missing")
	}
}
