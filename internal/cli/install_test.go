package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/tier"
)

func TestMergeHookEntries_FreshSettings(t *testing.T) {
	settings := map[string]any{}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
	}
	added := mergeHookEntries(settings, entry)
	if len(added) != 2 {
		t.Fatalf("expected 2 additions, got %d: %v", len(added), added)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list := hooks[ev].([]any)
		if len(list) != 1 {
			t.Errorf("%s: expected 1 entry, got %d", ev, len(list))
		}
	}
}

func TestMergeHookEntries_Idempotent(t *testing.T) {
	settings := map[string]any{}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
	}
	// First install.
	added1 := mergeHookEntries(settings, entry)
	if len(added1) != 2 {
		t.Fatalf("first install: expected 2, got %d", len(added1))
	}
	// Second install — must be idempotent.
	added2 := mergeHookEntries(settings, entry)
	if len(added2) != 0 {
		t.Errorf("second install: expected 0 new additions, got %d: %v", len(added2), added2)
	}
}

func TestMergeHookEntries_PreservesExistingHooks(t *testing.T) {
	// Pre-populate with an existing hook entry.
	existing := map[string]any{
		"command": "some-other-tool",
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": ".*",
					"hooks":   []any{existing},
				},
			},
		},
	}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
	}
	added := mergeHookEntries(settings, entry)
	// PreToolUse already has one entry; tiller adds another.
	// PostToolUse is empty; tiller adds one.
	if len(added) != 2 {
		t.Fatalf("expected 2 additions, got %d: %v", len(added), added)
	}
	hooks := settings["hooks"].(map[string]any)
	preList := hooks["PreToolUse"].([]any)
	if len(preList) != 2 {
		t.Errorf("PreToolUse: expected 2 entries (existing + tiller), got %d", len(preList))
	}
}

func TestRemoveHookEntries_RemovesTiller(t *testing.T) {
	cmd := "/usr/local/bin/tiller hook"
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": cmd},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": cmd},
					},
				},
			},
		},
	}
	removed := removeHookEntries(settings)
	if len(removed) != 2 {
		t.Fatalf("expected 2 removals, got %d: %v", len(removed), removed)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]any)
		if len(list) != 0 {
			t.Errorf("%s: expected empty list after uninstall, got %d entries", ev, len(list))
		}
	}
}

func TestRemoveHookEntries_PreservesOtherHooks(t *testing.T) {
	cmd := "/usr/local/bin/tiller hook"
	other := map[string]any{
		"matcher": ".*",
		"hooks":   []any{map[string]any{"type": "command", "command": "other-tool"}},
	}
	fb := map[string]any{
		"matcher": ".*",
		"hooks":   []any{map[string]any{"type": "command", "command": cmd}},
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{other, fb},
		},
	}
	removed := removeHookEntries(settings)
	if len(removed) != 1 {
		t.Fatalf("expected 1 removal, got %d", len(removed))
	}
	hooks := settings["hooks"].(map[string]any)
	preList := hooks["PreToolUse"].([]any)
	if len(preList) != 1 {
		t.Errorf("PreToolUse: expected 1 entry remaining (other-tool), got %d", len(preList))
	}
}

func TestRemoveHookEntries_NothingToRemove(t *testing.T) {
	settings := map[string]any{}
	removed := removeHookEntries(settings)
	if len(removed) != 0 {
		t.Errorf("expected 0 removals from empty settings, got %d", len(removed))
	}
}

