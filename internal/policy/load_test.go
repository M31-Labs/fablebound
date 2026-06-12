package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultsCompile verifies that all embedded default policies compile
// with schema typecheck successfully.
func TestDefaultsCompile(t *testing.T) {
	for _, kind := range []string{"dispatch", "toolgate", "ambient", "ambient_next_action"} {
		t.Run(kind, func(t *testing.T) {
			loaded, err := Load(kind, "")
			if err != nil {
				t.Fatalf("Load(%q, \"\") error: %v", kind, err)
			}
			if loaded.Prog == nil {
				t.Fatal("Load returned nil Prog")
			}
			if loaded.SHA256 == "" {
				t.Fatal("Load returned empty SHA256")
			}
			if loaded.Path != "embedded:"+kind {
				t.Fatalf("Load path = %q, want %q", loaded.Path, "embedded:"+kind)
			}
		})
	}
}

// TestBogusFieldSchemaError verifies that a policy referencing a non-existent
// field fails Load with a compile/schema error.
func TestBogusFieldSchemaError(t *testing.T) {
	const src = `
rule BogusRule priority 1 {
    when { dispatch.bogus_field == "nope" }
    then Deny { reason: "test" }
}
`
	tmpDir := t.TempDir()
	policyDir := filepath.Join(tmpDir, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	arbFile := filepath.Join(policyDir, "dispatch.arb")
	if err := os.WriteFile(arbFile, []byte(src), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := Load("dispatch", tmpDir)
	if err == nil {
		t.Fatal("expected error for bogus_field reference, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestContextMapDispatch verifies that ContextMap of a populated DispatchRequest
// yields the expected nested dispatch.*/caller.*/run.* keys.
func TestContextMapDispatch(t *testing.T) {
	req := DispatchRequest{
		Role:             "worker",
		Tier:             "execute",
		Background:       false,
		BriefBytes:       1024,
		Enforcement:      "degraded",
		SandboxMode:      "process",
		SandboxProfile:   "execution",
		HorizonManifests: 1,
		CallerRole:       "orchestrator",
		CallerDepth:      0,
		CallerID:         "root",
		RunID:            "20260609-123456-ab12",
		ActiveCount:      1,
		ReasonCount:      0,
		ReasonBudget:     2,
	}

	m := ContextMap(req)

	// Check dispatch.* keys
	dispatch, ok := m["dispatch"].(map[string]any)
	if !ok {
		t.Fatalf("m[dispatch] = %T, want map[string]any", m["dispatch"])
	}
	if got := dispatch["role"]; got != "worker" {
		t.Errorf("dispatch.role = %v, want worker", got)
	}
	if got := dispatch["tier"]; got != "execute" {
		t.Errorf("dispatch.tier = %v, want execute", got)
	}
	if got := dispatch["brief_bytes"]; got != 1024 {
		t.Errorf("dispatch.brief_bytes = %v, want 1024", got)
	}
	if got := dispatch["enforcement"]; got != "degraded" {
		t.Errorf("dispatch.enforcement = %v, want degraded", got)
	}
	sandbox, ok := dispatch["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch.sandbox = %T, want map[string]any", dispatch["sandbox"])
	}
	if got := sandbox["mode"]; got != "process" {
		t.Errorf("dispatch.sandbox.mode = %v, want process", got)
	}
	if got := sandbox["profile"]; got != "execution" {
		t.Errorf("dispatch.sandbox.profile = %v, want execution", got)
	}
	horizon, ok := dispatch["horizon"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch.horizon = %T, want map[string]any", dispatch["horizon"])
	}
	if got := horizon["manifests"]; got != 1 {
		t.Errorf("dispatch.horizon.manifests = %v, want 1", got)
	}

	// Check caller.* keys
	caller, ok := m["caller"].(map[string]any)
	if !ok {
		t.Fatalf("m[caller] = %T, want map[string]any", m["caller"])
	}
	if got := caller["role"]; got != "orchestrator" {
		t.Errorf("caller.role = %v, want orchestrator", got)
	}
	if got := caller["depth"]; got != 0 {
		t.Errorf("caller.depth = %v, want 0", got)
	}
	if got := caller["dispatch_id"]; got != "root" {
		t.Errorf("caller.dispatch_id = %v, want root", got)
	}

	// Check run.* keys
	run, ok := m["run"].(map[string]any)
	if !ok {
		t.Fatalf("m[run] = %T, want map[string]any", m["run"])
	}
	if got := run["id"]; got != "20260609-123456-ab12" {
		t.Errorf("run.id = %v, want 20260609-123456-ab12", got)
	}
	if got := run["reason_budget"]; got != 2 {
		t.Errorf("run.reason_budget = %v, want 2", got)
	}
}

// TestContextMapToolCall verifies that ContextMap of a ToolCallRequest
// yields the expected agent.*/tool.*/run.* keys.
func TestContextMapToolCall(t *testing.T) {
	req := ToolCallRequest{
		Role:        "orchestrator",
		Depth:       0,
		DispatchID:  "root",
		Tool:        "Bash",
		Command:     "ls",
		FilePath:    "",
		InScratch:   false,
		InWorkspace: false,
		RunID:       "20260609-000000-zz99",
	}

	m := ContextMap(req)

	agent, ok := m["agent"].(map[string]any)
	if !ok {
		t.Fatalf("m[agent] = %T, want map[string]any", m["agent"])
	}
	if got := agent["role"]; got != "orchestrator" {
		t.Errorf("agent.role = %v, want orchestrator", got)
	}
	if got := agent["depth"]; got != 0 {
		t.Errorf("agent.depth = %v, want 0", got)
	}

	tool, ok := m["tool"].(map[string]any)
	if !ok {
		t.Fatalf("m[tool] = %T, want map[string]any", m["tool"])
	}
	if got := tool["name"]; got != "Bash" {
		t.Errorf("tool.name = %v, want Bash", got)
	}
	if got := tool["command"]; got != "ls" {
		t.Errorf("tool.command = %v, want ls", got)
	}
}
