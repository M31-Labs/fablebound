package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/agents"
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

// runInstall implements `tiller install [--print] [--project]`.
// Idempotently:
//
//	(a) merges PreToolUse + PostToolUse hook entries into settings.json, and
//	(b) writes the tiller-* subagent definition files into the agents/ directory.
//
// --print: show what would change, write nothing.
// --project: install into ./.claude/ instead of ~/.claude/ (repo-local scope).
func runInstall(args []string) error {
	fset := flag.NewFlagSet("install", flag.ContinueOnError)
	printOnly := fset.Bool("print", false, "print what would change without writing")
	project := fset.Bool("project", false, "install into ./.claude/ instead of ~/.claude/")
	if err := fset.Parse(args); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	command := exe + " hook"

	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks: []settingsHookCommand{
			{Type: "command", Command: command},
		},
	}

	settingsPath, agentsDir, err := claudePaths(*project)
	if err != nil {
		return err
	}

	if *printOnly {
		return printInstallPlan(settingsPath, agentsDir, entry, command)
	}

	// ── (a) Hooks ─────────────────────────────────────────────────────────────
	settings, err := loadOrInitSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	added := mergeHookEntries(settings, entry)
	if len(added) == 0 {
		fmt.Println("tiller: hooks already installed in", settingsPath)
	} else {
		if err := writeSettings(settingsPath, settings); err != nil {
			return fmt.Errorf("write settings: %w", err)
		}
		for _, ev := range added {
			fmt.Printf("tiller: added %s hook → %s\n", ev, command)
		}
		fmt.Println("tiller: hooks installed in", settingsPath)
	}

	// ── (b) Agents ────────────────────────────────────────────────────────────
	written, err := installAgents(agentsDir, false)
	if err != nil {
		return fmt.Errorf("install agents: %w", err)
	}
	if len(written) == 0 {
		fmt.Println("tiller: tiller-* agents already up-to-date in", agentsDir)
	} else {
		for _, name := range written {
			fmt.Printf("tiller: wrote agent → %s\n", filepath.Join(agentsDir, name))
		}
		fmt.Println("tiller: agents installed in", agentsDir)
	}

	return nil
}

// printInstallPlan prints what install would do without writing anything.
func printInstallPlan(settingsPath, agentsDir string, entry settingsHookEntry, command string) error {
	fmt.Println("# tiller install --print (no files written)")
	fmt.Println()

	// Hook snippet.
	snippet := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse":  []interface{}{entry},
			"PostToolUse": []interface{}{entry},
		},
	}
	data, _ := json.MarshalIndent(snippet, "", "  ")
	fmt.Printf("## hooks → %s\n%s\n\n", settingsPath, string(data))

	// Agent files.
	fmt.Printf("## agents → %s\n", agentsDir)
	agentFS := agents.EmbeddedDefaults()
	entries, _ := fs.ReadDir(agentFS, ".")
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "tiller-") {
			fmt.Printf("  %s\n", filepath.Join(agentsDir, e.Name()))
		}
	}
	return nil
}

// runUninstall implements `tiller uninstall [--print] [--project]`.
//
// Removes only tiller's hook entries and owned tiller-*.md agent files.
// Foreign hooks and user-created agent files are never touched.
// After removal, prints a trial-exit report showing what was removed,
// what is intentionally left, and how to finish cleanup.
//
// --print: show the plan without writing (idempotent dry-run).
// --project: uninstall from ./.claude/ instead of ~/.claude/.
func runUninstall(args []string) error {
	fset := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	printOnly := fset.Bool("print", false, "print what would be removed without writing")
	project := fset.Bool("project", false, "uninstall from ./.claude/ instead of ~/.claude/")
	if err := fset.Parse(args); err != nil {
		return err
	}

	settingsPath, agentsDir, err := claudePaths(*project)
	if err != nil {
		return err
	}

	// Load settings — missing file is not an error (nothing to uninstall).
	settings, settingsMissing, err := loadSettingsForUninstall(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	// Determine what would be removed (mutates settings in-place for hooks).
	var removedHooks []string
	if !settingsMissing {
		removedHooks = removeHookEntries(settings)
		pruneEmptyHookContainers(settings)
	}
	agentFiles := ownedTillerAgentFiles(agentsDir)

	if len(removedHooks) == 0 && len(agentFiles) == 0 {
		fmt.Println("tiller: nothing to uninstall")
		return nil
	}

	if *printOnly {
		fmt.Println("# tiller uninstall --print (no files written)")
		fmt.Println()
		if len(removedHooks) > 0 {
			fmt.Printf("## hooks to remove from %s\n", settingsPath)
			for _, ev := range removedHooks {
				fmt.Printf("  %s hook entry (command ending with 'tiller hook')\n", ev)
			}
			fmt.Println()
		}
		if len(agentFiles) > 0 {
			fmt.Printf("## agent files to remove from %s\n", agentsDir)
			for _, name := range agentFiles {
				fmt.Printf("  %s\n", filepath.Join(agentsDir, name))
			}
		}
		return nil
	}

	// ── Remove hooks ──────────────────────────────────────────────────────────
	if len(removedHooks) > 0 {
		if err := writeSettings(settingsPath, settings); err != nil {
			return fmt.Errorf("write settings: %w", err)
		}
		for _, ev := range removedHooks {
			fmt.Printf("tiller: removed %s hook entry from %s\n", ev, settingsPath)
		}
	}

	// ── Remove owned agent files ──────────────────────────────────────────────
	var removedAgents []string
	for _, name := range agentFiles {
		p := filepath.Join(agentsDir, name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent %s: %w", p, err)
		}
		fmt.Printf("tiller: removed agent %s\n", p)
		removedAgents = append(removedAgents, p)
	}

	// ── Trial-exit report ─────────────────────────────────────────────────────
	printTrialExitReport(removedHooks, removedAgents, settingsPath)
	return nil
}

// loadSettingsForUninstall reads settings.json for uninstall.
// Returns (settings, missing=true, nil) when the file does not exist.
// Returns (nil, false, err) on parse errors (malformed JSON).
func loadSettingsForUninstall(path string) (settings map[string]interface{}, missing bool, err error) {
	data, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return nil, true, nil
	}
	if readErr != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	var s map[string]interface{}
	if jsonErr := json.Unmarshal(data, &s); jsonErr != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, jsonErr)
	}
	return s, false, nil
}