func TestHookCommandMatches_BackendArgs(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"/usr/local/bin/tiller hook", true},
		{"/usr/local/bin/tiller hook --backend codex", true},
		{"tiller hook --backend claude-code", true},
		{"other hook --backend codex", false},
		{"tiller install", false},
		{"tiller-hook", false},
	}
	for _, tc := range cases {
		if got := hookCommandMatches(tc.cmd); got != tc.want {
			t.Errorf("hookCommandMatches(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestRunInstallWizard_DefaultsToClaudeProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	var out strings.Builder
	if err := runInstallWizard(strings.NewReader("\n"), &out, "/usr/local/bin/tiller"); err != nil {
		t.Fatalf("runInstallWizard: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".claude", "settings.json")); err != nil {
		t.Fatalf("Claude Code settings not installed project-locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".claude", "agents", "tiller-worker.md")); err != nil {
		t.Fatalf("Claude Code worker agent not installed project-locally: %v", err)
	}
	if strings.Contains(out.String(), ".claude") {
		t.Fatalf("wizard prompt should stay concise and not print install output through its writer")
	}
}

func TestRunInstallWizard_Selection2InstallsCodexProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	var out strings.Builder
	if err := runInstallWizard(strings.NewReader("2\n"), &out, "/usr/local/bin/tiller"); err != nil {
		t.Fatalf("runInstallWizard: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".codex", "hooks.json")); err != nil {
		t.Fatalf("Codex hooks not installed project-locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".codex", "agents", "tiller-worker.toml")); err != nil {
		t.Fatalf("Codex worker agent not installed project-locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".codex", "config.toml")); err != nil {
		t.Fatalf("Codex config not installed project-locally: %v", err)
	}
	skill, err := os.ReadFile(filepath.Join(projectDir, ".codex", "skills", "using-tiller", "SKILL.md"))
	if err != nil {
		t.Fatalf("Codex Tiller skill not installed project-locally: %v", err)
	}
	if !strings.Contains(string(skill), "name: using-tiller") {
		t.Fatalf("Codex Tiller skill missing frontmatter:\n%s", string(skill))
	}
	sirenaSkill, err := os.ReadFile(filepath.Join(projectDir, ".codex", "skills", "using-sirena", "SKILL.md"))
	if err != nil {
		t.Fatalf("Codex Sirena skill not installed project-locally: %v", err)
	}
	if !strings.Contains(string(sirenaSkill), "name: using-sirena") {
		t.Fatalf("Codex Sirena skill missing frontmatter:\n%s", string(sirenaSkill))
	}
	notes, err := os.ReadFile(filepath.Join(projectDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("Codex operating notes not installed project-locally: %v", err)
	}
	for _, want := range []string{
		tillerCodexNotesBegin,
		"the root Codex session is the orchestrator",
		"`tiller-scout`",
		"`gpt-5.4-mini`",
		"`gpt-5.5 medium`",
		"`gpt-5.5 high`",
		"`gpt-5.5 xhigh`",
		"`tiller-worker`",
		"do not retry a variant",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("Codex operating notes missing %q:\n%s", want, string(notes))
		}
	}
	if strings.Contains(out.String(), ".codex") {
		t.Fatalf("wizard prompt should stay concise and not print install output through its writer")
	}
}

func TestRunInstallWizard_Selection3InstallsOpenCodeProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	var out strings.Builder
	if err := runInstallWizard(strings.NewReader("3\n"), &out, "/usr/local/bin/tiller"); err != nil {
		t.Fatalf("runInstallWizard: %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, "opencode.json")); err != nil {
		t.Fatalf("OpenCode config not installed project-locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode", "agents", "tiller-orchestrator.md")); err != nil {
		t.Fatalf("OpenCode orchestrator agent not installed project-locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode", "tiller.md")); err != nil {
		t.Fatalf("OpenCode operating notes not installed project-locally: %v", err)
	}
	if strings.Contains(out.String(), ".opencode") {
		t.Fatalf("wizard prompt should stay concise and not print install output through its writer")
	}
}

func TestMergeCodexConfigTextPreservesExistingSettings(t *testing.T) {
	in := `model = "gpt-5.5"

[features]
js_repl = true
multi_agent = false

[projects."/tmp/project"]
trust_level = "trusted"

[agents]
job_max_runtime_seconds = 900
max_threads = 4
`
	got := mergeCodexConfigText(in)
	for _, want := range []string{
		`model = "gpt-5.5"`,
		"js_repl = true",
		"multi_agent = true",
		`[projects."/tmp/project"]`,
		`trust_level = "trusted"`,
		"job_max_runtime_seconds = 900",
		"max_threads = 12",
		"max_depth = 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged Codex config missing %q:\n%s", want, got)
		}
	}
	if got2 := mergeCodexConfigText(got); got2 != got {
		t.Fatalf("mergeCodexConfigText should be idempotent:\nfirst:\n%s\nsecond:\n%s", got, got2)
	}
}

func TestMergeCodexOperatingNotesTextPreservesExistingInstructions(t *testing.T) {
	in := "# Existing Project Notes\n\nKeep this guidance.\n"
	got := mergeCodexOperatingNotesText(in)
	if !strings.Contains(got, "Keep this guidance.") {
		t.Fatalf("existing instructions should be preserved:\n%s", got)
	}
	if strings.Count(got, tillerCodexNotesBegin) != 1 {
		t.Fatalf("expected one Tiller section:\n%s", got)
	}

	replaced := mergeCodexOperatingNotesText(got)
	if strings.Count(replaced, tillerCodexNotesBegin) != 1 {
		t.Fatalf("expected managed section to be replaced, not duplicated:\n%s", replaced)
	}
}

func TestInstallCodexSkill(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
		want    []string
	}{
		{
			name:    "using-tiller",
			path:    filepath.Join(t.TempDir(), ".codex", "skills", "using-tiller", "SKILL.md"),
			content: codexSkillSnippet(),
			want:    []string{"name: using-tiller", "Root Workflow", "Right-Sizing Matrix", "hypha recall", "tiller-scout", "gpt-5.4-mini", "gpt-5.5 medium", "gpt-5.5 high", "gpt-5.5 xhigh", "tiller-worker", "using-sirena", "terse, direct, explicit", "configured checkpoint tool", "checkpoint candidate"},
		},
		{
			name:    "using-sirena",
			path:    filepath.Join(t.TempDir(), ".codex", "skills", "using-sirena", "SKILL.md"),
			content: codexSirenaSkillSnippet(),
			want:    []string{"name: using-sirena", "hypha recall sirena", "sirena bake", "Mermaid ingestion", "terse, direct, and explicit"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := installCodexSkill(tc.path, tc.content, false)
			if err != nil {
				t.Fatalf("installCodexSkill: %v", err)
			}
			if !changed {
				t.Fatal("first install should report changed")
			}
			data, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read skill: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(data), want) {
					t.Fatalf("skill missing %q:\n%s", want, string(data))
				}
			}
			managed, err := codexSkillHasManagedSnippet(tc.path, tc.content)
			if err != nil {
				t.Fatalf("codexSkillHasManagedSnippet: %v", err)
			}
			if !managed {
				t.Fatal("freshly installed skill should be recognized as managed")
			}

			changed, err = installCodexSkill(tc.path, tc.content, false)
			if err != nil {
				t.Fatalf("second installCodexSkill: %v", err)
			}
			if changed {
				t.Fatal("second install should be idempotent")
			}
		})
	}
}

func TestCodexSkillHasManagedSnippet(t *testing.T) {
	skillPath := filepath.Join(t.TempDir(), ".codex", "skills", "using-sirena", "SKILL.md")
	if _, err := installCodexSkill(skillPath, codexSirenaSkillSnippet(), false); err != nil {
		t.Fatalf("installCodexSkill: %v", err)
	}
	managed, err := codexSkillHasManagedSnippet(skillPath, codexSirenaSkillSnippet())
	if err != nil {
		t.Fatalf("codexSkillHasManagedSnippet: %v", err)
	}
	if !managed {
		t.Fatal("expected exact managed Sirena skill")
	}
	managed, err = codexSkillHasManagedSnippet(skillPath, codexSkillSnippet())
	if err != nil {
		t.Fatalf("codexSkillHasManagedSnippet with wrong content: %v", err)
	}
	if managed {
		t.Fatal("different managed skill content should not match")
	}
}

// TestInstallUninstallRoundtrip runs a full install+uninstall cycle using the
// lower-level merge/remove functions and a temp settings file, verifying the
// JSON file is written correctly and cleaned up.
func TestInstallUninstallRoundtrip(t *testing.T) {
	tmpHome := t.TempDir()
	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")

	cmd := "/usr/local/bin/tiller hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: cmd}},
	}

	// Initial install via merge + write.
	settings1 := map[string]any{}
	added := mergeHookEntries(settings1, entry)
	if len(added) != 2 {
		t.Fatalf("first install: expected 2 additions, got %d", len(added))
	}
	if err := writeSettings(settingsPath, settings1); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("settings.json not valid JSON: %v", err)
	}
	hooks, _ := s["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]any)
		if len(list) == 0 {
			t.Errorf("after install: %s hooks empty", ev)
		}
	}

	// Idempotent re-install: load, merge again, should add nothing.
	settings2, _ := loadOrInitSettings(settingsPath)
	added2 := mergeHookEntries(settings2, entry)
	if len(added2) != 0 {
		t.Errorf("idempotent install: expected 0 additions, got %d: %v", len(added2), added2)
	}

	// Uninstall: load, remove, write.
	settings3, _ := loadOrInitSettings(settingsPath)
	removed := removeHookEntries(settings3)
	if len(removed) != 2 {
		t.Fatalf("uninstall: expected 2 removals, got %d: %v", len(removed), removed)
	}
	if err := writeSettings(settingsPath, settings3); err != nil {
		t.Fatalf("writeSettings after uninstall: %v", err)
	}
	data3, _ := os.ReadFile(settingsPath)
	var s3 map[string]any
	json.Unmarshal(data3, &s3)
	hooks3, _ := s3["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks3[ev].([]any)
		if len(list) != 0 {
			t.Errorf("after uninstall: %s hooks not empty (%d entries)", ev, len(list))
		}
	}
}

