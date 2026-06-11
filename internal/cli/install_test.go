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
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
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
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
	}
	added := mergeHookEntries(settings, entry)
	// PreToolUse already has one entry; tiller adds another.
	// PostToolUse is empty; tiller adds one.
	if len(added) != 2 {
		t.Fatalf("expected 2 additions, got %d: %v", len(added), added)
	}
	hooks := settings["hooks"].(map[string]interface{})
	preList := hooks["PreToolUse"].([]interface{})
	if len(preList) != 2 {
		t.Errorf("PreToolUse: expected 2 entries (existing + tiller), got %d", len(preList))
	}
}

func TestRemoveHookEntries_RemovesTiller(t *testing.T) {
	cmd := "/usr/local/bin/tiller hook"
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
	cmd := "/usr/local/bin/tiller hook"
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

	cmd := "/usr/local/bin/tiller hook"
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
		Hooks:   []settingsHookCommand{{Type: "command", Command: "/usr/local/bin/tiller hook"}},
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
	settings := map[string]interface{}{}
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
	settings := map[string]interface{}{
		"theme": "dark",
		"hooks": map[string]interface{}{
			"PreToolUse":  []interface{}{},
			"PostToolUse": []interface{}{},
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
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{"matcher": ".*"},
			},
			"PostToolUse": []interface{}{},
		},
	}
	pruneEmptyHookContainers(settings)
	hooks, ok := settings["hooks"].(map[string]interface{})
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
	settings := map[string]interface{}{}
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
		var s map[string]interface{}
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
	settings := map[string]interface{}{}
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
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": ".*",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": tillerCmd},
					},
				},
				map[string]interface{}{
					"matcher": ".*",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": foreignCmd},
					},
				},
			},
			"PostToolUse": []interface{}{
				map[string]interface{}{
					"matcher": ".*",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": tillerCmd},
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
	var s map[string]interface{}
	if err := json.Unmarshal(after, &s); err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	hooks, ok := s["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("hooks map must remain (foreign hook present)")
	}

	// PreToolUse must have exactly 1 entry (foreign), no tiller entry.
	preList, _ := hooks["PreToolUse"].([]interface{})
	if len(preList) != 1 {
		t.Errorf("PreToolUse: expected 1 foreign entry, got %d", len(preList))
	}

	// PostToolUse must be absent (it was all-tiller → empty → pruned).
	if _, postOk := hooks["PostToolUse"]; postOk {
		t.Error("PostToolUse must be pruned (was all-tiller, now empty)")
	}

	// The foreign command must survive.
	if len(preList) > 0 {
		entry, _ := preList[0].(map[string]interface{})
		hooksCmds, _ := entry["hooks"].([]interface{})
		if len(hooksCmds) > 0 {
			h, _ := hooksCmds[0].(map[string]interface{})
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
		tc := tc
		t.Run(tc.cmd, func(t *testing.T) {
			got := hookCommandMatches(tc.cmd)
			if got != tc.want {
				t.Errorf("hookCommandMatches(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
