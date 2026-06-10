package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
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