// TestInstallPreservesExistingKeys ensures install does not clobber other
// top-level keys in settings.json.
func TestInstallPreservesExistingKeys(t *testing.T) {
	tmpHome := t.TempDir()
	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settingsPath), 0o755)

	// Pre-populate with an existing key.
	initial := map[string]any{
		"theme": "dark",
		"model": "claude-opus",
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(settingsPath, append(data, '\n'), 0o644)

	// Load, merge, write — as install does.
	settings, err := loadOrInitSettings(settingsPath)
	if err != nil {
		t.Fatalf("loadOrInitSettings: %v", err)
	}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
	}
	mergeHookEntries(settings, entry)
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	after, _ := os.ReadFile(settingsPath)
	var s map[string]any
	json.Unmarshal(after, &s)

	if s["theme"] != "dark" {
		t.Errorf("theme key clobbered (got %v)", s["theme"])
	}
	if s["model"] != "claude-opus" {
		t.Errorf("model key clobbered (got %v)", s["model"])
	}
	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks missing after install")
	}
}

// ── Agent file tests ──────────────────────────────────────────────────────────

// TestInstallAgents_FreshDir verifies that installAgents writes all 6 tiller-* files
// into an empty directory.
func TestInstallAgents_FreshDir(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	written, err := installAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	const wantCount = 6
	if len(written) != wantCount {
		t.Fatalf("expected %d files written, got %d: %v", wantCount, len(written), written)
	}
	// Verify all written files are tiller-*.md and exist on disk.
	for _, name := range written {
		if !strings.HasPrefix(name, "tiller-") || !strings.HasSuffix(name, ".md") {
			t.Errorf("unexpected filename %q", name)
		}
		p := filepath.Join(agentsDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file not found on disk: %s: %v", p, err)
		}
	}
}

// TestInstallAgents_Idempotent verifies that re-running installAgents when files
// are already identical returns zero written files.
func TestInstallAgents_Idempotent(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	// First install.
	written1, err := installAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("first installAgents: %v", err)
	}
	if len(written1) == 0 {
		t.Fatal("first install should have written files")
	}
	// Second install — files are identical, nothing should be written.
	written2, err := installAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("second installAgents: %v", err)
	}
	if len(written2) != 0 {
		t.Errorf("idempotent install: expected 0 writes, got %d: %v", len(written2), written2)
	}
}

// TestInstallAgents_ContentCheck verifies tiller-worker.md exists with model: sonnet.
func TestInstallAgents_ContentCheck(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(agentsDir, "tiller-worker.md"))
	if err != nil {
		t.Fatalf("tiller-worker.md not found: %v", err)
	}
	if !strings.Contains(string(data), "model: sonnet") {
		t.Errorf("tiller-worker.md missing 'model: sonnet'; content:\n%s", string(data))
	}
}

