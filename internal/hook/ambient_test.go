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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/adapter/claudecode"
	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
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

func TestAmbientUsesProjectPolicyOverride(t *testing.T) {
	workspace := t.TempDir()
	policyDir := filepath.Join(workspace, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatalf("mkdir policy dir: %v", err)
	}
	const distinctiveReason = "project ambient override denied root Read"
	override := `
rule DenyProjectRead priority 1 {
    when { tool.name == "Read" }
    then Deny { reason: "` + distinctiveReason + `" }
}
`
	if err := os.WriteFile(filepath.Join(policyDir, "ambient.arb"), []byte(override), 0o644); err != nil {
		t.Fatalf("write ambient override: %v", err)
	}

	transcript := filepath.Join(workspace, "t.jsonl")
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"file_path": filepath.Join(workspace, "foo.go")},
		"transcript_path": transcript,
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
	if err := Run(strings.NewReader(string(data)), &out, workspace); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	outBytes := bytes.TrimSpace(out.Bytes())
	if len(outBytes) == 0 {
		t.Fatal("expected ambient policy decision, got passthrough")
	}
	if decision := parseAmbientDecision(t, outBytes); decision != "deny" {
		t.Fatalf("decision = %q, want deny; raw output: %s", decision, outBytes)
	}
	reason := parseDecisionReason(t, outBytes)
	if !strings.Contains(reason, "DenyProjectRead") || !strings.Contains(reason, distinctiveReason) {
		t.Fatalf("override reason not used; got %q", reason)
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

func fableTranscriptWithUsage(t *testing.T, inputTokens, outputTokens int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	line := fmt.Sprintf(`{"type":"assistant","isSidechain":false,"message":{"model":"claude-fable-5","role":"assistant","usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"text","text":"hi"}]}}`+"\n", inputTokens, outputTokens)
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

func codexTranscript(t *testing.T, effort string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "codex.jsonl")
	line := `{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"` + effort + `"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatalf("write Codex transcript: %v", err)
	}
	return p
}

func codexTranscriptWithUsage(t *testing.T, effort string, outputTokens int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "codex.jsonl")
	lines := `{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"` + effort + `"}}` + "\n" +
		`{"type":"turn_end","payload":{"usage":{"output_tokens":` + fmt.Sprint(outputTokens) + `}}}` + "\n"
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatalf("write Codex transcript: %v", err)
	}
	return p
}

func makeAmbientLifecycleRunDir(t *testing.T, workspace string) string {
	t.Helper()
	runID := "20260101-000000-codex"
	runDir := filepath.Join(workspace, ".tiller", "runs", runID)
	for _, sub := range []string{"audit", "notes", "dispatches"} {
		if err := os.MkdirAll(filepath.Join(runDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"run_id":    runID,
		"task":      "test",
		"workspace": workspace,
		"status":    "running",
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func runCodexAmbientHook(t *testing.T, transcriptFile, toolName string, toolInput map[string]any) []byte {
	t.Helper()
	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      toolInput,
		"transcript_path": transcriptFile,
		"agent_id":        "",
		"model":           "gpt-5.5",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	return bytes.TrimSpace(out.Bytes())
}

func parseAmbientDecision(t *testing.T, out []byte) string {
	t.Helper()
	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, out)
	}
	return wrapper.HookSpecificOutput.PermissionDecision
}

func parseAdditionalContext(t *testing.T, out []byte) string {
	t.Helper()
	var wrapper struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("parse context output: %v (raw: %s)", err, out)
	}
	return wrapper.HookSpecificOutput.AdditionalContext
}

func parseDecisionReason(t *testing.T, out []byte) string {
	t.Helper()
	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("parse reason output: %v (raw: %s)", err, out)
	}
	return wrapper.HookSpecificOutput.PermissionDecisionReason
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

func TestCodexAmbientApplyPatchDenied(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "apply_patch", map[string]any{
		"command": "*** Begin Patch\n*** Update File: main.go\n@@\n-old\n+new\n*** End Patch",
	})
	if len(out) == 0 {
		t.Fatal("expected Codex deny output for root apply_patch in xhigh session")
	}
	if decision := parseAmbientDecision(t, out); decision != "deny" {
		t.Fatalf("expected deny, got %q", decision)
	}
}

func TestClaudeAmbientGovernedPreToolUseAppendsUsageLedger(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := fableTranscriptWithUsage(t, 12, 34)
	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": filepath.Join(workspace, "foo.go")},
		"transcript_path": p,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	runHook := func() {
		t.Helper()
		var out bytes.Buffer
		if err := Run(strings.NewReader(string(data)), &out, workspace); err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if decision := parseAmbientDecision(t, bytes.TrimSpace(out.Bytes())); decision != "deny" {
			t.Fatalf("expected deny, got %q", decision)
		}
	}
	runHook()
	runHook()

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ledger event count=%d want 1: %#v", len(events), events)
	}
	if events[0].Kind != "claude.ambient_usage" || events[0].Backend != "claude-code" {
		t.Fatalf("unexpected ledger event: %#v", events[0])
	}
	if events[0].TokenUsage == nil || events[0].TokenUsage.InputTokens != 12 || events[0].TokenUsage.OutputTokens != 34 {
		t.Fatalf("ledger token usage mismatch: %#v", events[0].TokenUsage)
	}
	if events[0].ID == "" {
		t.Fatal("ledger event ID is empty")
	}
	hasUsageRef := false
	for _, ref := range events[0].Refs {
		if strings.HasPrefix(ref, "usage:") {
			hasUsageRef = true
			break
		}
	}
	if !hasUsageRef {
		t.Fatalf("ledger event missing stable usage ref: %#v", events[0].Refs)
	}
	status, err := os.ReadFile(filepath.Join(runDir, "status.md"))
	if err != nil {
		t.Fatalf("read status.md: %v", err)
	}
	if got := string(status); !strings.Contains(got, "claude.ambient_usage") || !strings.Contains(got, "- ledger: input=12 output=34") {
		t.Fatalf("status.md missing Claude usage content:\n%s", got)
	}
}

func TestClaudeAmbientGovernedPreToolUseWritesAuditLine(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := fableTranscript(t)
	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": filepath.Join(workspace, "foo.go")},
		"transcript_path": p,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := Run(strings.NewReader(string(data)), &out, workspace); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if decision := parseAmbientDecision(t, bytes.TrimSpace(out.Bytes())); decision != "deny" {
		t.Fatalf("expected deny, got %q", decision)
	}

	auditPath := filepath.Join(runDir, "audit", "toolgate.jsonl")
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("audit line count=%d want 1; raw=%q", len(lines), raw)
	}

	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid audit JSON: %v", err)
	}
	if requestID, _ := ev["request_id"].(string); !strings.HasPrefix(requestID, "ambient-root-") {
		t.Fatalf("request_id = %q, want ambient-root-*", requestID)
	}
	if ctx, ok := ev["context"].(map[string]any); !ok || len(ctx) == 0 {
		t.Fatalf("context is empty or missing: %#v", ev["context"])
	}
	if rules, ok := ev["rules"].([]any); !ok || len(rules) == 0 {
		t.Fatalf("rules is empty or missing: %#v", ev["rules"])
	}
	if arbitrace, ok := ev["arbitrace"].([]any); !ok || len(arbitrace) == 0 {
		t.Fatalf("arbitrace is empty or missing: %#v", ev["arbitrace"])
	}
}

func TestCodexAmbientReadAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "Read", map[string]any{"file_path": "/workspace/main.go"})
	if len(out) != 0 {
		t.Fatalf("Codex allow should produce no stdout, got %s", out)
	}
}

func TestCodexAmbientNamespacedViewImageAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.view_image", map[string]any{"path": "/workspace/screenshot.png"})
	if len(out) != 0 {
		t.Fatalf("Codex allow for functions.view_image should produce no stdout, got %s", out)
	}
}

func TestCodexAmbientNamespacedToolSearchAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "tool_search.tool_search_tool", map[string]any{"query": "github"})
	if len(out) != 0 {
		t.Fatalf("Codex allow for tool_search.tool_search_tool should produce no stdout, got %s", out)
	}
}

func TestCodexAmbientNamespacedDiagnosticAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.list_mcp_resources", map[string]any{})
	if len(out) != 0 {
		t.Fatalf("Codex allow for functions.list_mcp_resources should produce no stdout, got %s", out)
	}
}

func TestCodexAmbientWebRunWrapperDenied(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "web.run", map[string]any{})
	if len(out) == 0 {
		t.Fatal("expected Codex deny output for generic web.run wrapper")
	}
	if decision := parseAmbientDecision(t, out); decision != "deny" {
		t.Fatalf("expected deny, got %q", decision)
	}
}

func TestCodexAmbientNamespacedMultiAgentLifecycleAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	for _, toolName := range []string{
		"multi_agent_v1spawn_agent",
		"multi_agent_v1wait_agent",
		"multi_agent_v1send_input",
		"multi_agent_v1resume_agent",
		"multi_agent_v1close_agent",
	} {
		out := runCodexAmbientHook(t, p, toolName, map[string]any{"agent_id": 1})
		if len(out) != 0 {
			t.Fatalf("Codex allow for %s should produce no stdout, got %s", toolName, out)
		}
	}
}

func TestCodexAmbientRootReadOnlyExecCommandAllowedSilent(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	for _, cmd := range []string{
		"pwd",
		"rg --files",
		"sed -n '1,40p' AGENTS.md",
		"hypha recall tiller ambient",
	} {
		out := runCodexAmbientHook(t, p, "functions.exec_command", map[string]any{"cmd": cmd})
		if len(out) != 0 {
			t.Fatalf("Codex read-only exec_command %q should produce no stdout, got %s", cmd, out)
		}
	}
}

func TestCodexAmbientRootMutatingExecCommandDenied(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.exec_command", map[string]any{"cmd": "go test ./..."})
	if len(out) == 0 {
		t.Fatal("expected Codex deny output for root mutating shell execution")
	}
	if decision := parseAmbientDecision(t, out); decision != "deny" {
		t.Fatalf("expected deny, got %q", decision)
	}
}

func TestCodexAmbientMediumPassthrough(t *testing.T) {
	p := codexTranscript(t, "medium")
	out := runCodexAmbientHook(t, p, "apply_patch", map[string]any{
		"command": "*** Begin Patch\n*** Update File: main.go\n@@\n-old\n+new\n*** End Patch",
	})
	if len(out) != 0 {
		t.Fatalf("Codex medium effort should pass through, got %s", out)
	}
}

func TestCodexSessionStartMediumPassthrough(t *testing.T) {
	p := codexTranscript(t, "medium")
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": p,
		"model":           "gpt-5.5",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("Codex medium SessionStart should pass through silently, got %s", got)
	}
}

func TestCodexSessionStartPayloadXHighAdditionalContextWithoutTranscript(t *testing.T) {
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"model":           "gpt-5.5",
		"effort":          "xhigh",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes()))
	if !strings.Contains(ctx, "Tiller ambient is active") {
		t.Fatalf("missing Codex SessionStart context: %s", ctx)
	}
}

func TestCodexSessionStartPayloadMediumPassthroughWithoutTranscript(t *testing.T) {
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"model":           "gpt-5.5",
		"effort":          "medium",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("Codex medium SessionStart payload should pass through silently, got %s", got)
	}
}

func TestCodexSessionStartWithoutXHighProofPassthrough(t *testing.T) {
	event := map[string]any{
		"hook_event_name": "SessionStart",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("Codex SessionStart without xhigh proof should pass through silently, got %s", got)
	}
}

func TestCodexSessionStartAdditionalContext(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": p,
		"model":           "gpt-5.5",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes()))
	for _, want := range []string{"Tiller ambient is active", "Root may read/search directly", ".tiller/scratch/codex/", "premium/reason-tier output", "descriptor-backed task list", "Codex, Claude Code, OpenCode, Cursor", "Descriptor fields", "budget tier/model ceiling", "Queue/background independent descriptors", "update descriptors from returned reports", "Git/GitHub for VCS", "Graft", "Checkpoint verified wins", "configured checkpoint tool", "spawn_agent", "agent_type=\"tiller-scout\"", "agent_type=\"tiller-summary\"", "status compaction", "stale/late report triage", "gpt-5.4-mini", "agent_type=\"tiller-worker\"", "gpt-5.5 medium", "gpt-5.5 high", "gpt-5.5 xhigh", "wait_agent/close_agent"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("SessionStart context missing %q:\n%s", want, ctx)
		}
	}
}

func TestCodexSessionStartAdditionalContextDisabled(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := ambientgate.Disable(dir); err != nil {
		t.Fatalf("disable ambient: %v", err)
	}
	p := codexTranscript(t, "xhigh")
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": p,
		"model":           "gpt-5.5",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, dir, "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes()))
	for _, want := range []string{"temporarily disabled", "Normal Codex tools are allowed", ".tiller/scratch/codex/"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("disabled SessionStart context missing %q:\n%s", want, ctx)
		}
	}
}

func TestCodexSubagentStartAdditionalContext(t *testing.T) {
	cases := []struct {
		name      string
		agentType string
		want      []string
	}{
		{
			name:      "worker",
			agentType: "tiller-worker",
			want:      []string{"Tiller execution agent", "changed files", "verification results", "Read relevant .tiller/scratch/codex/ notes first", "write final reports or handoff notes", "descriptor-compatible report contract", "checkpoint candidate yes/no", "update task status and checkpoint decisions", "checkpointable wins", "configured checkpoint tool or Git"},
		},
		{
			name:      "scout",
			agentType: "tiller-scout",
			want:      []string{"Tiller scout agent", "gpt-5.4-mini", "bounded read-only inventories", "simple summaries", "Do not edit files", "Read relevant .tiller/scratch/codex/ notes first", "descriptor-compatible report contract", "checkpoint candidate yes/no", "update task status and checkpoint decisions", "checkpointable wins", "configured checkpoint tool or Git"},
		},
		{
			name:      "summary",
			agentType: "tiller-summary",
			want:      []string{"Tiller summary agent", "gpt-5.4-mini", "read-only status compaction", "stale/late report triage", "checkpoint candidate synthesis", "recommended next action", "Do not edit files", "Read relevant .tiller/scratch/codex/ notes first", "descriptor-compatible report contract", "checkpoint candidate yes/no", "update task status and checkpoint decisions", "checkpointable wins", "configured checkpoint tool or Git"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := map[string]any{
				"hook_event_name": "SubagentStart",
				"agent_type":      tc.agentType,
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
			if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
				t.Fatalf("RunWithBackend error: %v", err)
			}
			ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes()))
			for _, want := range tc.want {
				if !strings.Contains(ctx, want) {
					t.Fatalf("SubagentStart context missing %q:\n%s", want, ctx)
				}
			}
		})
	}
}

func TestCodexNonTillerSubagentStartPassthrough(t *testing.T) {
	event := map[string]any{
		"hook_event_name": "SubagentStart",
		"agent_id":        "backend-agent-123",
		"agent_type":      "general-purpose",
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
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("non-Tiller Codex SubagentStart should pass through silently, got %s", got)
	}
}

func TestCodexLifecycleSessionStartAppendsLedger(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscript(t, "xhigh")
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": p,
		"model":           "gpt-5.5",
		"usage":           map[string]any{"output_tokens": 321},
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := RunWithBackend(strings.NewReader(string(data)), &out, workspace, "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes())); !strings.Contains(ctx, "Tiller ambient is active") {
		t.Fatalf("missing Codex SessionStart context: %s", ctx)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ledger event count=%d want 1: %#v", len(events), events)
	}
	if events[0].Kind != "codex.session_start" || events[0].Backend != "codex" {
		t.Fatalf("unexpected ledger event: %#v", events[0])
	}
	if events[0].TokenUsage == nil || events[0].TokenUsage.OutputTokens != 321 {
		t.Fatalf("ledger token usage mismatch: %#v", events[0].TokenUsage)
	}
	status, err := os.ReadFile(filepath.Join(runDir, "status.md"))
	if err != nil {
		t.Fatalf("read status.md: %v", err)
	}
	if got := string(status); !strings.Contains(got, "Generated snapshot from `manifest.json`") || !strings.Contains(got, "codex.session_start") || !strings.Contains(got, "- ledger: input=0 output=321") {
		t.Fatalf("status.md missing Codex lifecycle content:\n%s", got)
	}
}

func TestCodexLifecycleSessionStartMediumDoesNotAppendLedger(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscript(t, "medium")
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": p,
		"model":           "gpt-5.5",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := RunWithBackend(strings.NewReader(string(data)), &out, workspace, "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("medium Codex SessionStart should be silent, got %s", got)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ledger event count=%d want 0: %#v", len(events), events)
	}
}

func TestCodexLifecycleSubagentStartCreatesAgentRunWhenIdentityPresent(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	event := map[string]any{
		"hook_event_name": "SubagentStart",
		"agent_id":        "backend-agent-123",
		"agent_type":      "tiller-worker",
		"model":           "gpt-5.5",
		"token_usage":     map[string]any{"input_tokens": 111, "output_tokens": 222},
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := RunWithBackend(strings.NewReader(string(data)), &out, workspace, "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes())); !strings.Contains(ctx, "Tiller execution agent") {
		t.Fatalf("missing Codex SubagentStart context: %s", ctx)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ledger event count=%d want 1: %#v", len(events), events)
	}
	if events[0].Kind != "codex.subagent_start" || events[0].AgentRunID == "" {
		t.Fatalf("unexpected ledger event: %#v", events[0])
	}
	if events[0].TokenUsage == nil || events[0].TokenUsage.OutputTokens != 222 {
		t.Fatalf("ledger token usage mismatch: %#v", events[0].TokenUsage)
	}
	agents, err := st.ListAgentRuns(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListAgentRuns: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agent run count=%d want 1: %#v", len(agents), agents)
	}
	if agents[0].BackendAgentID != "backend-agent-123" || agents[0].Role != "worker" || agents[0].Tier != "execute" || agents[0].Status != scratch.AgentRunStatusRunning {
		t.Fatalf("unexpected agent run: %#v", agents[0])
	}
	if agents[0].TokenUsage == nil || agents[0].TokenUsage.InputTokens != 111 || agents[0].TokenUsage.OutputTokens != 222 {
		t.Fatalf("agent token usage mismatch: %#v", agents[0].TokenUsage)
	}
}

func TestCodexLifecycleRootMultiAgentToolAppendsLedgerAndStaysSilent(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscriptWithUsage(t, "xhigh", 654)
	out := runCodexAmbientHook(t, p, "functions.spawn_agent", map[string]any{
		"agent_type": "tiller-worker",
	})
	if len(out) != 0 {
		t.Fatalf("Codex allowed lifecycle tool should stay silent, got %s", out)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ledger event count=%d want 2: %#v", len(events), events)
	}
	lifecycle := findLedgerEventKind(events, "codex.lifecycle_tool")
	if lifecycle == nil || lifecycle.Status != scratch.AgentRunStatusRequested {
		t.Fatalf("missing lifecycle ledger event: %#v", events)
	}
	if lifecycle.TokenUsage == nil || lifecycle.TokenUsage.OutputTokens != 654 {
		t.Fatalf("ledger token usage mismatch: %#v", lifecycle.TokenUsage)
	}
	if !strings.Contains(lifecycle.Summary, "functions.spawn_agent") {
		t.Fatalf("ledger summary missing tool name: %#v", lifecycle)
	}
	descriptor := findLedgerEventKind(events, "ambient.task_descriptor")
	if descriptor == nil || descriptor.Status != scratch.AgentRunStatusRequested {
		t.Fatalf("missing descriptor ledger event: %#v", events)
	}
}

func TestCodexLifecycleRootMultiAgentToolMediumDoesNotAppendLedger(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscript(t, "medium")
	out := runCodexAmbientHook(t, p, "functions.spawn_agent", map[string]any{
		"agent_type": "tiller-worker",
	})
	if len(out) != 0 {
		t.Fatalf("Codex medium lifecycle tool should stay silent, got %s", out)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ledger event count=%d want 0: %#v", len(events), events)
	}
}

func TestCodexAmbientSpawnAgentAppendsTaskDescriptorAndStatus(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.spawn_agent", map[string]any{
		"agent_type": "tiller-worker",
		"prompt":     "Implement descriptor-backed task list.\nKeep it scoped.",
	})
	if len(out) != 0 {
		t.Fatalf("Codex allowed spawn_agent should stay silent, got %s", out)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	descriptor := findLedgerEventKind(events, "ambient.task_descriptor")
	if descriptor == nil {
		t.Fatalf("descriptor ledger event missing: %#v", events)
	}
	if descriptor.Backend != "codex" || descriptor.Status != scratch.AgentRunStatusRequested {
		t.Fatalf("unexpected descriptor event: %#v", descriptor)
	}
	for _, want := range []string{"tiller-worker: Implement descriptor-backed task list.", "tool:spawn_agent", "agent_type:tiller-worker", "descriptor_id:", "objective_hash:"} {
		if !ledgerEventContains(*descriptor, want) {
			t.Fatalf("descriptor missing %q: %#v", want, descriptor)
		}
	}
	status, err := os.ReadFile(filepath.Join(runDir, "status.md"))
	if err != nil {
		t.Fatalf("read status.md: %v", err)
	}
	got := string(status)
	for _, want := range []string{"## Task Descriptors", "- total: 1", "- by_status: requested=1", "tiller-worker: Implement descriptor-backed task list.", "refs: tool:spawn_agent, agent_type:tiller-worker"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status.md missing %q:\n%s", want, got)
		}
	}
}

func TestClaudeAmbientTaskAppendsTaskDescriptor(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_ROLE", "")
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := fableTranscript(t)
	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Task",
		"tool_input": map[string]any{
			"subagent_type": "tiller-worker",
			"description":   "Patch the renderer",
			"prompt":        "Patch the renderer and run tests.",
		},
		"transcript_path": p,
		"agent_id":        "",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := Run(strings.NewReader(string(data)), &out, workspace); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if decision := parseAmbientDecision(t, bytes.TrimSpace(out.Bytes())); decision != "allow" {
		t.Fatalf("expected allow, got %q", decision)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	descriptor := findLedgerEventKind(events, "ambient.task_descriptor")
	if descriptor == nil {
		t.Fatalf("descriptor ledger event missing: %#v", events)
	}
	if descriptor.Backend != "claude-code" || descriptor.Status != scratch.AgentRunStatusRequested {
		t.Fatalf("unexpected descriptor event: %#v", descriptor)
	}
	for _, want := range []string{"tiller-worker: Patch the renderer", "tool:Task", "agent_type:tiller-worker", "descriptor_id:", "objective_hash:"} {
		if !ledgerEventContains(*descriptor, want) {
			t.Fatalf("descriptor missing %q: %#v", want, descriptor)
		}
	}
}

func TestCodexAmbientMediumSpawnAgentDoesNotAppendTaskDescriptor(t *testing.T) {
	workspace := t.TempDir()
	runDir := makeAmbientLifecycleRunDir(t, workspace)
	t.Setenv("TILLER_RUN_DIR", runDir)

	p := codexTranscript(t, "medium")
	out := runCodexAmbientHook(t, p, "functions.spawn_agent", map[string]any{
		"agent_type": "tiller-worker",
		"prompt":     "Do work.",
	})
	if len(out) != 0 {
		t.Fatalf("Codex medium spawn_agent should pass through silently, got %s", out)
	}

	st := fsstore.Open(filepath.Dir(runDir))
	events, err := st.ListLedgerEvents(filepath.Base(runDir))
	if err != nil {
		t.Fatalf("ListLedgerEvents: %v", err)
	}
	if findLedgerEventKind(events, "ambient.task_descriptor") != nil {
		t.Fatalf("descriptor should not be appended for medium Codex: %#v", events)
	}
}

func findLedgerEventKind(events []scratch.LedgerEvent, kind string) *scratch.LedgerEvent {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

func ledgerEventContains(ev scratch.LedgerEvent, value string) bool {
	if strings.Contains(ev.Summary, value) {
		return true
	}
	for _, ref := range ev.Refs {
		if strings.Contains(ref, value) {
			return true
		}
	}
	return false
}

func TestCodexLifecycleWriteFailureDoesNotBlockContext(t *testing.T) {
	t.Setenv("TILLER_ROLE", "")
	runFile := filepath.Join(t.TempDir(), "not-a-run-dir")
	if err := os.WriteFile(runFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write run file fixture: %v", err)
	}
	t.Setenv("TILLER_RUN_DIR", runFile)

	event := map[string]any{
		"hook_event_name": "SubagentStart",
		"agent_id":        "backend-agent-123",
		"agent_type":      "tiller-worker",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var out bytes.Buffer
	if err := RunWithBackend(strings.NewReader(string(data)), &out, "", "codex"); err != nil {
		t.Fatalf("RunWithBackend error: %v", err)
	}
	if ctx := parseAdditionalContext(t, bytes.TrimSpace(out.Bytes())); !strings.Contains(ctx, "Tiller execution agent") {
		t.Fatalf("write failure altered Codex context output: %s", ctx)
	}
}

func TestAmbientDisabledMarkerPassthrough(t *testing.T) {
	p := fableTranscript(t)
	dir := t.TempDir()
	if _, _, err := ambientgate.Disable(dir); err != nil {
		t.Fatalf("disable ambient: %v", err)
	}

	event := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": filepath.Join(dir, "main.go")},
		"transcript_path": p,
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
	if err := Run(strings.NewReader(string(data)), &out, dir); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := bytes.TrimSpace(out.Bytes()); len(got) != 0 {
		t.Fatalf("ambient disabled should pass through silently, got %s", got)
	}
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

func TestAllowCodexExec_MediumExecution(t *testing.T) {
	p := fableTranscript(t)
	cmd := `codex exec -m gpt-5.5 -c model_reasoning_effort=medium "make the requested bounded edit"`
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "allow" {
		t.Errorf("expected allow for constrained medium Codex exec, got %q", decision)
	}
}

func TestAllowCodexExec_XHighReadOnlyReview(t *testing.T) {
	p := fableTranscript(t)
	cmd := `codex exec --model gpt-5.5 --config model_reasoning_effort=xhigh --sandbox read-only --output-last-message .tiller/reports/review.md "review the current diff"`
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "allow" {
		t.Errorf("expected allow for constrained xhigh read-only Codex exec, got %q", decision)
	}
}

func TestDenyCodexExec_XHighWithoutReadOnly(t *testing.T) {
	p := fableTranscript(t)
	cmd := `codex exec --model gpt-5.5 --config model_reasoning_effort=xhigh "review the current diff"`
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": cmd})
	if decision != "deny" {
		t.Errorf("expected deny for xhigh Codex exec without read-only sandbox, got %q", decision)
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
	for range proseLines {
		sb.WriteString("This is a prose line.\n")
	}
	if fencedLines > 0 {
		sb.WriteString("```go\n")
		for range fencedLines {
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

// ─── Gap 1: orchestration tools ─────────────────────────────────────────────

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

func TestCodexNamespacedSpawnAgentAllowed(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.spawn_agent", map[string]any{
		"agent": "tiller-worker",
	})
	if len(out) != 0 {
		t.Fatalf("Codex namespaced spawn_agent allow should produce no stdout, got %s", out)
	}
}

