package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/agents"
	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/hook"
)

func runCodex(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tiller codex doctor")
	}
	switch args[0] {
	case "doctor":
		return runCodexDoctor()
	default:
		return fmt.Errorf("unknown codex subcommand %q (usage: tiller codex doctor)", args[0])
	}
}

type codexDoctor struct {
	failures int
}

func runCodexDoctor() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	d := &codexDoctor{}
	d.checkHooks()
	d.checkConfig()
	d.checkAgents()
	d.checkSkills()
	d.checkAmbientBypass(cwd)
	d.checkHookSmoke(cwd)
	if d.failures > 0 {
		return fmt.Errorf("codex doctor found %d failing check(s)", d.failures)
	}
	return nil
}

func (d *codexDoctor) pass(format string, args ...any) {
	fmt.Printf("PASS "+format+"\n", args...)
}

func (d *codexDoctor) warn(format string, args ...any) {
	fmt.Printf("WARN "+format+"\n", args...)
}

func (d *codexDoctor) fail(format string, args ...any) {
	d.failures++
	fmt.Printf("FAIL "+format+"\n", args...)
}

func (d *codexDoctor) checkHooks() {
	hooksPath, _, err := codexPaths(true)
	if err != nil {
		d.fail("codex hooks: %v", err)
		return
	}
	settings, exists, err := readCodexDoctorJSON(hooksPath)
	if err != nil {
		d.fail("codex hooks: %v", err)
		return
	}
	if !exists {
		d.fail("codex hooks: missing %s", hooksPath)
		return
	}
	for _, event := range codexManagedHookEvents() {
		if !codexDoctorHasManagedHook(settings, event) {
			d.fail("codex hooks: missing managed %s entry in %s", event, hooksPath)
			continue
		}
		d.pass("codex hooks: managed %s entry present", event)
	}
}

func (d *codexDoctor) checkConfig() {
	configPath, err := codexConfigPath(true)
	if err != nil {
		d.fail("codex config: %v", err)
		return
	}
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		d.fail("codex config: missing %s", configPath)
		return
	}
	if err != nil {
		d.fail("codex config: read %s: %v", configPath, err)
		return
	}
	src := string(data)
	checks := []struct {
		section string
		key     string
		value   string
	}{
		{"features", "multi_agent", "true"},
		{"agents", "max_threads", "12"},
		{"agents", "max_depth", "2"},
	}
	for _, check := range checks {
		if !tomlHasKeyValue(src, check.section, check.key, check.value) {
			d.fail("codex config: missing [%s] %s = %s", check.section, check.key, check.value)
			continue
		}
		d.pass("codex config: [%s] %s = %s", check.section, check.key, check.value)
	}
}

func (d *codexDoctor) checkAgents() {
	_, agentsDir, err := codexPaths(true)
	if err != nil {
		d.fail("codex agents: %v", err)
		return
	}
	for _, name := range agents.CodexAgentFileNames() {
		path := filepath.Join(agentsDir, name)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				d.fail("codex agents: missing %s", path)
			} else {
				d.fail("codex agents: stat %s: %v", path, err)
			}
			continue
		}
		d.pass("codex agents: %s present", name)
	}
}

func (d *codexDoctor) checkSkills() {
	d.checkSkill(tillerCodexSkillPath, codexSkillSnippet(), "using-tiller")
	d.checkSkill(sirenaCodexSkillPath, codexSirenaSkillSnippet(), "using-sirena")
}

func (d *codexDoctor) checkSkill(relPath, managed, name string) {
	path, err := codexSkillPath(true, relPath)
	if err != nil {
		d.fail("codex skill %s: %v", name, err)
		return
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		d.fail("codex skill %s: missing %s", name, path)
		return
	}
	if err != nil {
		d.fail("codex skill %s: read %s: %v", name, path, err)
		return
	}
	if string(data) != managed {
		d.warn("codex skill %s: exists with local edits", name)
		return
	}
	d.pass("codex skill %s: managed snippet matches", name)
}

func (d *codexDoctor) checkAmbientBypass(cwd string) {
	if ambientgate.IsDisabled(cwd) {
		d.warn("ambient bypass: enabled by .tiller/ambient.disabled or TILLER_AMBIENT_DISABLED")
		return
	}
	d.pass("ambient bypass: not active")
}

