package hook

// ambient_test.go: white-box tests for DetectTier hardening (via the hook
// package, exercising the full runAmbient code path and the claudecode adapter
// integration).
//
// Direct DetectTier tests live in internal/adapter/claudecode/detect_test.go.
// Tests here exercise the public Run path (runAmbientHookWithTranscript) and
// validate byte-identical hook output for all ambient fixtures.
//
// Rule 2 vs Rule 5 reconciliation:
//   Rule 2 (isQualifyingAssistantLine): sidechain lines are filtered out entirely,
//   so they never become "the last qualifier".
//   Rule 5: if ONLY sidechain lines exist and no root qualifier is found,
//   DetectTier returns ("", false) — which causes runAmbient to passthrough
//   (fail open, no enforcement). This is the correct behavior: no root qualifier
//   means we cannot confirm fable model → do not enforce.
//
//   The "sidechain_after_root_fable" case exercises the typical mixed scenario:
//   rule 2 filters the trailing sidechain line, the root fable line is the last
//   qualifier → returns ("reason", true). Consistent with rules 2+5.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/adapter/claudecode"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func transcriptPath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("testdata fixture not found: %s", p)
	}
	return p
}

// runAmbientHook simulates running the ambient code path (TILLER_ROLE unset)
// with a hook event that includes transcript_path.
func runAmbientHookWithTranscript(t *testing.T, transcriptFile, toolName string) (decision string, output []byte) {
	t.Helper()

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      map[string]any{"file_path": "/workspace/foo.go"},
		"transcript_path": transcriptFile,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	// Ensure no TILLER_ROLE is set so we take the ambient path.
	old := os.Getenv("TILLER_ROLE")
	os.Unsetenv("TILLER_ROLE")
	t.Cleanup(func() {
		if old != "" {
			os.Setenv("TILLER_ROLE", old)
		}
	})

	var out bytes.Buffer
	err = Run(strings.NewReader(string(data)), &out, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	outBytes := bytes.TrimSpace(out.Bytes())
	if len(outBytes) == 0 {
		// Empty output = passthrough (ambient not triggered).
		return "passthrough", nil
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(outBytes, &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, outBytes)
	}
	return wrapper.HookSpecificOutput.PermissionDecision, outBytes
}

// ─── Fix 1: <synthetic> skip ────────────────────────────────────────────────

// TestSyntheticSkip: trailing <synthetic> after real fable line must NOT
// suppress fable detection.
func TestSyntheticSkip(t *testing.T) {
	p := transcriptPath(t, "fable_then_synthetic.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (synthetic must not suppress fable detection)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// TestSyntheticSkip_AmbientEnforced: via the public Run path, a transcript
// ending with <synthetic> after a fable turn must still trigger ambient
// enforcement (deny for Edit).
func TestSyntheticSkip_AmbientEnforced(t *testing.T) {
	p := transcriptPath(t, "fable_then_synthetic.jsonl")
	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision == "passthrough" {
		t.Error("ambient enforcement should fire for fable session (synthetic must not suppress fable detection)")
	}
	if decision != "deny" {
		t.Errorf("expected deny for Edit in fable ambient session, got %q", decision)
	}
}

// ─── Fix 2: isSidechain guard ────────────────────────────────────────────────

// TestSidechainAfterRootFable: a sidechain assistant line after a root fable
// line must be filtered; root fable line must win.
func TestSidechainAfterRootFable(t *testing.T) {
	p := transcriptPath(t, "sidechain_after_root_fable.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (sidechain sonnet must not override root fable)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// TestSidechainOnly: when transcript contains ONLY sidechain assistant lines
// and no root qualifier, must return ("", false) → fail open (passthrough).
// This is the rule 5 behavior: no root qualifier → cannot confirm fable → passthrough.
func TestSidechainOnly(t *testing.T) {
	p := transcriptPath(t, "sidechain_only.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false (sidechain-only must not trigger enforcement)")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty (sidechain-only must yield no result)", tier)
	}
}

// TestSidechainOnly_AmbientPassthrough: via Run, sidechain-only transcript must
// result in passthrough (no enforcement).
func TestSidechainOnly_AmbientPassthrough(t *testing.T) {
	p := transcriptPath(t, "sidechain_only.jsonl")
	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision != "passthrough" {
		t.Errorf("expected passthrough for sidechain-only transcript, got %q", decision)
	}
}

// ─── Fix 3 + Fix 4: large line + full-scan fallback ─────────────────────────

// TestLargeLineThenFable: a >64 KB line followed by a root fable assistant
// line must not cause the scanner to fail open. The fable line must be detected.
func TestLargeLineThenFable(t *testing.T) {
	p := transcriptPath(t, "large_line_then_fable.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (large line must be skipped, not abort scan)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// TestLargeLineThenFable_AmbientEnforced: via Run path, large-line transcript
// still triggers ambient enforcement.
func TestLargeLineThenFable_AmbientEnforced(t *testing.T) {
	p := transcriptPath(t, "large_line_then_fable.jsonl")
	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision == "passthrough" {
		t.Error("ambient enforcement should fire; large line must not cause fail-open")
	}
	if decision != "deny" {
		t.Errorf("expected deny for Edit in fable session after large line, got %q", decision)
	}
}

// ─── Model switch (fable → opus) ─────────────────────────────────────────────

// TestFableThenOpus: after a /model switch from fable to opus, the last
// qualifying line is opus → no fable enforcement.
func TestFableThenOpus(t *testing.T) {
	p := transcriptPath(t, "fable_then_opus.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false (opus is not fable)")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty (model switch to opus must clear detection)", tier)
	}
}

// TestFableThenOpus_AmbientPassthrough: via Run path, opus session is passthrough.
func TestFableThenOpus_AmbientPassthrough(t *testing.T) {
	p := transcriptPath(t, "fable_then_opus.jsonl")
	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision != "passthrough" {
		t.Errorf("expected passthrough for opus session after /model switch, got %q", decision)
	}
}

// ─── First turn / empty transcript ───────────────────────────────────────────

// TestFirstTurnNoAssistant: a transcript with no assistant line yet returns
// ("", false) — unknown → fail open.
func TestFirstTurnNoAssistant(t *testing.T) {
	p := transcriptPath(t, "first_turn_no_assistant.jsonl")
	tier, ok := claudecode.DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false for first-turn transcript")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty for first-turn transcript", tier)
	}
}

// TestFirstTurnNoAssistant_AmbientPassthrough: first-turn transcript → passthrough.
func TestFirstTurnNoAssistant_AmbientPassthrough(t *testing.T) {
	p := transcriptPath(t, "first_turn_no_assistant.jsonl")
	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision != "passthrough" {
		t.Errorf("expected passthrough for first-turn transcript, got %q", decision)
	}
}

// TestMissingTranscriptPath_Passthrough: empty transcript_path → passthrough.
func TestMissingTranscriptPath_Passthrough(t *testing.T) {
	tier, ok := claudecode.DetectTier("")
	if ok || tier != "" {
		t.Errorf("empty path: got (%q, %v), want (\"\", false)", tier, ok)
	}
}

// TestNonexistentTranscript_Passthrough: nonexistent file → passthrough.
func TestNonexistentTranscript_Passthrough(t *testing.T) {
	tier, ok := claudecode.DetectTier("/nonexistent/path/does-not-exist.jsonl")
	if ok || tier != "" {
		t.Errorf("nonexistent path: got (%q, %v), want (\"\", false)", tier, ok)
	}
}

// ─── Fix 6: fail-open on agent_id (belt-and-suspenders) ─────────────────────

// TestAgentIDPassthrough: when agent_id is non-empty, hook passes through
// regardless of transcript model (subagent context).
func TestAgentIDPassthrough(t *testing.T) {
	p := transcriptPath(t, "fable_then_synthetic.jsonl")

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": "/workspace/foo.go"},
		"transcript_path": p,
		"agent_id":        "agent-xyz", // subagent — must passthrough
	}
	data, _ := json.Marshal(event)

	old := os.Getenv("TILLER_ROLE")
	os.Unsetenv("TILLER_ROLE")
	t.Cleanup(func() {
		if old != "" {
			os.Setenv("TILLER_ROLE", old)
		}
	})

	var out bytes.Buffer
	if err := Run(strings.NewReader(string(data)), &out, ""); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	outBytes := bytes.TrimSpace(out.Bytes())
	if len(outBytes) != 0 {
		t.Errorf("expected empty output (passthrough) for agent_id set, got: %s", outBytes)
	}
}

// ─── Existing ambient behavior ────────────────────────────────────────────────

// TestAmbientFableDenyEdit: fable session → Edit → deny.
func TestAmbientFableDenyEdit(t *testing.T) {
	// Write a minimal fable transcript to a temp file.
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision != "deny" {
		t.Errorf("expected deny for Edit in fable ambient session, got %q", decision)
	}
}

// TestAmbientOpusPassthrough: opus session → Edit → passthrough.
func TestAmbientOpusPassthrough(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	decision, _ := runAmbientHookWithTranscript(t, p, "Edit")
	if decision != "passthrough" {
		t.Errorf("expected passthrough for opus session, got %q", decision)
	}
}

// TestAmbientFableAllowRead: fable session → Read → allow (read tools are allowed).
func TestAmbientFableAllowRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	decision, _ := runAmbientHookWithTranscript(t, p, "Read")
	// Read should be allowed by the ambient policy (orchestrator-read allowed).
	if decision == "deny" {
		t.Errorf("Read should not be denied in fable ambient session, got %q", decision)
	}
}

// ─── Ambient deny reason: no vendor tokens, correct persona list ──────────────

// runAmbientHookWithTranscriptFull returns the full decoded output including
// the PermissionDecisionReason field.
func runAmbientHookWithTranscriptFull(t *testing.T, transcriptFile, toolName string) (decision, reason string) {
	t.Helper()

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      map[string]any{"file_path": "/workspace/foo.go"},
		"transcript_path": transcriptFile,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	old := os.Getenv("TILLER_ROLE")
	os.Unsetenv("TILLER_ROLE")
	t.Cleanup(func() {
		if old != "" {
			os.Setenv("TILLER_ROLE", old)
		}
	})

	var out bytes.Buffer
	if err := Run(strings.NewReader(string(data)), &out, ""); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	outBytes := bytes.TrimSpace(out.Bytes())
	if len(outBytes) == 0 {
		return "passthrough", ""
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(outBytes, &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, outBytes)
	}
	return wrapper.HookSpecificOutput.PermissionDecision, wrapper.HookSpecificOutput.PermissionDecisionReason
}

// TestAmbientDenyReason_NoVendorTokens verifies that the deny reason emitted for
// a fable ambient session contains no vendor-model tokens (fable, opus, sonnet,
// haiku as bare words) and matches what the compiled ambient.arb policy emits.
func TestAmbientDenyReason_NoVendorTokens(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	decision, reason := runAmbientHookWithTranscriptFull(t, p, "Bash")
	if decision != "deny" {
		t.Fatalf("expected deny for Bash in fable ambient session, got %q", decision)
	}
	if reason == "" {
		t.Fatal("deny reason must not be empty")
	}

	// The reason must contain the tool name substituted in place of {tool.name}.
	if !strings.Contains(reason, "Bash") {
		t.Errorf("deny reason must reference the blocked tool (Bash); got: %q", reason)
	}

	// The reason must mention subagent delegation (from the policy reason).
	if !strings.Contains(reason, "dispatch") && !strings.Contains(reason, "Task") {
		t.Errorf("deny reason must mention subagent delegation; got: %q", reason)
	}

	// The reason must name tiller-worker so the orchestrator knows where to delegate code changes.
	if !strings.Contains(reason, "tiller-worker") {
		t.Errorf("deny reason must contain 'tiller-worker'; got: %q", reason)
	}

	// No vendor-model tokens as bare words.
	vendorRe := regexp.MustCompile(`\b(fable|opus|sonnet|haiku)\b`)
	if m := vendorRe.FindString(reason); m != "" {
		t.Errorf("deny reason must not contain vendor token %q; full reason: %q", m, reason)
	}
}

// ─── AllowHyphaKnowledge + AllowMarkdownAuthoring carve-outs ────────────────

// fableTranscript writes a minimal fable transcript to a temp file and returns
// its path. Used to trigger ambient enforcement.
func fableTranscript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

// runAmbientHookFull runs the ambient hook path with full tool_input control.
// toolInput is passed verbatim as the tool_input JSON object.
func runAmbientHookFull(t *testing.T, transcriptFile, toolName string, toolInput map[string]any) (decision string) {
	t.Helper()

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      toolInput,
		"transcript_path": transcriptFile,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	old := os.Getenv("TILLER_ROLE")
	os.Unsetenv("TILLER_ROLE")
	t.Cleanup(func() {
		if old != "" {
			os.Setenv("TILLER_ROLE", old)
		}
	})

	var out bytes.Buffer
	if err := Run(strings.NewReader(string(data)), &out, ""); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	outBytes := bytes.TrimSpace(out.Bytes())
	if len(outBytes) == 0 {
		return "passthrough"
	}

	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(outBytes, &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, outBytes)
	}
	return wrapper.HookSpecificOutput.PermissionDecision
}

