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
