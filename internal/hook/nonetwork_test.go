package hook_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/hook"
)

// TestNoNetworkInGate verifies that the toolgate hot path (tiller hook)
// makes a correct local policy decision even when TILLER_STORE=pg and
// TILLER_STORE_DSN points at an unreachable host.
//
// The hook path MUST resolve identity via TILLER_RUN_DIR (fsstore semantics)
// and evaluate the toolgate policy entirely in-process — no dial, no network.
//
// Acceptance criteria (plan P3.4):
//   - Decision is correct (deny for orchestrator + Bash ls).
//   - Wall-clock time < 2 seconds (no network timeout in path).
func TestNoNetworkInGate(t *testing.T) {
	// Build a legitimate run dir so identity verification passes.
	workspace := t.TempDir()
	runDir := makeRealRunDir(t, workspace)
	dispatchDir := filepath.Join(runDir, "dispatches", "root")
	if err := os.MkdirAll(dispatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestMeta(t, runDir, "root", "orchestrator", 0)

	// Set TILLER_STORE=pg + unreachable DSN.
	// The hook must NOT use this store for identity resolution or gating —
	// it must use the fsstore (TILLER_RUN_DIR path) exclusively.
	setEnv(t,
		"TILLER_ROLE", "orchestrator",
		"TILLER_DEPTH", "0",
		"TILLER_DISPATCH_ID", "root",
		"TILLER_RUN_DIR", runDir,
		"TILLER_STORE", "pg",
		"TILLER_STORE_DSN", "postgres://invalid.invalid:1/nosuchdb?connect_timeout=1",
	)

	start := time.Now()

	// Run the hook: orchestrator + Bash ls → deny (toolgate policy blocks
	// direct Bash for orchestrator role).
	out, err := runHookWithWorkspace(t,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls -la"}}`,
		workspace,
	)

	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("hook took %v — expected < 2s (network dial in hot path?)", elapsed)
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the decision is correct (deny — orchestrator cannot run Bash ls).
	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if jsonErr := json.Unmarshal(bytes.TrimSpace(out), &wrapper); jsonErr != nil {
		t.Fatalf("parse output: %v (raw: %s)", jsonErr, out)
	}
	if wrapper.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny, got %q — TILLER_STORE_DSN should not affect toolgate decision", wrapper.HookSpecificOutput.PermissionDecision)
	}
}

// TestNoNetworkInGate_ReadIdentityIgnoresStore confirms that hook.ReadIdentity
// does not read TILLER_STORE or TILLER_STORE_DSN — store env vars must be invisible
// to the hook identity resolution path.
func TestNoNetworkInGate_ReadIdentityIgnoresStore(t *testing.T) {
	setEnv(t,
		"TILLER_ROLE", "worker",
		"TILLER_DEPTH", "1",
		"TILLER_DISPATCH_ID", "d01",
		"TILLER_RUN_DIR", "/some/run/dir",
		"TILLER_STORE", "pg",
		"TILLER_STORE_DSN", "postgres://invalid.invalid:1/nosuchdb",
	)

	id, ok := hook.ReadIdentity()
	if !ok {
		t.Fatal("ReadIdentity returned ok=false with TILLER_ROLE set")
	}
	if id.Role != "worker" {
		t.Errorf("role = %q, want worker", id.Role)
	}
	if id.Depth != 1 {
		t.Errorf("depth = %d, want 1", id.Depth)
	}
	// No store fields exist on Identity — this is a compile-time guarantee.
	// The test confirms that ReadIdentity completes without attempting any
	// network operation (no hang, no error from store connection).
}
