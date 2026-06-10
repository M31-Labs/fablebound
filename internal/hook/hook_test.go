package hook_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/fablebound/internal/hook"
)

// setupFixtureRun creates a minimal run dir and returns its path.
func setupFixtureRun(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run1")
	for _, sub := range []string{"audit", "notes", "dispatches"} {
		if err := os.MkdirAll(filepath.Join(runDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return runDir
}

// setEnv temporarily sets environment variables, restoring them on cleanup.
func setEnv(t *testing.T, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		key, val := kv[i], kv[i+1]
		old, hadOld := os.LookupEnv(key)
		if val == "" {
			os.Unsetenv(key)
		} else {
			os.Setenv(key, val)
		}
		t.Cleanup(func() {
			if hadOld {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		})
	}
}

// runHook invokes hook.Run with the given stdin JSON and returns stdout bytes.
func runHook(t *testing.T, inputJSON string) (stdout []byte, err error) {
	t.Helper()
	var out bytes.Buffer
	r := strings.NewReader(inputJSON)
	err = hook.Run(r, &out, "")
	return out.Bytes(), err
}

// runHookWithWorkspace invokes hook.Run with a known workspace dir.
func runHookWithWorkspace(t *testing.T, inputJSON, workspaceDir string) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	r := strings.NewReader(inputJSON)
	err := hook.Run(r, &out, workspaceDir)
	return out.Bytes(), err
}

// TestMissingRole verifies that missing FABLEBOUND_ROLE exits 0 silently.
func TestMissingRole(t *testing.T) {
	setEnv(t, "FABLEBOUND_ROLE", "", "FABLEBOUND_DEPTH", "", "FABLEBOUND_DISPATCH_ID", "", "FABLEBOUND_RUN_DIR", "")
	out, err := runHook(t, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if err != nil {
		t.Errorf("expected nil error for missing role, got: %v", err)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("expected empty output for missing role, got: %s", out)
	}
}

// TestMalformedJSON verifies that malformed JSON returns an error (exit 2).
func TestMalformedJSON(t *testing.T) {
	setEnv(t, "FABLEBOUND_ROLE", "worker", "FABLEBOUND_DEPTH", "1", "FABLEBOUND_DISPATCH_ID", "d01", "FABLEBOUND_RUN_DIR", "")
	_, err := runHook(t, `{not valid json`)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

// TestOrchestratorDenyLS verifies orchestrator ls → deny.
func TestOrchestratorDenyLS(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "root")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "root", "orchestrator", 0)

	setEnv(t,
		"FABLEBOUND_ROLE", "orchestrator",
		"FABLEBOUND_DEPTH", "0",
		"FABLEBOUND_DISPATCH_ID", "root",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls -la"}}`,
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny, got %q", wrapper.HookSpecificOutput.PermissionDecision)
	}
}

// TestOrchestratorAllowDispatch verifies orchestrator fablebound dispatch → allow.
func TestOrchestratorAllowDispatch(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "root")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "root", "orchestrator", 0)

	setEnv(t,
		"FABLEBOUND_ROLE", "orchestrator",
		"FABLEBOUND_DEPTH", "0",
		"FABLEBOUND_DISPATCH_ID", "root",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"fablebound dispatch --role investigator --brief -"}}`,
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("expected allow, got %q", wrapper.HookSpecificOutput.PermissionDecision)
	}
}

// TestWorkerEditAllow verifies worker Edit → allow.
func TestWorkerEditAllow(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "d01", "worker", 1)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"/workspace/main.go"}}`,
		"/workspace",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("expected allow, got %q", wrapper.HookSpecificOutput.PermissionDecision)
	}
}

// TestReviewerWriteOutsideDeny verifies reviewer Write outside scratch → deny.
func TestReviewerWriteOutsideDeny(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "d01", "reviewer", 1)

	setEnv(t,
		"FABLEBOUND_ROLE", "reviewer",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	// File is outside the run dir (scratch).
	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/workspace/outside.txt"}}`,
		"/workspace",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny, got %q", wrapper.HookSpecificOutput.PermissionDecision)
	}
}