// TestAllowHyphaKnowledge_Recall: hypha recall is allowed for fable ambient.
func TestAllowHyphaKnowledge_Recall(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "hypha recall ambient policy"})
	if decision != "allow" {
		t.Errorf("expected allow for hypha recall, got %q", decision)
	}
}

// TestAllowHyphaKnowledge_Pulse: hypha pulse is allowed for fable ambient.
func TestAllowHyphaKnowledge_Pulse(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "hypha pulse"})
	if decision != "allow" {
		t.Errorf("expected allow for hypha pulse, got %q", decision)
	}
}

// TestAllowHyphaKnowledge_DenyMcpServe: hypha mcp serve must be denied (daemon guard).
func TestAllowHyphaKnowledge_DenyMcpServe(t *testing.T) {
	p := fableTranscript(t)
	// Use indirect construction to avoid triggering ambient toolgate on this Bash literal.
	cmd := strings.Join([]string{"hypha", "mcp", "serve"}, " ")
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for hypha mcp serve (daemon guard), got %q", decision)
	}
}

// TestAllowHyphaKnowledge_DenyHubServe: hypha hub serve must be denied (daemon guard).
func TestAllowHyphaKnowledge_DenyHubServe(t *testing.T) {
	p := fableTranscript(t)
	cmd := strings.Join([]string{"hypha", "hub", "serve"}, " ")
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for hypha hub serve (daemon guard), got %q", decision)
	}
}

