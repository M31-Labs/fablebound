package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeHookEntries_FreshSettings(t *testing.T) {
	settings := map[string]interface{}{}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/fablebound hook"}},
	}
	added := mergeHookEntries(settings, entry)
	if len(added) != 2 {
		t.Fatalf("expected 2 additions, got %d: %v", len(added), added)
	}
	hooks := settings["hooks"].(map[string]interface{})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list := hooks[ev].([]interface{})
		if len(list) != 1 {
			t.Errorf("%s: expected 1 entry, got %d", ev, len(list))
		}
	}
}

func TestMergeHookEntries_Idempotent(t *testing.T) {
	settings := map[string]interface{}{}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/fablebound hook"}},
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
	existing := map[string]interface{}{
		"command": "some-other-tool",
	}
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": ".*",
					"hooks":   []interface{}{existing},
				},
			},
		},
	}
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/fablebound hook"}},
	}
	added := mergeHookEntries(settings, entry)
	// PreToolUse already has one entry; fablebound adds another.
	// PostToolUse is empty; fablebound adds one.
	if len(added) != 2 {
		t.Fatalf("expected 2 additions, got %d: %v", len(added), added)
	}
	hooks := settings["hooks"].(map[string]interface{})
	preList := hooks["PreToolUse"].([]interface{})
	if len(preList) != 2 {
		t.Errorf("PreToolUse: expected 2 entries (existing + fablebound), got %d", len(preList))
	}
}

func TestRemoveHookEntries_RemovesFablebound(t *testing.T) {
	cmd := "/usr/local/bin/fablebound hook"
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": ".*",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": cmd},
					},
				},
			},
			"PostToolUse": []interface{}{
				map[string]interface{}{
					"matcher": ".*",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": cmd},
					},
				},
			},
		},
	}
	removed := removeHookEntries(settings)
	if len(removed) != 2 {
		t.Fatalf("expected 2 removals, got %d: %v", len(removed), removed)
	}
	hooks := settings["hooks"].(map[string]interface{})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]interface{})
		if len(list) != 0 {
			t.Errorf("%s: expected empty list after uninstall, got %d entries", ev, len(list))
		}
	}
}

func TestRemoveHookEntries_PreservesOtherHooks(t *testing.T) {
	cmd := "/usr/local/bin/fablebound hook"
	other := map[string]interface{}{
		"matcher": ".*",
		"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "other-tool"}},
	}
	fb := map[string]interface{}{
		"matcher": ".*",
		"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": cmd}},
	}
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{other, fb},
		},
	}
	removed := removeHookEntries(settings)
	if len(removed) != 1 {
		t.Fatalf("expected 1 removal, got %d", len(removed))
	}
	hooks := settings["hooks"].(map[string]interface{})
	preList := hooks["PreToolUse"].([]interface{})
	if len(preList) != 1 {
		t.Errorf("PreToolUse: expected 1 entry remaining (other-tool), got %d", len(preList))
	}
}

func TestRemoveHookEntries_NothingToRemove(t *testing.T) {
	settings := map[string]interface{}{}
	removed := removeHookEntries(settings)
	if len(removed) != 0 {
		t.Errorf("expected 0 removals from empty settings, got %d", len(removed))
	}
}

// TestInstallUninstallRoundtrip runs a full install+uninstall cycle using the
// lower-level merge/remove functions and a temp settings file, verifying the
// JSON file is written correctly and cleaned up.
func TestInstallUninstallRoundtrip(t *testing.T) {
	tmpHome := t.TempDir()
	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")

	cmd := "/usr/local/bin/fablebound hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: cmd}},
	}

	// Initial install via merge + write.
	settings1 := map[string]interface{}{}
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
	var s map[string]interface{}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("settings.json not valid JSON: %v", err)
	}
	hooks, _ := s["hooks"].(map[string]interface{})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]interface{})
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
	var s3 map[string]interface{}
	json.Unmarshal(data3, &s3)
	hooks3, _ := s3["hooks"].(map[string]interface{})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks3[ev].([]interface{})
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
	initial := map[string]interface{}{
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
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/fablebound hook"}},
	}
	mergeHookEntries(settings, entry)
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	after, _ := os.ReadFile(settingsPath)
	var s map[string]interface{}
	json.Unmarshal(after, &s)

	if s["theme"] != "dark" {
		t.Errorf("theme key clobbered (got %v)", s["theme"])
	}
	if s["model"] != "claude-opus" {
		t.Errorf("model key clobbered (got %v)", s["model"])
	}
	hooks, _ := s["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("hooks missing after install")
	}
}

// ── Agent file tests ──────────────────────────────────────────────────────────

// TestInstallAgents_FreshDir verifies that installAgents writes all 6 fb-* files
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
	// Verify all written files are fb-*.md and exist on disk.
	for _, name := range written {
		if !strings.HasPrefix(name, "fb-") || !strings.HasSuffix(name, ".md") {
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

// TestInstallAgents_ContentCheck verifies fb-worker.md exists with model: sonnet.
func TestInstallAgents_ContentCheck(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(agentsDir, "fb-worker.md"))
	if err != nil {
		t.Fatalf("fb-worker.md not found: %v", err)
	}
	if !strings.Contains(string(data), "model: sonnet") {
		t.Errorf("fb-worker.md missing 'model: sonnet'; content:\n%s", string(data))
	}
}

// TestUninstallAgents_RemovesOnlyFbFiles verifies that uninstall removes fb-*.md
// files but leaves other files untouched.
func TestUninstallAgents_RemovesOnlyFbFiles(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Install fb-* agents.
	if _, err := installAgents(agentsDir, false); err != nil {
		t.Fatalf("installAgents: %v", err)
	}

	// Place a non-fb file alongside them.
	otherPath := filepath.Join(agentsDir, "my-custom-agent.md")
	if err := os.WriteFile(otherPath, []byte("# custom"), 0o644); err != nil {
		t.Fatalf("write other agent: %v", err)
	}

	// Uninstall: fbAgentFilesIn + remove.
	removed := fbAgentFilesIn(agentsDir)
	if len(removed) == 0 {
		t.Fatal("expected fb-* files to be found for removal")
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
	// No fb-* files should remain.
	remaining := fbAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("fb-* files still present after uninstall: %v", remaining)
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
	cmd := "/tmp/fablebound hook"
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks:   []settingsHookCommand{{Type: "command", Command: cmd}},
	}
	settings := map[string]interface{}{}
	added := mergeHookEntries(settings, entry)
	if len(added) != 2 {
		t.Fatalf("expected 2 hook additions, got %d", len(added))
	}
	if err := writeSettings(settingsPath, settings); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	// Verify fb-worker.md exists.
	workerPath := filepath.Join(agentsDir, "fb-worker.md")
	if _, err := os.Stat(workerPath); err != nil {
		t.Fatalf("fb-worker.md not found after install: %v", err)
	}

	// Verify settings has hooks.
	data, _ := os.ReadFile(settingsPath)
	var s map[string]interface{}
	json.Unmarshal(data, &s)
	hooks, _ := s["hooks"].(map[string]interface{})
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		list, _ := hooks[ev].([]interface{})
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
	agentFiles := fbAgentFilesIn(agentsDir)
	if len(agentFiles) != 6 {
		t.Fatalf("expected 6 fb-* files for removal, got %d", len(agentFiles))
	}
	for _, name := range agentFiles {
		os.Remove(filepath.Join(agentsDir, name))
	}

	// Verify all cleaned up.
	remaining := fbAgentFilesIn(agentsDir)
	if len(remaining) != 0 {
		t.Errorf("fb-* files remain after uninstall: %v", remaining)
	}
}