// pruneEmptyHookContainers removes empty arrays and the hooks map itself from
// settings when they become empty after tiller hook entries are removed.
// This prevents accumulation of empty "hooks": {} husks in settings.json.
func pruneEmptyHookContainers(settings map[string]interface{}) {
	hooksRaw, ok := settings["hooks"]
	if !ok {
		return
	}
	hooks, ok := hooksRaw.(map[string]interface{})
	if !ok {
		return
	}
	// Remove keys whose arrays are now empty.
	for k, v := range hooks {
		list, ok := v.([]interface{})
		if ok && len(list) == 0 {
			delete(hooks, k)
		}
	}
	// If the hooks map itself is now empty, remove it from settings.
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
}

// ownedTillerAgentFiles returns the list of agent filenames in agentsDir that
// tiller owns — i.e. filenames that exactly match an embedded tiller-*.md file.
// User-created files named tiller-*.md that are NOT in the embedded set are
// intentionally left alone.
func ownedTillerAgentFiles(agentsDir string) []string {
	// Build the set of names tiller owns from the embedded FS.
	ownedSet := make(map[string]bool)
	for _, name := range agents.AgentFileNames() {
		ownedSet[name] = true
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && ownedSet[e.Name()] {
			out = append(out, e.Name())
		}
	}
	return out
}

// printTrialExitReport prints the short post-uninstall summary.
func printTrialExitReport(removedHooks []string, removedAgentPaths []string, settingsPath string) {
	fmt.Println()
	fmt.Println("tiller uninstalled.")
	fmt.Printf("  hooks removed:  %d (%s)\n", len(removedHooks), settingsPath)
	fmt.Printf("  agents removed: %d\n", len(removedAgentPaths))
	fmt.Println()
	fmt.Println("What's still on disk (yours to keep or remove):")
	fmt.Println("  binary:   rm $(which tiller)")
	fmt.Println("  run data: .tiller/ dirs in your projects (run history, audit logs — untouched)")
	fmt.Println()
	fmt.Println("Active fable sessions disengage on their next tool call — no restart needed.")
}

// claudePaths returns the settings.json path and agents/ directory for the
// chosen scope (project = ./.claude, default = ~/.claude).
func claudePaths(project bool) (settingsPath, agentsDir string, err error) {
	var claudeDir string
	if project {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return "", "", fmt.Errorf("getwd: %w", cerr)
		}
		claudeDir = filepath.Join(cwd, ".claude")
	} else {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", fmt.Errorf("home dir: %w", herr)
		}
		claudeDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(claudeDir, "settings.json"), filepath.Join(claudeDir, "agents"), nil
}

// claudeSettingsPath returns the path to ~/.claude/settings.json.
// Kept for backward compatibility with existing test helpers.
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// installAgents writes the embedded tiller-*.md files into agentsDir.
// If dryRun is true it returns the list of names that would be written without
// writing anything.  Only fb-* files are touched (non-fb files are left alone).
// Returns the list of files actually written (or that would be written).
func installAgents(agentsDir string, dryRun bool) ([]string, error) {
	agentFS := agents.EmbeddedDefaults()
	entries, err := fs.ReadDir(agentFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded agents: %w", err)
	}

	if !dryRun {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir agents dir: %w", err)
		}
	}

	var written []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "tiller-") {
			continue
		}
		dest := filepath.Join(agentsDir, e.Name())

		// Read embedded content.
		content, err := fs.ReadFile(agentFS, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}

		// Check if already identical on disk.
		if existing, err := os.ReadFile(dest); err == nil {
			if string(existing) == string(content) {
				continue // already up-to-date
			}
		}

		if !dryRun {
			if err := os.WriteFile(dest, content, 0o644); err != nil {
				return nil, fmt.Errorf("write %s: %w", dest, err)
			}
		}
		written = append(written, e.Name())
	}
	return written, nil
}

// tillerAgentFilesIn returns the list of tiller-*.md filenames present in agentsDir.
func tillerAgentFilesIn(agentsDir string) []string {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "tiller-") && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	return out
}

// loadOrInitSettings reads the settings file into a map, or returns
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

// hookCommandMatches returns true if the hook command string is a tiller hook entry.
func hookCommandMatches(cmd string) bool {
	// cmd should end with " hook" and have "tiller" as the binary base name.
	if len(cmd) < 4 {
		return false
	}
	if cmd[len(cmd)-5:] != " hook" {
		return false
	}
	binary := cmd[:len(cmd)-5]
	base := filepath.Base(binary)
	return base == "tiller"
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

// removeHookEntries removes all tiller hook entries from settings.
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
		filtered := filterTillerEntries(list)
		if len(filtered) < len(list) {
			removed = append(removed, eventName)
			hooks[eventName] = filtered
		}
	}
	settings["hooks"] = hooks
	return removed
}

// filterTillerEntries removes hook entries whose command is a tiller hook.
func filterTillerEntries(list []interface{}) []interface{} {
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
		hasTiller := false
		for _, h := range hooksRaw {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, ok := hm["command"].(string); ok && hookCommandMatches(cmd) {
				hasTiller = true
				break
			}
		}
		if !hasTiller {
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