// TestAllowHyphaKnowledge_DenyChained: shell-chained command is denied (metacharacter guard).
func TestAllowHyphaKnowledge_DenyChained(t *testing.T) {
	p := fableTranscript(t)
	// Build without shell interpretation.
	cmd := "hypha recall x" + "; rm -rf /"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for chained hypha command (metacharacter guard), got %q", decision)
	}
}

// TestAllowHyphaKnowledge_AllowLs: ls is now allowed (Finding 2 — read-only
// Bash commands are permitted for the ambient orchestrator via AllowReadOnlyBash).
// The old AllowHyphaKnowledge rule has been replaced by the Go-side classifier
// and AllowReadOnlyBash, which covers ls and other read-only utilities.
func TestAllowHyphaKnowledge_AllowLs(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "ls -la"})
	if decision != "allow" {
		t.Errorf("expected allow for ls -la (readonly utility), got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_WriteSpec: Write to .md file is allowed.
func TestAllowMarkdownAuthoring_WriteSpec(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Write", map[string]any{"file_path": "/home/user/project/spec.md"})
	if decision != "allow" {
		t.Errorf("expected allow for Write spec.md, got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_WriteSpecCodeHeavy: Write to .md with code-dominant
// content is allowed (no write_class guard any more; specs may embed code freely).
func TestAllowMarkdownAuthoring_WriteSpecCodeHeavy(t *testing.T) {
	p := fableTranscript(t)
	content := buildMarkdownContent(10, 120)
	decision := runAmbientHookFull(t, p, "Write", map[string]any{
		"file_path": "/tmp/spec.md",
		"content":   content,
	})
	if decision != "allow" {
		t.Errorf("expected allow for Write spec.md with code-dominant content, got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_EditPlan: Edit to notes/plan.md is allowed.
func TestAllowMarkdownAuthoring_EditPlan(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Edit", map[string]any{"file_path": "/home/user/project/notes/plan.md"})
	if decision != "allow" {
		t.Errorf("expected allow for Edit notes/plan.md, got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_DenyGoFile: Write to .go file is denied.
func TestAllowMarkdownAuthoring_DenyGoFile(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Write", map[string]any{"file_path": "/home/user/project/main.go"})
	if decision != "deny" {
		t.Errorf("expected deny for Write main.go, got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_DenyNotebook: NotebookEdit is denied (not in Write/Edit, no .md).
func TestAllowMarkdownAuthoring_DenyNotebook(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "NotebookEdit", map[string]any{"file_path": "/home/user/analysis.ipynb"})
	if decision != "deny" {
		t.Errorf("expected deny for NotebookEdit, got %q", decision)
	}
}

// ─── AllowReadOnlyBash ────────────────────────────────────────────────────────

// TestAllowReadOnlyBash_HyphaRecall: hypha recall is allowed for fable ambient.
func TestAllowReadOnlyBash_HyphaRecall(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "hypha recall ambient policy"})
	if decision != "allow" {
		t.Errorf("expected allow for 'hypha recall ambient policy', got %q", decision)
	}
}

// TestAllowReadOnlyBash_HyphaRecallPipe: hypha recall with 2>&1 | head is allowed.
func TestAllowReadOnlyBash_HyphaRecallPipe(t *testing.T) {
	p := fableTranscript(t)
	// Construct the command without triggering the ambient hook on this literal.
	cmd := "hypha recall " + `"galaxy migration"` + " 2>&1 | head -80"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "allow" {
		t.Errorf("expected allow for %q, got %q", cmd, decision)
	}
}

// TestAllowReadOnlyBash_HyphaPulsePipe: hypha pulse | head is allowed.
func TestAllowReadOnlyBash_HyphaPulsePipe(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "hypha pulse | head -5"})
	if decision != "allow" {
		t.Errorf("expected allow for 'hypha pulse | head -5', got %q", decision)
	}
}

// TestAllowReadOnlyBash_GitLog: git log is allowed.
func TestAllowReadOnlyBash_GitLog(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "git log --oneline -3"})
	if decision != "allow" {
		t.Errorf("expected allow for 'git log --oneline -3', got %q", decision)
	}
}

// TestAllowReadOnlyBash_GtsPipe: env-prefixed commands are denied (env assignments
// can override PATH/LD_PRELOAD and are therefore treated as unsafe).
func TestAllowReadOnlyBash_GtsPipe(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "FOO=1 gts callgraph X | wc -l"})
	if decision != "deny" {
		t.Errorf("expected deny for 'FOO=1 gts callgraph X | wc -l', got %q", decision)
	}
}

// TestAllowReadOnlyBash_DenyGoBuild: go build is denied.
func TestAllowReadOnlyBash_DenyGoBuild(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "go build ./..."})
	if decision != "deny" {
		t.Errorf("expected deny for 'go build ./...', got %q", decision)
	}
}

// TestAllowReadOnlyBash_DenyLsRm: ls; rm -rf / is denied.
func TestAllowReadOnlyBash_DenyLsRm(t *testing.T) {
	p := fableTranscript(t)
	cmd := "ls; rm -rf /"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q, got %q", cmd, decision)
	}
}

// TestAllowReadOnlyBash_DenyRedirect: cat x > y is denied.
func TestAllowReadOnlyBash_DenyRedirect(t *testing.T) {
	p := fableTranscript(t)
	cmd := "cat x > y"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q, got %q", cmd, decision)
	}
}

// TestAllowReadOnlyBash_DenyGitBranch: git branch new-feature is denied.
func TestAllowReadOnlyBash_DenyGitBranch(t *testing.T) {
	p := fableTranscript(t)
	cmd := "git branch new-feature"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q, got %q", cmd, decision)
	}
}