func TestInstallAgents_RenderConfiguredModels(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	ambient := &tier.AmbientConfig{
		Models: map[string][]string{
			"reason":  {"5.5 xhigh"},
			"execute": {"5.5 medium"},
		},
	}
	if _, err := installAgentsWithConfig(agentsDir, false, ambient); err != nil {
		t.Fatalf("installAgentsWithConfig: %v", err)
	}

	cases := map[string]string{
		"tiller-worker.md":       "model: 5.5 medium",
		"tiller-debugger.md":     "model: 5.5 medium",
		"tiller-investigator.md": "model: 5.5 xhigh",
		"tiller-reviewer.md":     "model: 5.5 xhigh",
		"tiller-architect.md":    "model: 5.5 xhigh",
		"tiller-deep-report.md":  "model: 5.5 xhigh",
	}
	for name, want := range cases {
		data, err := os.ReadFile(filepath.Join(agentsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s missing %q; content:\n%s", name, want, string(data))
		}
	}
}

func TestInstallAgents_RenderScrutinyWhenConfigured(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	ambient := &tier.AmbientConfig{
		Models: map[string][]string{
			"reason":   {"fable"},
			"scrutiny": {"opus"},
			"execute":  {"sonnet"},
		},
	}
	if _, err := installAgentsWithConfig(agentsDir, false, ambient); err != nil {
		t.Fatalf("installAgentsWithConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(agentsDir, "tiller-reviewer.md"))
	if err != nil {
		t.Fatalf("read reviewer: %v", err)
	}
	if !strings.Contains(string(data), "model: opus") {
		t.Errorf("reviewer should use scrutiny model when configured; content:\n%s", string(data))
	}
}

func TestInstallCodexAgents_FreshDir(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	written, err := installCodexAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("installCodexAgents: %v", err)
	}
	if len(written) != 7 {
		t.Fatalf("expected 7 Codex agents written, got %d: %v", len(written), written)
	}

	data, err := os.ReadFile(filepath.Join(agentsDir, "tiller-worker.toml"))
	if err != nil {
		t.Fatalf("read tiller-worker.toml: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `model = "gpt-5.5"`) {
		t.Errorf("Codex worker missing model setting:\n%s", content)
	}
	if !strings.Contains(content, `model_reasoning_effort = "medium"`) {
		t.Errorf("Codex worker missing medium execution effort:\n%s", content)
	}

	data, err = os.ReadFile(filepath.Join(agentsDir, "tiller-scout.toml"))
	if err != nil {
		t.Fatalf("read tiller-scout.toml: %v", err)
	}
	content = string(data)
	for _, want := range []string{`model = "gpt-5.4-mini"`, `model_reasoning_effort = "medium"`, `sandbox_mode = "read-only"`, "bounded read-only inventories"} {
		if !strings.Contains(content, want) {
			t.Errorf("Codex scout missing %q:\n%s", want, content)
		}
	}
}

func TestInstallCodexAgents_Idempotent(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	written1, err := installCodexAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("first installCodexAgents: %v", err)
	}
	if len(written1) == 0 {
		t.Fatal("first Codex install should write files")
	}
	written2, err := installCodexAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("second installCodexAgents: %v", err)
	}
	if len(written2) != 0 {
		t.Fatalf("idempotent Codex install wrote files: %v", written2)
	}
}

func TestInstallOpenCodeAgents_FreshDir(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	written, err := installOpenCodeAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("installOpenCodeAgents: %v", err)
	}
	if len(written) != 8 {
		t.Fatalf("expected 8 OpenCode agents written, got %d: %v", len(written), written)
	}

	data, err := os.ReadFile(filepath.Join(agentsDir, "tiller-orchestrator.md"))
	if err != nil {
		t.Fatalf("read tiller-orchestrator.md: %v", err)
	}
	content := string(data)
	for _, want := range []string{"mode: primary", "edit: deny", "task:", "tiller-*", ".tiller/scratch/opencode/", "checkpoint tool"} {
		if !strings.Contains(content, want) {
			t.Errorf("OpenCode orchestrator missing %q:\n%s", want, content)
		}
	}

	data, err = os.ReadFile(filepath.Join(agentsDir, "tiller-worker.md"))
	if err != nil {
		t.Fatalf("read tiller-worker.md: %v", err)
	}
	content = string(data)
	for _, want := range []string{"mode: subagent", "edit: allow", "bash: allow", "checkpoint candidate"} {
		if !strings.Contains(content, want) {
			t.Errorf("OpenCode worker missing %q:\n%s", want, content)
		}
	}
}

func TestInstallOpenCodeAgents_Idempotent(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	written1, err := installOpenCodeAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("first installOpenCodeAgents: %v", err)
	}
	if len(written1) == 0 {
		t.Fatal("first OpenCode install should write files")
	}
	written2, err := installOpenCodeAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("second installOpenCodeAgents: %v", err)
	}
	if len(written2) != 0 {
		t.Fatalf("idempotent OpenCode install wrote files: %v", written2)
	}
}