func TestCodexNamespacedLifecycleAllowed(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.update_plan", map[string]any{
		"plan": []map[string]any{{"step": "dispatch work", "status": "in_progress"}},
	})
	if len(out) != 0 {
		t.Fatalf("Codex namespaced update_plan allow should produce no stdout, got %s", out)
	}
}

func TestCodexNamespacedExecCommandDenied(t *testing.T) {
	p := codexTranscript(t, "xhigh")
	out := runCodexAmbientHook(t, p, "functions.exec_command", map[string]any{
		"cmd": "go test ./...",
	})
	if len(out) == 0 {
		t.Fatal("expected Codex deny output for root functions.exec_command in xhigh session")
	}
	if decision := parseAmbientDecision(t, out); decision != "deny" {
		t.Fatalf("expected deny, got %q", decision)
	}
	reason := parseDecisionReason(t, out)
	for _, want := range []string{"DenyExecution", "root can read/search directly", "spawn_agent", "agent_type=\"tiller-worker\"", "wait_agent/close_agent"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("Codex deny reason missing %q:\n%s", want, reason)
		}
	}
	if strings.Contains(reason, "Task tool") {
		t.Fatalf("Codex deny reason must not mention Claude Task tool:\n%s", reason)
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

// TestSelfUninstall_AllowBackendProject: project-scoped backend uninstall is
// allowed so a gated session can remove the matching project install.
func TestSelfUninstall_AllowBackendProject(t *testing.T) {
	p := fableTranscript(t)
	decision := runAmbientHookFull(t, p, "Bash", map[string]any{"command": "tiller uninstall --backend codex --project"})
	if decision != "allow" {
		t.Errorf("expected allow for 'tiller uninstall --backend codex --project', got %q", decision)
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