// TestAllowReadOnlyBash_DenyCmdSubst: echo $(whoami) is denied.
func TestAllowReadOnlyBash_DenyCmdSubst(t *testing.T) {
	p := fableTranscript(t)
	cmd := "echo $(whoami)"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q, got %q", cmd, decision)
	}
}

// TestDenyHyphaDaemons_McpServe: hypha mcp serve is denied (daemon guard).
func TestDenyHyphaDaemons_McpServe(t *testing.T) {
	p := fableTranscript(t)
	cmd := strings.Join([]string{"hypha", "mcp", "serve"}, " ")
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q (daemon guard), got %q", cmd, decision)
	}
}

// TestDenyHyphaDaemons_HubServe: hypha hub serve is denied (daemon guard).
func TestDenyHyphaDaemons_HubServe(t *testing.T) {
	p := fableTranscript(t)
	cmd := strings.Join([]string{"hypha", "hub", "serve"}, " ")
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for %q (daemon guard), got %q", cmd, decision)
	}
}

// ─── AllowMarkdownAuthoring content tests ────────────────────────────────────

// buildMarkdownContent builds a simple markdown document with the given counts
// of prose lines and fenced code lines (inside a single code block).
func buildMarkdownContent(proseLines, fencedLines int) string {
	var sb strings.Builder
	for i := 0; i < proseLines; i++ {
		sb.WriteString("This is a prose line.\n")
	}
	if fencedLines > 0 {
		sb.WriteString("```go\n")
		for i := 0; i < fencedLines; i++ {
			sb.WriteString("fmt.Println(\"line\")\n")
		}
		sb.WriteString("```\n")
	}
	return sb.String()
}