func TestInstallOpenCodeConfigMergesInstructions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{"instructions":["README.md"],"share":"disabled"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	changed, err := installOpenCodeConfig(configPath, ".opencode/tiller.md", false)
	if err != nil {
		t.Fatalf("installOpenCodeConfig: %v", err)
	}
	if !changed {
		t.Fatal("first config merge should report changed")
	}
	changed, err = installOpenCodeConfig(configPath, ".opencode/tiller.md", false)
	if err != nil {
		t.Fatalf("second installOpenCodeConfig: %v", err)
	}
	if changed {
		t.Fatal("second config merge should be idempotent")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	instructions := config["instructions"].([]any)
	for _, want := range []string{"README.md", ".opencode/tiller.md"} {
		found := false
		for _, raw := range instructions {
			if raw == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("instructions missing %q: %v", want, instructions)
		}
	}
	if config["share"] != "disabled" {
		t.Fatalf("existing config key not preserved: %v", config)
	}
}

// TestUninstallAgents_RemovesOnlyFbFiles verifies that uninstall removes tiller-*.md
// files but leaves other files untouched.
func TestUninstallAgents_RemovesOnlyFbFiles(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Install tiller-* agents.
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}

	// Place a non-fb file alongside them.
	otherPath := filepath.Join(agentsDir, "my-custom-agent.md")
	if err := os.WriteFile(otherPath, []byte("# custom"), 0o644); err != nil {
		t.Fatalf("write other agent: %v", err)
	}

	// Uninstall: tillerAgentFilesIn + remove.
	removed := tillerAgentFilesIn(agentsDir)
	if len(removed) == 0 {
		t.Fatal("expected tiller-* files to be found for removal")
	}
	for _, name := range removed {
		p := filepath.Join(agentsDir, name)
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", p, err)
		}
	}

	// Other agent must still be present.
	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("non-fb agent was unexpectedly removed: %v", err)
	}
	// No tiller-* files should remain.
	remaining := tillerAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("tiller-* files still present after uninstall: %v", remaining)
	}
}

func TestRunInstallCodexProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	command := "/usr/local/bin/tiller hook --backend codex"
	if err := runInstallCodex(false, true, command); err != nil {
		t.Fatalf("runInstallCodex: %v", err)
	}

	hooksPath := filepath.Join(projectDir, ".codex", "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; ok {
		t.Fatal("Codex install must not add PostToolUse")
	}
	for _, eventName := range codexManagedHookEvents() {
		list := hooks[eventName].([]any)
		if len(list) != 1 {
			t.Fatalf("expected one %s entry, got %d", eventName, len(list))
		}
		entry := list[0].(map[string]any)
		hookList := entry["hooks"].([]any)
		cmd := hookList[0].(map[string]any)
		if cmd["command"] != command {
			t.Fatalf("%s command = %v, want %s", eventName, cmd["command"], command)
		}
		if cmd["timeout"] != float64(30) {
			t.Fatalf("%s timeout = %v, want 30", eventName, cmd["timeout"])
		}
		if cmd["statusMessage"] == "" {
			t.Fatalf("%s Codex hook should include statusMessage", eventName)
		}
	}

	worker := filepath.Join(projectDir, ".codex", "agents", "tiller-worker.toml")
	if _, err := os.Stat(worker); err != nil {
		t.Fatalf("Codex worker agent not installed: %v", err)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	config := string(configData)
	for _, want := range []string{"multi_agent = true", "max_threads = 12", "max_depth = 2"} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}

	notesData, err := os.ReadFile(filepath.Join(projectDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	notes := string(notesData)
	for _, want := range []string{tillerCodexNotesBegin, "SessionStart adds this context", ".tiller/scratch/codex/", "Git/GitHub for VCS", "Graft", "Checkpoint verified wins", "configured checkpoint tool", "Do not run implementation shell commands", "Right-sizing matrix", "`tiller-scout`", "`gpt-5.4-mini`", "`gpt-5.5 medium`", "`gpt-5.5 high`", "`gpt-5.5 xhigh`", "agent_type", "wait_agent", "using-sirena", "terse, direct, explicit"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("Codex operating notes missing %q:\n%s", want, notes)
		}
	}

	skillData, err := os.ReadFile(filepath.Join(projectDir, ".codex", "skills", "using-tiller", "SKILL.md"))
	if err != nil {
		t.Fatalf("read Codex Tiller skill: %v", err)
	}
	skill := string(skillData)
	for _, want := range []string{"name: using-tiller", "SessionStart makes this visible", ".tiller/scratch/codex/", "Git/GitHub for VCS", "Graft", "Checkpoint verified wins", "configured checkpoint tool", "Root Workflow", "Right-Sizing Matrix", "hypha recall", "tiller-scout", "gpt-5.4-mini", "gpt-5.5 medium", "gpt-5.5 high", "gpt-5.5 xhigh", "tiller-worker", "wait_agent", "using-sirena", "terse, direct, explicit"} {
		if !strings.Contains(skill, want) {
			t.Fatalf("Codex Tiller skill missing %q:\n%s", want, skill)
		}
	}
	sirenaSkillData, err := os.ReadFile(filepath.Join(projectDir, ".codex", "skills", "using-sirena", "SKILL.md"))
	if err != nil {
		t.Fatalf("read Codex Sirena skill: %v", err)
	}
	sirenaSkill := string(sirenaSkillData)
	for _, want := range []string{"name: using-sirena", "hypha recall sirena", "sirena bake", "Mermaid ingestion", "terse, direct, and explicit"} {
		if !strings.Contains(sirenaSkill, want) {
			t.Fatalf("Codex Sirena skill missing %q:\n%s", want, sirenaSkill)
		}
	}
}

func TestRunInstallOpenCodeProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := runInstallOpenCode(false, true); err != nil {
		t.Fatalf("runInstallOpenCode: %v", err)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("parse opencode.json: %v", err)
	}
	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != ".opencode/tiller.md" {
		t.Fatalf("OpenCode instructions = %v, want .opencode/tiller.md", instructions)
	}

	notesData, err := os.ReadFile(filepath.Join(projectDir, ".opencode", "tiller.md"))
	if err != nil {
		t.Fatalf("read OpenCode notes: %v", err)
	}
	notes := string(notesData)
	for _, want := range []string{tillerOpenCodeNotesBegin, "tiller-orchestrator", ".tiller/scratch/opencode/", "Git/GitHub for VCS", "Graft", "checkpoint tool", "Right-sizing matrix", "`tiller-worker`"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("OpenCode notes missing %q:\n%s", want, notes)
		}
	}

	orchestrator, err := os.ReadFile(filepath.Join(projectDir, ".opencode", "agents", "tiller-orchestrator.md"))
	if err != nil {
		t.Fatalf("read OpenCode orchestrator: %v", err)
	}
	for _, want := range []string{"mode: primary", "permission:", "task:", "tiller-*"} {
		if !strings.Contains(string(orchestrator), want) {
			t.Fatalf("OpenCode orchestrator missing %q:\n%s", want, string(orchestrator))
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode", "agents", "tiller-worker.md")); err != nil {
		t.Fatalf("OpenCode worker agent not installed: %v", err)
	}
}

func TestRunUninstallCodexProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	hooksPath := filepath.Join(projectDir, ".codex", "hooks.json")
	agentsDir := filepath.Join(projectDir, ".codex", "agents")
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks: []settingsHookCommand{
			{Type: "command", Command: "/usr/local/bin/tiller hook --backend codex"},
		},
	}
	settings := map[string]any{}
	mergeHookEntriesForEvents(settings, entry, codexManagedHookEvents())
	if err := writeSettings(hooksPath, settings); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	if _, err := installCodexAgents(agentsDir, false); err != nil {
		t.Fatalf("installCodexAgents: %v", err)
	}
	configPath := filepath.Join(projectDir, ".codex", "config.toml")
	if _, err := installCodexConfig(configPath, false); err != nil {
		t.Fatalf("installCodexConfig: %v", err)
	}
	tillerSkillPath := filepath.Join(projectDir, ".codex", "skills", "using-tiller", "SKILL.md")
	if _, err := installCodexSkill(tillerSkillPath, codexSkillSnippet(), false); err != nil {
		t.Fatalf("installCodexSkill using-tiller: %v", err)
	}
	sirenaSkillPath := filepath.Join(projectDir, ".codex", "skills", "using-sirena", "SKILL.md")
	if _, err := installCodexSkill(sirenaSkillPath, codexSirenaSkillSnippet(), false); err != nil {
		t.Fatalf("installCodexSkill using-sirena: %v", err)
	}
	customAgent := filepath.Join(agentsDir, "tiller-custom.toml")
	if err := os.WriteFile(customAgent, []byte("name = \"custom\"\n"), 0o644); err != nil {
		t.Fatalf("write custom agent: %v", err)
	}

	if err := runUninstall([]string{"--backend", "codex", "--project"}); err != nil {
		t.Fatalf("runUninstall codex: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks after uninstall: %v", err)
	}
	var after map[string]any
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatalf("parse hooks after uninstall: %v", err)
	}
	if _, ok := after["hooks"]; ok {
		t.Fatal("hooks key must be pruned after Codex uninstall")
	}
	if _, err := os.Stat(filepath.Join(agentsDir, "tiller-worker.toml")); !os.IsNotExist(err) {
		t.Fatalf("owned Codex agent should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(customAgent); err != nil {
		t.Fatalf("custom Codex agent should be preserved: %v", err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after uninstall: %v", err)
	}
	config := string(configData)
	if strings.Contains(config, "max_threads = 12") || strings.Contains(config, "max_depth = 2") {
		t.Fatalf("managed Codex agent defaults should be removed:\n%s", config)
	}
	if !strings.Contains(config, "multi_agent = true") {
		t.Fatalf("Codex multi_agent feature flag should be preserved:\n%s", config)
	}
	if _, err := os.Stat(tillerSkillPath); !os.IsNotExist(err) {
		t.Fatalf("managed Codex Tiller skill should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(sirenaSkillPath); !os.IsNotExist(err) {
		t.Fatalf("managed Codex Sirena skill should be removed, stat err=%v", err)
	}
}

func TestRunUninstallOpenCodeProject(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := runInstallOpenCode(false, true); err != nil {
		t.Fatalf("runInstallOpenCode: %v", err)
	}
	agentsDir := filepath.Join(projectDir, ".opencode", "agents")
	customAgent := filepath.Join(agentsDir, "tiller-custom.md")
	if err := os.WriteFile(customAgent, []byte("---\ndescription: custom\n---\n"), 0o644); err != nil {
		t.Fatalf("write custom agent: %v", err)
	}

	if err := runUninstall([]string{"--backend", "opencode", "--project"}); err != nil {
		t.Fatalf("runUninstall opencode: %v", err)
	}

	if _, err := os.Stat(filepath.Join(agentsDir, "tiller-worker.md")); !os.IsNotExist(err) {
		t.Fatalf("owned OpenCode agent should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(customAgent); err != nil {
		t.Fatalf("custom OpenCode agent should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".opencode", "tiller.md")); !os.IsNotExist(err) {
		t.Fatalf("managed OpenCode notes should be removed, stat err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	if strings.Contains(string(data), ".opencode/tiller.md") {
		t.Fatalf("OpenCode instruction reference should be removed:\n%s", string(data))
	}
}

func TestRunUninstallCodexPreservesModifiedSkill(t *testing.T) {
	projectDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	skillPath := filepath.Join(projectDir, ".codex", "skills", "using-tiller", "SKILL.md")
	if _, err := installCodexSkill(skillPath, codexSkillSnippet(), false); err != nil {
		t.Fatalf("installCodexSkill: %v", err)
	}
	custom := codexSkillSnippet() + "\n<!-- local edits -->\n"
	if err := os.WriteFile(skillPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("customize skill: %v", err)
	}

	if err := runUninstall([]string{"--backend", "codex", "--project"}); err != nil {
		t.Fatalf("runUninstall codex: %v", err)
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("modified skill should be preserved: %v", err)
	}
	if string(data) != custom {
		t.Fatalf("modified skill content changed:\n%s", string(data))
	}
}

// TestFullInstallUninstall_WithAgents runs a complete install+uninstall cycle
// using a temp HOME, verifying hooks and agents are written and cleaned up.
func TestFullInstallUninstall_WithAgents(t *testing.T) {
	tmpHome := t.TempDir()
	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	agentsDir := filepath.Join(tmpHome, ".claude", "agents")

	// Install agents.
	written, err := installAgents(agentsDir, false)
	if err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	if len(written) != 6 {
		t.Fatalf("expected 6 agents written, got %d", len(written))
	}

	// Install hooks.
	cmd := "/tmp/tiller hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: cmd}},
	}
	settings := map[string]any{}
	added := mergeHookEntries(settings, entry)
	if len(added) != 2 {
		t.Fatalf("expected 2 hook additions, got %d", len(added))
	}
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	// Verify tiller-worker.md exists.
	workerPath := filepath.Join(agentsDir, "tiller-worker.md")
	if _, err := os.Stat(workerPath); err != nil {
		t.Fatalf("tiller-worker.md not found after install: %v", err)
	}

	// Verify settings has hooks.
	data, _ := os.ReadFile(settingsPath)
	var s map[string]any
	json.Unmarshal(data, &s)
	hooks, _ := s["hooks"].(map[string]any)
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]any)
		if len(list) == 0 {
			t.Errorf("after install: %s hooks empty", ev)
		}
	}

	// Uninstall hooks.
	settings2, _ := loadOrInitSettings(settingsPath)
	removedHooks := removeHookEntries(settings2)
	if len(removedHooks) != 2 {
		t.Fatalf("expected 2 hook removals, got %d", len(removedHooks))
	}
	if err := writeSettings(settingsPath, settings2); err != nil {
		t.Fatalf("writeSettings after uninstall: %v", err)
	}

	// Uninstall agents.
	agentFiles := tillerAgentFilesIn(agentsDir)
	if len(agentFiles) != 6 {
		t.Fatalf("expected 6 tiller-* files for removal, got %d", len(agentFiles))
	}
	for _, name := range agentFiles {
		os.Remove(filepath.Join(agentsDir, name))
	}

	// Verify all cleaned up.
	remaining := tillerAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("tiller-* files remain after uninstall: %v", remaining)
	}
}