// TestArchitectWriteInsideAllow verifies chief-architect Write inside scratch → allow.
func TestArchitectWriteInsideAllow(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "d01", "chief-architect", 0)

	setEnv(t,
		"FABLEBOUND_ROLE", "chief-architect",
		"FABLEBOUND_DEPTH", "0",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	// File is inside the run dir (scratch).
	scratchFile := filepath.Join(runDir, "notes", "analysis.md")
	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"`+scratchFile+`"}}`,
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("expected allow, got %q (reason: %s)", wrapper.HookSpecificOutput.PermissionDecision, out)
	}
}

// TestDepth2DispatchDeny verifies depth-2 fablebound dispatch → DenyTerminalDispatch.
func TestDepth2DispatchDeny(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d02")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "d02", "worker", 2)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "2",
		"FABLEBOUND_DISPATCH_ID", "d02",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"fablebound dispatch --role investigator --brief investigate this"}}`,
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny, got %q", wrapper.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(wrapper.HookSpecificOutput.PermissionDecisionReason, "DenyTerminalDispatch") {
		t.Errorf("expected DenyTerminalDispatch in reason, got %q", wrapper.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestPreToolUseAuditLine verifies that each PreToolUse writes a line to audit/toolgate.jsonl.
func TestPreToolUseAuditLine(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "root")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "root", "orchestrator", 0)

	setEnv(t,
		"FABLEBOUND_ROLE", "orchestrator",
		"FABLEBOUND_DEPTH", "0",
		"FABLEBOUND_DISPATCH_ID", "root",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	cases := []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"fablebound dispatch --role investigator --brief -"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/workspace/main.go"}}`,
	}

	for _, input := range cases {
		_, err := runHookWithWorkspace(t, input, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	auditPath := filepath.Join(runDir, "audit", "toolgate.jsonl")
	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer f.Close()

	var lines int
	var nonEmptyArbitrace bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("invalid JSON in audit line: %v", err)
		}
		if arb, ok := ev["arbitrace"]; ok {
			if arr, ok := arb.([]any); ok && len(arr) > 0 {
				nonEmptyArbitrace = true
			}
		}
		lines++
	}

	if lines != len(cases) {
		t.Errorf("audit lines = %d, want %d", lines, len(cases))
	}
	if !nonEmptyArbitrace {
		t.Error("no event had non-empty arbitrace")
	}
}

// TestPostToolUseToolTrace verifies that PostToolUse appends to tool_trace.jsonl.
func TestPostToolUseToolTrace(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)
	// No meta needed for PostToolUse (identity verification only happens on PreToolUse).

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	cases := []string{
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go build ./..."},"tool_response":{"is_error":false}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"/workspace/README.md"},"tool_response":{"is_error":false}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/workspace/main.go"},"tool_response":{"is_error":true,"output":"file not found"}}`,
	}

	for _, input := range cases {
		_, err := runHookWithWorkspace(t, input, "")
		if err != nil {
			// PostToolUse should never return an error (logged to stderr, not returned).
			t.Errorf("PostToolUse returned error: %v", err)
		}
	}

	// tool_trace.jsonl should have 3 events.
	toolTracePath := filepath.Join(runDir, "dispatches", "d01", "tool_trace.jsonl")
	checkJSONLLines(t, toolTracePath, 3)

	// context_trace.jsonl should have 1 event (only Read).
	ctxTracePath := filepath.Join(runDir, "dispatches", "d01", "context_trace.jsonl")
	checkJSONLLines(t, ctxTracePath, 1)

	// Verify the Read event in context_trace.
	ctxData, err := os.ReadFile(ctxTracePath)
	if err != nil {
		t.Fatal(err)
	}
	var readEv map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(ctxData), &readEv); err != nil {
		t.Fatalf("parse context event: %v", err)
	}
	if readEv["kind"] != "read" {
		t.Errorf("context event kind = %q, want %q", readEv["kind"], "read")
	}
	if readEv["role"] != "worker" {
		t.Errorf("context event role = %q, want %q", readEv["role"], "worker")
	}
	if readEv["depth"].(float64) != 1 {
		t.Errorf("context event depth = %v, want 1", readEv["depth"])
	}
}

// TestPostToolUseTraceFailureSilent verifies that a broken trace dir still exits 0.
func TestPostToolUseTraceFailureSilent(t *testing.T) {
	runDir := setupFixtureRun(t)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d-nonexistent",
		"FABLEBOUND_RUN_DIR", runDir+"/nonexistent-run",
	)

	// Even with a broken run dir, PostToolUse must not return error.
	_, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go build ./..."},"tool_response":{"is_error":false}}`,
		"",
	)
	// PostToolUse logs errors to stderr but hook.Run itself should still return nil.
	if err != nil {
		t.Errorf("PostToolUse with broken run dir should succeed (exit 0), got: %v", err)
	}
}

// TestToolTraceRoleDepthMatch verifies tool_trace events carry the correct role and depth.
func TestToolTraceRoleDepthMatch(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d03")
	os.MkdirAll(dispatchDir, 0o755)
	// No meta needed for PostToolUse (identity verification only happens on PreToolUse).

	setEnv(t,
		"FABLEBOUND_ROLE", "investigator",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d03",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	_, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"rg TODO ./src"},"tool_response":{"is_error":false}}`,
		"",
	)
	if err != nil {
		t.Fatalf("PostToolUse error: %v", err)
	}

	tracePath := filepath.Join(runDir, "dispatches", "d03", "tool_trace.jsonl")
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("parse trace event: %v", err)
	}
	if ev["role"] != "investigator" {
		t.Errorf("role = %q, want %q", ev["role"], "investigator")
	}
	if ev["depth"].(float64) != 1 {
		t.Errorf("depth = %v, want 1", ev["depth"])
	}
	if ev["tool"] != "Bash" {
		t.Errorf("tool = %q, want %q", ev["tool"], "Bash")
	}
	if ev["status"] != "ok" {
		t.Errorf("status = %q, want %q", ev["status"], "ok")
	}
}