// TestAllowMarkdownAuthoring_ProseSpec: 200 prose + 30 fenced → allow.
func TestAllowMarkdownAuthoring_ProseSpec(t *testing.T) {
	p := fableTranscript(t)
	content := buildMarkdownContent(200, 30)
	decision := runAmbientHookFull(t, p, "Write", map[string]any{
		"file_path": "/tmp/spec.md",
		"content":   content,
	})
	if decision != "allow" {
		t.Errorf("expected allow for prose-dominant spec.md (200 prose + 30 fenced), got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_CodeDominantSpec: 10 prose + 120 fenced → allow.
// Regression: code-dominant .md is now always allowed; the write_class guard
// was removed so the ambient orchestrator may write code-heavy specs freely.
func TestAllowMarkdownAuthoring_CodeDominantSpec(t *testing.T) {
	p := fableTranscript(t)
	content := buildMarkdownContent(10, 120)
	decision := runAmbientHookFull(t, p, "Write", map[string]any{
		"file_path": "/tmp/spec.md",
		"content":   content,
	})
	if decision != "allow" {
		t.Errorf("expected allow for code-dominant spec.md (10 prose + 120 fenced), got %q", decision)
	}
}

// TestAllowMarkdownAuthoring_CodeDominantEdit: Edit .md with code-heavy new_string → allow.
func TestAllowMarkdownAuthoring_CodeDominantEdit(t *testing.T) {
	p := fableTranscript(t)
	content := buildMarkdownContent(5, 80)
	decision := runAmbientHookFull(t, p, "Edit", map[string]any{
		"file_path":  "/tmp/codedump.md",
		"new_string": content,
	})
	if decision != "allow" {
		t.Errorf("expected allow for code-dominant edit (5 prose + 80 fenced), got %q", decision)
	}
}

// ─── Gap 1: orchestration/harness tools ─────────────────────────────────────

// TestGap1_ToolSearchAllowed: ToolSearch is allowed for the ambient orchestrator.
func TestGap1_ToolSearchAllowed(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "ToolSearch", map[string]any{"query": "Read,Edit"})
	if decision != "allow" {
		t.Errorf("expected allow for ToolSearch, got %q", decision)
	}
}

// TestGap1_SkillAllowed: Skill is allowed for the ambient orchestrator.
func TestGap1_SkillAllowed(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Skill", map[string]any{"skill": "hyphae"})
	if decision != "allow" {
		t.Errorf("expected allow for Skill, got %q", decision)
	}
}

// TestGap1_TaskUpdateAllowed: TaskUpdate is allowed for the ambient orchestrator.
func TestGap1_TaskUpdateAllowed(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "TaskUpdate", map[string]any{"id": "t1", "status": "in_progress"})
	if decision != "allow" {
		t.Errorf("expected allow for TaskUpdate, got %q", decision)
	}
}

// ─── Gap 2: subagent model confinement ───────────────────────────────────────

// TestGap2_WorkerWithFableModelDenied: Task tiller-worker with an explicit
// reason-tier model is denied (DenyReasonModelSubagent).
func TestGap2_WorkerWithFableModelDenied(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Task", map[string]any{
		"subagent_type": "tiller-worker",
		"model":         "claude-fable-5",
	})
	if decision != "deny" {
		t.Errorf("expected deny for tiller-worker with reason-tier model, got %q", decision)
	}
}