// ── New uninstall hardening tests ─────────────────────────────────────────────

// TestPruneEmptyHookContainers_RemovesEmptyArrays verifies that after removing
// all hook entries, pruneEmptyHookContainers clears the empty arrays and the
// hooks map itself, leaving no husks in settings.json.
func TestPruneEmptyHookContainers_RemovesEmptyArrays(t *testing.T) {
	settings := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"PreToolUse":  []any{},
			"PostToolUse": []any{},
		},
	}
	pruneEmptyHookContainers(settings)
	if _, ok := settings["hooks"]; ok {
		t.Error("hooks key must be removed when all arrays are empty")
	}
	if settings["theme"] != "dark" {
		t.Error("theme key must be preserved")
	}
}

// TestPruneEmptyHookContainers_KeepsNonEmpty verifies that non-empty arrays
// survive pruning.
func TestPruneEmptyHookContainers_KeepsNonEmpty(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": ".*"},
			},
			"PostToolUse": []any{},
		},
	}
	pruneEmptyHookContainers(settings)
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks map must survive when PreToolUse still has entries")
	}
	if _, postOk := hooks["PostToolUse"]; postOk {
		t.Error("empty PostToolUse array must be pruned")
	}
	if _, preOk := hooks["PreToolUse"]; !preOk {
		t.Error("non-empty PreToolUse must remain")
	}
}

// TestOwnedTillerAgentFiles_OnlyOwned verifies that ownedTillerAgentFiles
// returns only the embedded tiller-*.md files and NOT user-created agents
// that happen to have a tiller- prefix but are not in the embedded set.
func TestOwnedTillerAgentFiles_OnlyOwned(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Install the six embedded tiller-* files.
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}

	// Place a user-created agent with tiller- prefix not in the embedded set.
	userAgent := filepath.Join(agentsDir, "tiller-my-custom.md")
	if err := os.WriteFile(userAgent, []byte("# custom"), 0o644); err != nil {
		t.Fatalf("write user agent: %v", err)
	}

	// Place a completely unrelated agent.
	otherAgent := filepath.Join(agentsDir, "my-agent.md")
	if err := os.WriteFile(otherAgent, []byte("# other"), 0o644); err != nil {
		t.Fatalf("write other agent: %v", err)
	}

	owned := ownedTillerAgentFiles(agentsDir)
	// Should be exactly 6.
	if len(owned) != 6 {
		t.Fatalf("expected 6 owned files, got %d: %v", len(owned), owned)
	}
	// Must not include user-created tiller-my-custom.md or my-agent.md.
	for _, name := range owned {
		if name == "tiller-my-custom.md" {
			t.Error("user-created tiller-my-custom.md must not be included in owned list")
		}
		if name == "my-agent.md" {
			t.Error("my-agent.md must not be included in owned list")
		}
	}
}