// TestIdentityVerification_ForgedRole verifies that a forged FABLEBOUND_ROLE
// (different from meta.json) causes the hook to fail closed (return error, exit 2).
func TestIdentityVerification_ForgedRole(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)

	// Write a meta that says role=worker
	writeTestMeta(t, runDir, "d01", "worker", 1)

	// But claim role=investigator in env (forged).
	setEnv(t,
		"FABLEBOUND_ROLE", "investigator", // FORGED
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	_, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/workspace/main.go"}}`,
		"/workspace",
	)
	if err == nil {
		t.Error("expected error for forged role, got nil (hook should fail closed)")
	}
}

// TestIdentityVerification_ForgedDepth verifies that a forged FABLEBOUND_DEPTH
// (different from meta.json) causes the hook to fail closed.
func TestIdentityVerification_ForgedDepth(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)

	// meta says depth=1
	writeTestMeta(t, runDir, "d01", "worker", 1)

	// but env claims depth=0 (forged to escape terminal-depth restrictions)
	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "0", // FORGED
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	_, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"fablebound dispatch --role investigator --brief test"}}`,
		"",
	)
	if err == nil {
		t.Error("expected error for forged depth, got nil (hook should fail closed)")
	}
}

// TestIdentityVerification_NonexistentDispatch verifies that a nonexistent
// FABLEBOUND_DISPATCH_ID causes the hook to fail closed (can't read meta).
func TestIdentityVerification_NonexistentDispatch(t *testing.T) {
	runDir := setupFixtureRun(t)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d-does-not-exist",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	_, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/workspace/main.go"}}`,
		"/workspace",
	)
	if err == nil {
		t.Error("expected error for nonexistent dispatch id, got nil (hook should fail closed)")
	}
	if err != nil && !strings.Contains(err.Error(), "identity mismatch") {
		t.Errorf("expected 'identity mismatch' in error, got: %v", err)
	}
}

// TestIdentityVerification_ValidIdentity verifies that matching env and meta passes.
func TestIdentityVerification_ValidIdentity(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)

	writeTestMeta(t, runDir, "d01", "worker", 1)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/workspace/main.go"}}`,
		"/workspace",
	)
	if err != nil {
		t.Errorf("expected nil error for valid identity, got: %v", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		t.Error("expected non-empty output for valid PreToolUse")
	}
}

// writeTestMeta writes a minimal meta.json for testing identity verification.
func writeTestMeta(t *testing.T, runDir, dispatchID, role string, depth int) {
	t.Helper()
	type minMeta struct {
		ID        string `json:"id"`
		Role      string `json:"role"`
		Model     string `json:"model"`
		Profile   string `json:"profile"`
		Status    string `json:"status"`
		Depth     int    `json:"depth"`
		StartedAt string `json:"started_at"`
	}
	m := minMeta{
		ID: dispatchID, Role: role, Model: "sonnet", Profile: "execution",
		Status: "running", Depth: depth, StartedAt: "2026-01-01T00:00:00Z",
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, "dispatches", dispatchID, "meta.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUnknownHookEventWarning verifies that an unknown hook_event_name exits 0
// but emits a warning to stderr.
func TestUnknownHookEventWarning(t *testing.T) {
	runDir := setupFixtureRun(t)
	dispatchDir := filepath.Join(runDir, "dispatches", "d01")
	os.MkdirAll(dispatchDir, 0o755)
	writeTestMeta(t, runDir, "d01", "worker", 1)

	setEnv(t,
		"FABLEBOUND_ROLE", "worker",
		"FABLEBOUND_DEPTH", "1",
		"FABLEBOUND_DISPATCH_ID", "d01",
		"FABLEBOUND_RUN_DIR", runDir,
	)

	// Capture stderr via a pipe trick — hook.Run writes to os.Stderr directly.
	// We test only that no error is returned (exit 0) for unknown events.
	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"SomeNewEvent","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		"/workspace",
	)
	if err != nil {
		t.Errorf("expected nil error (exit 0) for unknown hook event, got: %v", err)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("expected empty stdout for unknown event, got: %s", out)
	}
}

func checkJSONLLines(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if sc.Text() != "" {
			lines++
		}
	}
	if lines != want {
		t.Errorf("%s: got %d lines, want %d", path, lines, want)
	}
}