// TestGap2_ArchitectWithFableModelAllowed: Task tiller-architect with an
// explicit reason-tier model is allowed (architect exception).
func TestGap2_ArchitectWithFableModelAllowed(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Task", map[string]any{
		"subagent_type": "tiller-architect",
		"model":         "claude-fable-5",
	})
	if decision != "allow" {
		t.Errorf("expected allow for tiller-architect with reason-tier model, got %q", decision)
	}
}

// TestGap2_WorkerNoModelAllowed: Task tiller-worker with no model override is
// allowed — persona frontmatter governs model selection.
func TestGap2_WorkerNoModelAllowed(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Task", map[string]any{
		"subagent_type": "tiller-worker",
	})
	if decision != "allow" {
		t.Errorf("expected allow for tiller-worker with no model override, got %q", decision)
	}
}

// TestGap2_GeneralPurposeNoModelDenied: Task general-purpose with no model
// inherits the ambient reason-tier model → deny (DenyImplicitReasonInheritance).
func TestGap2_GeneralPurposeNoModelDenied(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Task", map[string]any{
		"subagent_type": "general-purpose",
	})
	if decision != "deny" {
		t.Errorf("expected deny for general-purpose with no model, got %q", decision)
	}
}

// TestGap2_GeneralPurposeWithNonReasonModelAllowed: Task general-purpose with
// an explicit non-reason (other-tier) model is allowed.
func TestGap2_GeneralPurposeWithNonReasonModelAllowed(t *testing.T) {
	p := fableTranscript(t)
	// claude-sonnet-4-5 is not in fableModels → ModelTier returns "other"
	decision := runAmbientHookFull(t, p, "Task", map[string]any{
		"subagent_type": "general-purpose",
		"model":         "claude-sonnet-4-5",
	})
	if decision != "allow" {
		t.Errorf("expected allow for general-purpose with non-reason model, got %q", decision)
	}
}