// TestRunUninstall_Idempotent verifies that running uninstall twice prints
// "nothing to uninstall" on the second call.
func TestRunUninstall_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	agentsDir := filepath.Join(tmpHome, ".claude", "agents")

	// Set up settings.json with a proper "tiller hook" command (not the test binary).
	tillerCmd := "/usr/local/bin/tiller hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: tillerCmd}},
	}
	settings := map[string]any{}
	mergeHookEntries(settings, entry)
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	// Install agents.
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}

	// First uninstall — should remove hooks and agents.
	if err := runUninstall([]string{}); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}

	// Second uninstall — nothing to uninstall.
	if err := runUninstall([]string{}); err != nil {
		t.Fatalf("second uninstall must not error (idempotent), got: %v", err)
	}

	// Settings.json must not have hooks or empty husks.
	if _, err := os.Stat(settingsPath); err == nil {
		data, _ := os.ReadFile(settingsPath)
		var s map[string]any
		if jsonErr := json.Unmarshal(data, &s); jsonErr == nil {
			if _, ok := s["hooks"]; ok {
				t.Error("hooks key must not remain after uninstall")
			}
		}
	}

	// No tiller-* files in agents dir.
	remaining := tillerAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("tiller-* files remain after uninstall: %v", remaining)
	}
}

// TestRunUninstall_PrintNoWrite verifies --print does not write any files.
func TestRunUninstall_PrintNoWrite(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	agentsDir := filepath.Join(tmpHome, ".claude", "agents")

	// Set up settings with a proper "tiller hook" command.
	tillerCmd := "/usr/local/bin/tiller hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: tillerCmd}},
	}
	settings := map[string]any{}
	mergeHookEntries(settings, entry)
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	// Install agents.
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}

	// Capture settings before --print.
	beforeData, _ := os.ReadFile(settingsPath)

	// Run uninstall --print.
	if err := runUninstall([]string{"--print"}); err != nil {
		t.Fatalf("uninstall --print: %v", err)
	}

	// Settings.json must be unchanged.
	afterData, _ := os.ReadFile(settingsPath)
	if string(beforeData) != string(afterData) {
		t.Error("--print must not modify settings.json")
	}

	// Agent files must still be present.
	owned := ownedTillerAgentFiles(agentsDir)
	if len(owned) != 6 {
		t.Errorf("--print must not remove agents; expected 6, got %d", len(owned))
	}
}

// TestRunUninstall_ForeignHookPreserved verifies that a foreign hook entry
// (non-tiller command) survives uninstall and no empty husks remain.
func TestRunUninstall_ForeignHookPreserved(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build settings with both a tiller hook and a foreign hook.
	tillerCmd := "/usr/local/bin/tiller hook"
	foreignCmd := "/usr/local/bin/block-hypha-serve.sh"
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": tillerCmd},
					},
				},
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": foreignCmd},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": tillerCmd},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// Uninstall.
	if err := runUninstall([]string{}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Read back settings.
	after, _ := os.ReadFile(settingsPath)
	var s map[string]any
	if err := json.Unmarshal(after, &s); err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	hooks, ok := s["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks map must remain (foreign hook present)")
	}

	// PreToolUse must have exactly 1 entry (foreign), no tiller entry.
	preList, _ := hooks["PreToolUse"].([]any)
	if len(preList) != 1 {
		t.Errorf("PreToolUse: expected 1 foreign entry, got %d", len(preList))
	}

	// PostToolUse must be absent (it was all-tiller → empty → pruned).
	if _, postOk := hooks["PostToolUse"]; postOk {
		t.Error("PostToolUse must be pruned (was all-tiller, now empty)")
	}

	// The foreign command must survive.
	if len(preList) > 0 {
		entry, _ := preList[0].(map[string]any)
		hooksCmds, _ := entry["hooks"].([]any)
		if len(hooksCmds) > 0 {
			h, _ := hooksCmds[0].(map[string]any)
			if h["command"] != foreignCmd {
				t.Errorf("foreign command changed: got %v, want %s", h["command"], foreignCmd)
			}
		}
	}
}

// TestRunUninstall_MissingSettings verifies that uninstall exits 0 gracefully
// when settings.json does not exist.
func TestRunUninstall_MissingSettings(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// Do not create settings.json or agents dir — they don't exist.
	if err := runUninstall([]string{}); err != nil {
		t.Fatalf("uninstall with missing settings must exit 0, got: %v", err)
	}
}

// TestRunUninstall_Project verifies --project flag uninstalls from ./.claude.
func TestRunUninstall_Project(t *testing.T) {
	tmpDir := t.TempDir()
	// Change CWD so claudePaths(project=true) resolves into tmpDir.
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) })

	// Install project-scope.
	if err := runInstall([]string{"--project"}); err != nil {
		t.Fatalf("install --project: %v", err)
	}

	// Uninstall project-scope.
	if err := runUninstall([]string{"--project"}); err != nil {
		t.Fatalf("uninstall --project: %v", err)
	}

	// No tiller agents remain.
	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	remaining := tillerAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("tiller-* files remain after --project uninstall: %v", remaining)
	}
}

// TestHookCommandMatches exercises hookCommandMatches, including the guard
// that previously panicked for commands shorter than 5 characters.
func TestHookCommandMatches(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Valid tiller hook entries.
		{"/usr/local/bin/tiller hook", true},
		{"/home/user/go/bin/tiller hook", true},
		{"tiller hook", true},
		// Suffix is present but binary base name is not "tiller".
		{"other hook", false},
		{"notiller hook", false},
		// 4-char command: must return false without panicking (regression).
		{"hook", false},
		// Shorter strings.
		{"", false},
		{"x", false},
		{"hook", false},
		// No " hook" suffix.
		{"/usr/local/bin/tiller", false},
		{"tiller", false},
		{"tiller hookx", false},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := hookCommandMatches(tc.cmd)
			if got != tc.want {
				t.Errorf("hookCommandMatches(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