func (d *codexDoctor) checkHookSmoke(cwd string) {
	transcript, cleanup, err := codexDoctorTranscript()
	if err != nil {
		d.fail("hook smoke: %v", err)
		return
	}
	defer cleanup()
	smokeWorkspace, cleanupWorkspace, err := codexDoctorSmokeWorkspace()
	if err != nil {
		d.fail("hook smoke: %v", err)
		return
	}
	defer cleanupWorkspace()
	oldDisabled, hadDisabled := os.LookupEnv("TILLER_AMBIENT_DISABLED")
	_ = os.Unsetenv("TILLER_AMBIENT_DISABLED")
	defer func() {
		if hadDisabled {
			_ = os.Setenv("TILLER_AMBIENT_DISABLED", oldDisabled)
		}
	}()

	sessionOut, err := codexDoctorRunHook(smokeWorkspace, map[string]any{
		"hook_event_name": "SessionStart",
		"transcript_path": transcript,
		"model":           "gpt-5.5",
		"effort":          "xhigh",
	})
	if err != nil {
		d.fail("hook smoke SessionStart: %v", err)
	} else if context := codexDoctorAdditionalContext(sessionOut); containsAll(context, []string{"tiller-scout", "tiller-summary", "gpt-5.4-mini", ".tiller/scratch/codex/", "configured checkpoint tool", "spawn_agent"}) {
		d.pass("hook smoke: SessionStart context")
	} else {
		d.fail("hook smoke SessionStart: missing expected context in %q", context)
	}

	subagentOut, err := codexDoctorRunHook(smokeWorkspace, map[string]any{
		"hook_event_name": "SubagentStart",
		"tool_input":      map[string]any{"agent_type": "tiller-scout"},
	})
	if err != nil {
		d.fail("hook smoke SubagentStart: %v", err)
	} else if context := codexDoctorAdditionalContext(subagentOut); containsAll(context, []string{"scout", "gpt-5.4-mini", "read-only"}) {
		d.pass("hook smoke: SubagentStart tiller-scout context")
	} else {
		d.fail("hook smoke SubagentStart: missing expected scout context in %q", context)
	}

	readOut, err := codexDoctorRunHook(smokeWorkspace, codexDoctorPreToolEvent(transcript, "Read", map[string]any{"file_path": filepath.Join(cwd, "README.md")}))
	if err != nil {
		d.fail("hook smoke Read: %v", err)
	} else if strings.TrimSpace(string(readOut)) == "" {
		d.pass("hook smoke: PreToolUse Read silent allow")
	} else {
		d.fail("hook smoke Read: expected silent allow, got %s", bytes.TrimSpace(readOut))
	}

	patchOut, err := codexDoctorRunHook(smokeWorkspace, codexDoctorPreToolEvent(transcript, "apply_patch", map[string]any{"cmd": "*** Begin Patch\n*** Update File: README.md\n@@\n-old\n+new\n*** End Patch\n"}))
	if err != nil {
		d.fail("hook smoke apply_patch: %v", err)
		return
	}
	reason := codexDoctorDecisionReason(patchOut)
	if decision := codexDoctorDecision(patchOut); decision != "deny" {
		d.fail("hook smoke apply_patch: expected deny, got %q", decision)
		return
	}
	if !containsAll(reason, []string{"spawn_agent", "agent_type=\"tiller-worker\""}) {
		d.fail("hook smoke apply_patch: deny reason missing Codex delegation guidance: %q", reason)
		return
	}
	if strings.Contains(reason, "Task tool") {
		d.fail("hook smoke apply_patch: deny reason contains legacy Task tool phrase")
		return
	}
	d.pass("hook smoke: PreToolUse apply_patch Codex deny guidance")
}

func codexDoctorSmokeWorkspace() (string, func(), error) {
	dir, err := os.MkdirTemp("", "tiller-codex-doctor-workspace-*")
	if err != nil {
		return "", nil, fmt.Errorf("mktemp workspace: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func readCodexDoctorJSON(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, true, nil
}

func codexDoctorHasManagedHook(settings map[string]any, event string) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	entries, ok := hooks[event].([]any)
	if !ok {
		return false
	}
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok || entry["matcher"] != ".*" {
			continue
		}
		cmds, ok := entry["hooks"].([]any)
		if !ok {
			continue
		}
		for _, rawCmd := range cmds {
			cmd, ok := rawCmd.(map[string]any)
			if !ok || cmd["type"] != "command" {
				continue
			}
			command, _ := cmd["command"].(string)
			if hookCommandMatches(command) && strings.Contains(command, "--backend codex") {
				return true
			}
		}
	}
	return false
}

func tomlHasKeyValue(src, section, key, value string) bool {
	lines := splitTomlLines(src)
	start, end := findTomlSection(lines, section)
	if start < 0 {
		return false
	}
	want := key + " = " + value
	for i := start + 1; i < end; i++ {
		if tomlLineKey(lines[i]) == key {
			return strings.TrimSpace(lines[i]) == want
		}
	}
	return false
}

func codexDoctorTranscript() (string, func(), error) {
	dir, err := os.MkdirTemp("", "tiller-codex-doctor-*")
	if err != nil {
		return "", nil, fmt.Errorf("mktemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "codex.jsonl")
	line := `{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"xhigh"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write transcript: %w", err)
	}
	return path, cleanup, nil
}

func codexDoctorPreToolEvent(transcript, toolName string, input map[string]any) map[string]any {
	return map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      input,
		"transcript_path": transcript,
		"agent_id":        "",
		"model":           "gpt-5.5",
	}
}

func codexDoctorRunHook(cwd string, event map[string]any) ([]byte, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal hook event: %w", err)
	}
	var out bytes.Buffer
	if err := hook.RunWithBackend(bytes.NewReader(data), &out, cwd, "codex"); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out.Bytes()), nil
}

func codexDoctorAdditionalContext(out []byte) string {
	var wrapper struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(out, &wrapper)
	return wrapper.HookSpecificOutput.AdditionalContext
}

func codexDoctorDecision(out []byte) string {
	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(out, &wrapper)
	return wrapper.HookSpecificOutput.PermissionDecision
}

func codexDoctorDecisionReason(out []byte) string {
	var wrapper struct {
		HookSpecificOutput struct {
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(out, &wrapper)
	return wrapper.HookSpecificOutput.PermissionDecisionReason
}

func containsAll(s string, wants []string) bool {
	for _, want := range wants {
		if !strings.Contains(s, want) {
			return false
		}
	}
	return true
}