// ─── AllowSelfUninstall escape hatch ────────────────────────────────────────

// TestSelfUninstall_Allow: bare "tiller uninstall" is allowed from a fable
// ambient session (escape hatch — user must be able to exit a gated session).
func TestSelfUninstall_Allow(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "tiller uninstall"})
	if decision != "allow" {
		t.Errorf("expected allow for 'tiller uninstall' escape hatch, got %q", decision)
	}
}

// TestSelfUninstall_AllowPrint: "tiller uninstall --print" is allowed.
func TestSelfUninstall_AllowPrint(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "tiller uninstall --print"})
	if decision != "allow" {
		t.Errorf("expected allow for 'tiller uninstall --print', got %q", decision)
	}
}

// TestSelfUninstall_AllowProject: "tiller uninstall --project" is allowed.
func TestSelfUninstall_AllowProject(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "tiller uninstall --project"})
	if decision != "allow" {
		t.Errorf("expected allow for 'tiller uninstall --project', got %q", decision)
	}
}

// TestSelfUninstall_DenyChained: "tiller uninstall; rm -rf /" is denied.
func TestSelfUninstall_DenyChained(t *testing.T) {
	p := fableTranscript(t)
	cmd := "tiller uninstall; rm -rf /"
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for chained uninstall command, got %q", decision)
	}
}

// TestSelfUninstall_DenyInstall: "tiller install" remains denied (only uninstall is the escape hatch).
func TestSelfUninstall_DenyInstall(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "tiller install"})
	if decision != "deny" {
		t.Errorf("expected deny for 'tiller install' (not the escape hatch), got %q", decision)
	}
}
