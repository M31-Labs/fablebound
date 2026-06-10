package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// settingsHookEntry is a single hook entry in the Claude Code settings JSON.
type settingsHookEntry struct {
	Matcher string                `json:"matcher"`
	Hooks   []settingsHookCommand `json:"hooks"`
}

type settingsHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// runInstall implements `fablebound install [--print]`.
// Idempotently adds fablebound PreToolUse and PostToolUse hook entries to
// ~/.claude/settings.json without clobbering existing hooks or keys.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "print the JSON snippet without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	// Use absolute path to the binary for the hook command.
	command := exe + " hook"

	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks: []settingsHookCommand{
			{Type: "command", Command: command},
		},
	}

	if *printOnly {
		snippet := map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreToolUse":  []interface{}{entry},
				"PostToolUse": []interface{}{entry},
			},
		}
		data, _ := json.MarshalIndent(snippet, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	settingsPath, err := claudeSettingsPath()
	if err != nil {
		return err
	}

	settings, err := loadOrInitSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	added := mergeHookEntries(settings, entry)
	if len(added) == 0 {
		fmt.Println("fablebound: hooks already installed in", settingsPath)
		return nil
	}

	if err := writeSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	for _, ev := range added {
		fmt.Printf("fablebound: added %s hook → %s\n", ev, command)
	}
	fmt.Println("fablebound: installed in", settingsPath)
	return nil
}

// runUninstall implements `fablebound uninstall [--print]`.
// Removes only fablebound's hook entries from ~/.claude/settings.json.
func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "print what would be removed without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	settingsPath, err := claudeSettingsPath()
	if err != nil {
		return err
	}

	settings, err := loadOrInitSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	removed := removeHookEntries(settings)
	if len(removed) == 0 {
		fmt.Println("fablebound: no fablebound hooks found in", settingsPath)
		return nil
	}

	if *printOnly {
		for _, ev := range removed {
			fmt.Printf("would remove: %s hook entry\n", ev)
		}
		return nil
	}

	if err := writeSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	for _, ev := range removed {
		fmt.Printf("fablebound: removed %s hook entry\n", ev)
	}
	fmt.Println("fablebound: uninstalled from", settingsPath)
	return nil
}

// claudeSettingsPath returns the path to ~/.claude/settings.json.
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// loadOrInitSettings reads ~/.claude/settings.json into a map, or returns
// an empty map if the file does not exist.
func loadOrInitSettings(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return settings, nil
}

// writeSettings atomically writes the settings map to path (JSON indented).
// Creates the parent directory if needed.
func writeSettings(path string, settings map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// hookCommandMatches returns true if the hook command string is a fablebound hook entry.
func hookCommandMatches(cmd string) bool {
	// cmd should end with " hook" and have "fablebound" as the binary base name.
	if len(cmd) < 4 {
		return false
	}
	if cmd[len(cmd)-5:] != " hook" {
		return false
	}
	binary := cmd[:len(cmd)-5]
	base := filepath.Base(binary)
	return base == "fablebound"
}

// mergeHookEntries adds entry to settings under hooks.PreToolUse and
// hooks.PostToolUse if an identical command is not already present.
// Returns the list of event names actually added (for reporting).
func mergeHookEntries(settings map[string]interface{}, entry settingsHookEntry) []string {
	hooks := getOrCreateMap(settings, "hooks")
	var added []string
	for _, eventName := range []string{"PreToolUse", "PostToolUse"} {
		if mergeHookList(hooks, eventName, entry) {
			added = append(added, eventName)
		}
	}
	settings["hooks"] = hooks
	return added
}

// mergeHookList ensures entry is present in the hook list for eventName.
// Returns true if a new entry was added.
func mergeHookList(hooks map[string]interface{}, eventName string, entry settingsHookEntry) bool {
	raw, ok := hooks[eventName]
	var list []interface{}
	if ok {
		list, _ = raw.([]interface{})
	}

	// Check if our command is already present.
	cmd := entry.Hooks[0].Command
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		hooksRaw, ok := m["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hooksRaw {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if hm["command"] == cmd {
				return false // already present
			}
		}
	}

	// Not present — append.
	newEntry := map[string]interface{}{
		"matcher": entry.Matcher,
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    entry.Hooks[0].Type,
				"command": entry.Hooks[0].Command,
			},
		},
	}
	list = append(list, newEntry)
	hooks[eventName] = list
	return true
}

// removeHookEntries removes all fablebound hook entries from settings.
// Returns the list of event names from which entries were removed.
func removeHookEntries(settings map[string]interface{}) []string {
	hooksRaw, ok := settings["hooks"]
	if !ok {
		return nil
	}
	hooks, ok := hooksRaw.(map[string]interface{})
	if !ok {
		return nil
	}

	var removed []string
	for _, eventName := range []string{"PreToolUse", "PostToolUse"} {
		raw, ok := hooks[eventName]
		if !ok {
			continue
		}
		list, ok := raw.([]interface{})
		if !ok {
			continue
		}
		filtered := filterFableboundEntries(list)
		if len(filtered) < len(list) {
			removed = append(removed, eventName)
			hooks[eventName] = filtered
		}
	}
	settings["hooks"] = hooks
	return removed
}

// filterFableboundEntries removes hook entries whose command is a fablebound hook.
func filterFableboundEntries(list []interface{}) []interface{} {
	var out []interface{}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		hooksRaw, ok := m["hooks"].([]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		hasFablebound := false
		for _, h := range hooksRaw {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, ok := hm["command"].(string); ok && hookCommandMatches(cmd) {
				hasFablebound = true
				break
			}
		}
		if !hasFablebound {
			out = append(out, item)
		}
	}
	return out
}

// getOrCreateMap returns or creates a nested map in parent[key].
func getOrCreateMap(parent map[string]interface{}, key string) map[string]interface{} {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	m := map[string]interface{}{}
	parent[key] = m
	return m
}
