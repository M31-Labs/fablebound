package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/agents"
	"m31labs.dev/tiller/internal/tier"
)

// settingsHookEntry is a single hook entry in a backend hooks config.
type settingsHookEntry struct {
	Matcher string                `json:"matcher"`
	Hooks   []settingsHookCommand `json:"hooks"`
}

type settingsHookCommand struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

const (
	defaultCodexAgentMaxThreads = 12
	defaultCodexAgentMaxDepth   = 2

	tillerCodexNotesBegin = "<!-- BEGIN TILLER CODEX OPERATING NOTES -->"
	tillerCodexNotesEnd   = "<!-- END TILLER CODEX OPERATING NOTES -->"
	tillerCodexSkillPath  = "skills/using-tiller/SKILL.md"
	sirenaCodexSkillPath  = "skills/using-sirena/SKILL.md"

	tillerOpenCodeNotesBegin = "<!-- BEGIN TILLER OPENCODE OPERATING NOTES -->"
	tillerOpenCodeNotesEnd   = "<!-- END TILLER OPENCODE OPERATING NOTES -->"
)

// runInstall implements `tiller install [--backend BACKEND] [--print] [--project]`.
// Idempotently:
//
//	(a) merges backend-specific hook entries into the backend config, and
//	(b) writes the tiller-* persona files into the backend agents directory.
//
// --print: show what would change, write nothing.
// --project: install into the repo-local backend config instead of user config.
func runInstall(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if len(args) == 0 {
		return runInstallWizard(os.Stdin, os.Stdout, exe)
	}

	fset := flag.NewFlagSet("install", flag.ContinueOnError)
	backend := fset.String("backend", "claude-code", "ambient backend: claude-code, codex, or opencode")
	printOnly := fset.Bool("print", false, "print what would change without writing")
	project := fset.Bool("project", false, "install into repo-local backend config instead of user config")
	global := fset.Bool("global", false, "install into user config instead of repo-local config")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *project && *global {
		return fmt.Errorf("install: --project and --global are mutually exclusive")
	}

	switch *backend {
	case "claude-code":
		return runInstallClaude(*printOnly, *project, exe+" hook")
	case "codex":
		return runInstallCodex(*printOnly, *project, exe+" hook --backend codex")
	case "opencode":
		return runInstallOpenCode(*printOnly, *project)
	default:
		return fmt.Errorf("unsupported install backend %q (want claude-code, codex, or opencode)", *backend)
	}
}

func runInstallWizard(in io.Reader, out io.Writer, exe string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	fmt.Fprintf(out, "tiller install: project scope (%s)\n", cwd)
	fmt.Fprintln(out, "Choose agent harness config to install:")
	fmt.Fprintln(out, "  1) Claude Code")
	fmt.Fprintln(out, "  2) Codex")
	fmt.Fprintln(out, "  3) OpenCode")
	fmt.Fprintln(out, "  4) All")
	fmt.Fprint(out, "Selection [1]: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(line) == 0 {
		return fmt.Errorf("install: no selection provided; use --backend codex --project, --backend claude-code --project, or --backend opencode --project")
	}
	choice := strings.ToLower(strings.TrimSpace(line))
	switch choice {
	case "", "1", "claude", "claude-code", "claude code":
		return runInstallClaude(false, true, exe+" hook")
	case "2", "codex":
		return runInstallCodex(false, true, exe+" hook --backend codex")
	case "3", "opencode", "open-code", "open code":
		return runInstallOpenCode(false, true)
	case "4", "a", "all", "both":
		if err := runInstallClaude(false, true, exe+" hook"); err != nil {
			return err
		}
		if err := runInstallCodex(false, true, exe+" hook --backend codex"); err != nil {
			return err
		}
		return runInstallOpenCode(false, true)
	case "q", "quit", "exit":
		fmt.Fprintln(out, "tiller: install cancelled")
		return nil
	default:
		return fmt.Errorf("install: unknown selection %q (choose 1, 2, 3, or 4)", strings.TrimSpace(line))
	}
}

func runInstallClaude(printOnly, project bool, command string) error {
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks: []settingsHookCommand{
			{Type: "command", Command: command},
		},
	}

	settingsPath, agentsDir, err := claudePaths(project)
	if err != nil {
		return err
	}

	if printOnly {
		return printInstallPlan(settingsPath, agentsDir, entry, []string{"PreToolUse", "PostToolUse"}, agents.AgentFileNames())
	}

	// Hooks.
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
			fmt.Printf("tiller: added %s hook -> %s\n", ev, command)
		}
		fmt.Println("tiller: hooks installed in", settingsPath)
	}

	// Agents.
	written, err := installAgentsWithConfig(agentsDir, false, loadInstallAmbientConfig(project, "claude-code"))
	if err != nil {
		return fmt.Errorf("install agents: %w", err)
	}
	if len(written) == 0 {
		fmt.Println("tiller: tiller-* agents already up-to-date in", agentsDir)
	} else {
		for _, name := range written {
			fmt.Printf("tiller: wrote agent -> %s\n", filepath.Join(agentsDir, name))
		}
		fmt.Println("tiller: agents installed in", agentsDir)
	}

	return nil
}

func runInstallCodex(printOnly, project bool, command string) error {
	entry := settingsHookEntry{
		Matcher: ".*",
		Hooks: []settingsHookCommand{
			{
				Type:          "command",
				Command:       command,
				Timeout:       30,
				StatusMessage: "Checking tiller ambient policy",
			},
		},
	}

	hooksPath, agentsDir, err := codexPaths(project)
	if err != nil {
		return err
	}
	configPath, err := codexConfigPath(project)
	if err != nil {
		return err
	}
	notesPath, err := codexOperatingNotesPath(project)
	if err != nil {
		return err
	}
	tillerSkillPath, err := codexSkillPath(project, tillerCodexSkillPath)
	if err != nil {
		return err
	}
	sirenaSkillPath, err := codexSkillPath(project, sirenaCodexSkillPath)
	if err != nil {
		return err
	}

	if printOnly {
		return printCodexInstallPlan(hooksPath, agentsDir, configPath, notesPath, tillerSkillPath, sirenaSkillPath, entry)
	}

	settings, err := loadOrInitSettings(hooksPath)
	if err != nil {
		return fmt.Errorf("load hooks config: %w", err)
	}

	added := mergeHookEntriesForEvents(settings, entry, codexManagedHookEvents())
	if len(added) == 0 {
		fmt.Println("tiller: Codex hooks already installed in", hooksPath)
	} else {
		if err := writeSettings(hooksPath, settings); err != nil {
			return fmt.Errorf("write hooks config: %w", err)
		}
		for _, ev := range added {
			fmt.Printf("tiller: added Codex %s hook -> %s\n", ev, command)
		}
		fmt.Println("tiller: Codex hooks installed in", hooksPath)
	}

	configChanged, err := installCodexConfig(configPath, false)
	if err != nil {
		return fmt.Errorf("install Codex config: %w", err)
	}
	if configChanged {
		fmt.Printf("tiller: wrote Codex config -> %s\n", configPath)
	} else {
		fmt.Println("tiller: Codex config already up-to-date in", configPath)
	}
	if notesPath != "" {
		notesChanged, err := installCodexOperatingNotes(notesPath, false)
		if err != nil {
			return fmt.Errorf("install Codex operating notes: %w", err)
		}
		if notesChanged {
			fmt.Printf("tiller: wrote Codex operating notes -> %s\n", notesPath)
		} else {
			fmt.Println("tiller: Codex operating notes already up-to-date in", notesPath)
		}
	}
	skillChanged, err := installCodexSkill(tillerSkillPath, codexSkillSnippet(), false)
	if err != nil {
		return fmt.Errorf("install Codex Tiller skill: %w", err)
	}
	if skillChanged {
		fmt.Printf("tiller: wrote Codex Tiller skill -> %s\n", tillerSkillPath)
	} else {
		fmt.Println("tiller: Codex Tiller skill already up-to-date in", tillerSkillPath)
	}
	sirenaChanged, err := installCodexSkill(sirenaSkillPath, codexSirenaSkillSnippet(), false)
	if err != nil {
		return fmt.Errorf("install Codex Sirena skill: %w", err)
	}
	if sirenaChanged {
		fmt.Printf("tiller: wrote Codex Sirena skill -> %s\n", sirenaSkillPath)
	} else {
		fmt.Println("tiller: Codex Sirena skill already up-to-date in", sirenaSkillPath)
	}

	written, err := installCodexAgents(agentsDir, false)
	if err != nil {
		return fmt.Errorf("install Codex agents: %w", err)
	}
	if len(written) == 0 {
		fmt.Println("tiller: Codex tiller-* agents already up-to-date in", agentsDir)
	} else {
		for _, name := range written {
			fmt.Printf("tiller: wrote Codex agent -> %s\n", filepath.Join(agentsDir, name))
		}
		fmt.Println("tiller: Codex agents installed in", agentsDir)
	}

	return nil
}

func runInstallOpenCode(printOnly, project bool) error {
	configPath, agentsDir, notesPath, instructionPath, err := opencodePaths(project)
	if err != nil {
		return err
	}

	if printOnly {
		return printOpenCodeInstallPlan(configPath, agentsDir, notesPath, instructionPath)
	}

	configChanged, err := installOpenCodeConfig(configPath, instructionPath, false)
	if err != nil {
		return fmt.Errorf("install OpenCode config: %w", err)
	}
	if configChanged {
		fmt.Printf("tiller: wrote OpenCode config -> %s\n", configPath)
	} else {
		fmt.Println("tiller: OpenCode config already up-to-date in", configPath)
	}

	notesChanged, err := installOpenCodeNotes(notesPath, false)
	if err != nil {
		return fmt.Errorf("install OpenCode operating notes: %w", err)
	}
	if notesChanged {
		fmt.Printf("tiller: wrote OpenCode operating notes -> %s\n", notesPath)
	} else {
		fmt.Println("tiller: OpenCode operating notes already up-to-date in", notesPath)
	}

	written, err := installOpenCodeAgents(agentsDir, false)
	if err != nil {
		return fmt.Errorf("install OpenCode agents: %w", err)
	}
	if len(written) == 0 {
		fmt.Println("tiller: OpenCode tiller-* agents already up-to-date in", agentsDir)
	} else {
		for _, name := range written {
			fmt.Printf("tiller: wrote OpenCode agent -> %s\n", filepath.Join(agentsDir, name))
		}
		fmt.Println("tiller: OpenCode agents installed in", agentsDir)
	}

	return nil
}

func printCodexInstallPlan(hooksPath, agentsDir, configPath, notesPath, tillerSkillPath, sirenaSkillPath string, entry settingsHookEntry) error {
	if err := printInstallPlan(hooksPath, agentsDir, entry, codexManagedHookEvents(), agents.CodexAgentFileNames()); err != nil {
		return err
	}
	fmt.Printf("\n## config -> %s\n%s", configPath, codexConfigSnippet())
	if notesPath != "" {
		fmt.Printf("\n## operating notes -> %s\n%s", notesPath, codexOperatingNotesSnippet())
	}
	fmt.Printf("\n## skill -> %s\n%s", tillerSkillPath, codexSkillSnippet())
	fmt.Printf("\n## skill -> %s\n%s", sirenaSkillPath, codexSirenaSkillSnippet())
	return nil
}

func printOpenCodeInstallPlan(configPath, agentsDir, notesPath, instructionPath string) error {
	fmt.Println("# tiller install --print (no files written)")
	fmt.Println()
	fmt.Printf("## config -> %s\n%s\n", configPath, opencodeConfigSnippet(instructionPath))
	fmt.Printf("\n## operating notes -> %s\n%s", notesPath, opencodeOperatingNotesSnippet())
	fmt.Printf("\n## agents -> %s\n", agentsDir)
	for _, name := range agents.OpenCodeAgentFileNames() {
		if strings.HasPrefix(name, "tiller-") {
			fmt.Printf("  %s\n", filepath.Join(agentsDir, name))
		}
	}
	return nil
}

// printInstallPlan prints what install would do without writing anything.
func printInstallPlan(settingsPath, agentsDir string, entry settingsHookEntry, events []string, agentNames []string) error {
	fmt.Println("# tiller install --print (no files written)")
	fmt.Println()

	// Hook snippet.
	hooks := make(map[string]any, len(events))
	for _, eventName := range events {
		hooks[eventName] = []any{entry}
	}
	snippet := map[string]any{
		"hooks": hooks,
	}
	data, _ := json.MarshalIndent(snippet, "", "  ")
	fmt.Printf("## hooks -> %s\n%s\n\n", settingsPath, string(data))

	// Agent files.
	fmt.Printf("## agents -> %s\n", agentsDir)
	for _, name := range agentNames {
		if strings.HasPrefix(name, "tiller-") {
			fmt.Printf("  %s\n", filepath.Join(agentsDir, name))
		}
	}
	return nil
}

func codexManagedHookEvents() []string {
	return []string{"PreToolUse", "SessionStart", "SubagentStart"}
}

// runUninstall implements `tiller uninstall [--backend BACKEND] [--print] [--project]`.
//
// Removes only tiller's hook entries and owned tiller-*.md agent files.
// Foreign hooks and user-created agent files are never touched.
// After removal, prints a trial-exit report showing what was removed,
// what is intentionally left, and how to finish cleanup.
//
// --print: show the plan without writing (idempotent dry-run).
// --project: uninstall from repo-local backend config instead of user config.
func runUninstall(args []string) error {
	fset := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	backend := fset.String("backend", "claude-code", "ambient backend: claude-code, codex, or opencode")
	printOnly := fset.Bool("print", false, "print what would be removed without writing")
	project := fset.Bool("project", false, "uninstall from repo-local backend config instead of user config")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *backend == "opencode" {
		return runUninstallOpenCode(*printOnly, *project)
	}

	var (
		settingsPath string
		agentsDir    string
		events       []string
		agentFiles   []string
		err          error
	)
	switch *backend {
	case "claude-code":
		settingsPath, agentsDir, err = claudePaths(*project)
		events = []string{"PreToolUse", "PostToolUse"}
	case "codex":
		settingsPath, agentsDir, err = codexPaths(*project)
		events = codexManagedHookEvents()
	default:
		return fmt.Errorf("unsupported uninstall backend %q (want claude-code, codex, or opencode)", *backend)
	}
	if err != nil {
		return err
	}

	// Load settings; a missing file is not an error.
	settings, settingsMissing, err := loadSettingsForUninstall(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	// Determine what would be removed (mutates settings in-place for hooks).
	var removedHooks []string
	if !settingsMissing {
		removedHooks = removeHookEntriesForEvents(settings, events)
		pruneEmptyHookContainers(settings)
	}
	switch *backend {
	case "claude-code":
		agentFiles = ownedTillerAgentFiles(agentsDir)
	case "codex":
		agentFiles = ownedCodexAgentFiles(agentsDir)
	}
	configPath := ""
	removeCodexConfig := false
	var codexSkillsToRemove []string
	if *backend == "codex" {
		configPath, err = codexConfigPath(*project)
		if err != nil {
			return err
		}
		removeCodexConfig, err = codexConfigHasManagedDefaults(configPath)
		if err != nil {
			return fmt.Errorf("inspect Codex config defaults: %w", err)
		}
		tillerSkillPath, err := codexSkillPath(*project, tillerCodexSkillPath)
		if err != nil {
			return err
		}
		removeTillerSkill, err := codexSkillHasManagedSnippet(tillerSkillPath, codexSkillSnippet())
		if err != nil {
			return fmt.Errorf("inspect Codex Tiller skill: %w", err)
		}
		if removeTillerSkill {
			codexSkillsToRemove = append(codexSkillsToRemove, tillerSkillPath)
		}
		sirenaSkillPath, err := codexSkillPath(*project, sirenaCodexSkillPath)
		if err != nil {
			return err
		}
		removeSirenaSkill, err := codexSkillHasManagedSnippet(sirenaSkillPath, codexSirenaSkillSnippet())
		if err != nil {
			return fmt.Errorf("inspect Codex Sirena skill: %w", err)
		}
		if removeSirenaSkill {
			codexSkillsToRemove = append(codexSkillsToRemove, sirenaSkillPath)
		}
	}

	if len(removedHooks) == 0 && len(agentFiles) == 0 && !removeCodexConfig && len(codexSkillsToRemove) == 0 {
		fmt.Println("tiller: nothing to uninstall")
		return nil
	}

	if *printOnly {
		fmt.Println("# tiller uninstall --print (no files written)")
		fmt.Println()
		if len(removedHooks) > 0 {
			fmt.Printf("## hooks to remove from %s\n", settingsPath)
			for _, ev := range removedHooks {
				fmt.Printf("  %s hook entry (tiller hook command)\n", ev)
			}
			fmt.Println()
		}
		if len(agentFiles) > 0 {
			fmt.Printf("## agent files to remove from %s\n", agentsDir)
			for _, name := range agentFiles {
				fmt.Printf("  %s\n", filepath.Join(agentsDir, name))
			}
		}
		if removeCodexConfig {
			fmt.Printf("\n## Codex config defaults to remove from %s\n", configPath)
			fmt.Println("  [agents].max_threads = 12")
			fmt.Println("  [agents].max_depth = 2")
		}
		if len(codexSkillsToRemove) > 0 {
			fmt.Println("\n## Codex skills to remove")
			for _, path := range codexSkillsToRemove {
				fmt.Printf("  %s\n", path)
			}
		}
		return nil
	}

	// Remove hooks.
	if len(removedHooks) > 0 {
		if err := writeSettings(settingsPath, settings); err != nil {
			return fmt.Errorf("write settings: %w", err)
		}
		for _, ev := range removedHooks {
			fmt.Printf("tiller: removed %s hook entry from %s\n", ev, settingsPath)
		}
	}

	// Remove owned agent files.
	var removedAgents []string
	for _, name := range agentFiles {
		p := filepath.Join(agentsDir, name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent %s: %w", p, err)
		}
		fmt.Printf("tiller: removed agent %s\n", p)
		removedAgents = append(removedAgents, p)
	}
	if removeCodexConfig {
		changed, err := removeCodexConfigDefaults(configPath)
		if err != nil {
			return fmt.Errorf("remove Codex config defaults: %w", err)
		}
		if changed {
			fmt.Printf("tiller: removed managed Codex agent defaults from %s\n", configPath)
		}
	}
	for _, skillPath := range codexSkillsToRemove {
		if err := os.Remove(skillPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove Codex skill %s: %w", skillPath, err)
		}
		fmt.Printf("tiller: removed Codex skill %s\n", skillPath)
	}

	// Trial-exit report.
	printTrialExitReport(removedHooks, removedAgents, settingsPath)
	return nil
}

func runUninstallOpenCode(printOnly, project bool) error {
	configPath, agentsDir, notesPath, instructionPath, err := opencodePaths(project)
	if err != nil {
		return err
	}

	agentFiles := ownedOpenCodeAgentFiles(agentsDir)
	removeNotes, err := opencodeNotesHasManagedSnippet(notesPath)
	if err != nil {
		return err
	}
	removeInstruction, err := opencodeConfigHasInstruction(configPath, instructionPath)
	if err != nil {
		return err
	}

	if len(agentFiles) == 0 && !removeNotes && !removeInstruction {
		fmt.Println("tiller: nothing to uninstall")
		return nil
	}

	if printOnly {
		fmt.Println("# tiller uninstall --print (no files written)")
		fmt.Println()
		if len(agentFiles) > 0 {
			fmt.Printf("## OpenCode agent files to remove from %s\n", agentsDir)
			for _, name := range agentFiles {
				fmt.Printf("  %s\n", filepath.Join(agentsDir, name))
			}
		}
		if removeNotes {
			fmt.Printf("\n## OpenCode operating notes to remove\n  %s\n", notesPath)
		}
		if removeInstruction {
			fmt.Printf("\n## OpenCode config instruction reference to remove from %s\n  %s\n", configPath, instructionPath)
		}
		return nil
	}

	var removedAgents []string
	for _, name := range agentFiles {
		p := filepath.Join(agentsDir, name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove OpenCode agent %s: %w", p, err)
		}
		fmt.Printf("tiller: removed OpenCode agent %s\n", p)
		removedAgents = append(removedAgents, p)
	}

	if removeNotes {
		if err := os.Remove(notesPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove OpenCode notes %s: %w", notesPath, err)
		}
		fmt.Printf("tiller: removed OpenCode operating notes %s\n", notesPath)
	}

	if removeInstruction {
		if changed, err := removeOpenCodeConfigInstruction(configPath, instructionPath); err != nil {
			return err
		} else if changed {
			fmt.Printf("tiller: removed OpenCode instruction reference from %s\n", configPath)
		}
	}

	printTrialExitReport(nil, removedAgents, configPath)
	return nil
}

// loadSettingsForUninstall reads settings.json for uninstall.
// Returns (settings, missing=true, nil) when the file does not exist.
// Returns (nil, false, err) on parse errors (malformed JSON).
func loadSettingsForUninstall(path string) (settings map[string]any, missing bool, err error) {
	data, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return nil, true, nil
	}
	if readErr != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	var s map[string]any
	if jsonErr := json.Unmarshal(data, &s); jsonErr != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, jsonErr)
	}
	return s, false, nil
}

// pruneEmptyHookContainers removes empty arrays and the hooks map itself from
// settings when they become empty after tiller hook entries are removed.
// This prevents accumulation of empty "hooks": {} husks in settings.json.
func pruneEmptyHookContainers(settings map[string]any) {
	hooksRaw, ok := settings["hooks"]
	if !ok {
		return
	}
	hooks, ok := hooksRaw.(map[string]any)
	if !ok {
		return
	}
	// Remove keys whose arrays are now empty.
	for k, v := range hooks {
		list, ok := v.([]any)
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
// tiller owns - i.e. filenames that exactly match an embedded tiller-*.md file.
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

func ownedCodexAgentFiles(agentsDir string) []string {
	ownedSet := make(map[string]bool)
	for _, name := range agents.CodexAgentFileNames() {
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

func ownedOpenCodeAgentFiles(agentsDir string) []string {
	ownedSet := make(map[string]bool)
	for _, name := range agents.OpenCodeAgentFileNames() {
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
	fmt.Println("  run data: .tiller/ dirs in your projects (run history, audit logs - untouched)")
	fmt.Println()
	fmt.Println("Active governed sessions disengage on their next tool call - no restart needed.")
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

// codexPaths returns the hooks.json path and agents/ directory for the chosen
// scope (project = ./.codex, default = ~/.codex).
func codexPaths(project bool) (hooksPath, agentsDir string, err error) {
	codexDir, err := codexBaseDir(project)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(codexDir, "hooks.json"), filepath.Join(codexDir, "agents"), nil
}

func codexConfigPath(project bool) (string, error) {
	codexDir, err := codexBaseDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(codexDir, "config.toml"), nil
}

func codexOperatingNotesPath(project bool) (string, error) {
	if !project {
		return "", nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return filepath.Join(cwd, "AGENTS.md"), nil
}

func codexSkillPath(project bool, relPath string) (string, error) {
	codexDir, err := codexBaseDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(codexDir, filepath.FromSlash(relPath)), nil
}

func codexBaseDir(project bool) (string, error) {
	var codexDir string
	if project {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return "", fmt.Errorf("getwd: %w", cerr)
		}
		codexDir = filepath.Join(cwd, ".codex")
	} else {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("home dir: %w", herr)
		}
		codexDir = filepath.Join(home, ".codex")
	}
	return codexDir, nil
}

func opencodePaths(project bool) (configPath, agentsDir, notesPath, instructionPath string, err error) {
	if project {
		cwd, cerr := os.Getwd()
		if cerr != nil {
			err = fmt.Errorf("getwd: %w", cerr)
			return
		}
		configPath = filepath.Join(cwd, "opencode.json")
		agentsDir = filepath.Join(cwd, ".opencode", "agents")
		notesPath = filepath.Join(cwd, ".opencode", "tiller.md")
		instructionPath = ".opencode/tiller.md"
		return
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		err = fmt.Errorf("home dir: %w", herr)
		return
	}
	base := filepath.Join(home, ".config", "opencode")
	configPath = filepath.Join(base, "opencode.json")
	agentsDir = filepath.Join(base, "agents")
	notesPath = filepath.Join(base, "tiller.md")
	instructionPath = "tiller.md"
	return
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

func loadInstallAmbientConfig(project bool, backend string) *tier.AmbientConfig {
	projectDir := ""
	if project {
		if cwd, err := os.Getwd(); err == nil {
			projectDir = cwd
		}
	}
	cfg, err := tier.Load(projectDir)
	if err != nil {
		cfg, err = tier.Load("")
		if err != nil {
			return nil
		}
	}
	return cfg.AmbientConfig(backend)
}

// installAgents writes the embedded tiller-*.md files into agentsDir using the
// default Claude Code ambient model aliases.
func installAgents(agentsDir string, dryRun bool) ([]string, error) {
	return installAgentsWithConfig(agentsDir, dryRun, loadInstallAmbientConfig(false, "claude-code"))
}

// installAgentsWithConfig writes the embedded tiller-*.md files into agentsDir.
// If dryRun is true it returns the list of names that would be written without
// writing anything. Only tiller-* files are touched (non-tiller files are left alone).
// Returns the list of files actually written (or that would be written).
func installAgentsWithConfig(agentsDir string, dryRun bool, ambient *tier.AmbientConfig) ([]string, error) {
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
		content = renderAgentModel(e.Name(), content, ambient)

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

func renderAgentModel(name string, content []byte, ambient *tier.AmbientConfig) []byte {
	model := ambientModelForAgent(name, ambient)
	if model == "" {
		return content
	}

	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "model: ") {
			lines[i] = "model: " + model
			return []byte(strings.Join(lines, "\n"))
		}
	}
	return content
}

func ambientModelForAgent(name string, ambient *tier.AmbientConfig) string {
	if ambient == nil {
		return ""
	}
	switch name {
	case "tiller-worker.md", "tiller-debugger.md":
		return ambient.PreferredModel("execute")
	case "tiller-investigator.md", "tiller-reviewer.md":
		if model := ambient.PreferredModel("scrutiny"); model != "" {
			return model
		}
		return ambient.PreferredModel("reason")
	case "tiller-architect.md", "tiller-deep-report.md":
		return ambient.PreferredModel("reason")
	default:
		return ""
	}
}

// installCodexAgents writes the embedded tiller-*.toml custom agent files into
// agentsDir. Only embedded tiller-* files are touched.
func installCodexAgents(agentsDir string, dryRun bool) ([]string, error) {
	agentFS := agents.EmbeddedCodexDefaults()
	entries, err := fs.ReadDir(agentFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded Codex agents: %w", err)
	}

	if !dryRun {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir agents dir: %w", err)
		}
	}

	var written []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "tiller-") || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		dest := filepath.Join(agentsDir, e.Name())
		content, err := fs.ReadFile(agentFS, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}

		if existing, err := os.ReadFile(dest); err == nil {
			if string(existing) == string(content) {
				continue
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

func installOpenCodeAgents(agentsDir string, dryRun bool) ([]string, error) {
	agentFS := agents.EmbeddedOpenCodeDefaults()
	entries, err := fs.ReadDir(agentFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded OpenCode agents: %w", err)
	}

	if !dryRun {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir agents dir: %w", err)
		}
	}

	var written []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "tiller-") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		dest := filepath.Join(agentsDir, e.Name())
		content, err := fs.ReadFile(agentFS, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}

		if existing, err := os.ReadFile(dest); err == nil {
			if string(existing) == string(content) {
				continue
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

func codexConfigSnippet() string {
	return fmt.Sprintf(`[features]
multi_agent = true

[agents]
max_threads = %d
max_depth = %d
`, defaultCodexAgentMaxThreads, defaultCodexAgentMaxDepth)
}

func codexOperatingNotesSnippet() string {
	return tillerCodexNotesBegin + `
When Tiller ambient mode is active, the root Codex session is the orchestrator.
SessionStart adds this context up front; denied execution calls also point back
to this backend's lifecycle tools.
Use ` + "`.tiller/scratch/codex/`" + ` for terse shared handoff notes, reports, and
claims when useful.
Use Git/GitHub for VCS. Use Graft for coordination, work claims, shared
plans/notes, coord checks, structural inspection/review/blame when available.
Checkpoint verified wins at natural boundaries. Prefer the repo's configured
checkpoint tool when one is present; otherwise use normal Git/GitHub. Stage
explicit paths, inspect the diff, and never include unrelated dirty work.

Root Codex session:
- Read enough context yourself to route the work and make integration
  decisions. Do not spawn a subagent just to read a file, search the tree, or
  inspect ordinary context.
- Spend premium/reason-tier output on durable judgment artifacts: specs, plans,
  architecture notes, implementation docs, reviews, policy rationale,
  checkpoint decisions, and high-quality handoff briefs.
- Maintain a descriptor-backed task list. Each descriptor should look like a
  portable subagent/task packet that can be mapped to Codex, Claude Code,
  OpenCode, Cursor, or future harnesses.
- Descriptor fields: id/title, role/profile, objective, context paths,
  constraints, expected outputs, verification target, budget tier/model
  ceiling, sandbox/permission needs, dependencies/blockers, checkpoint
  criteria, and report contract.
- Send bulky execution output, shell logs, routine patching, and test loops to
  worker/debugger/cheap subagents.
- Keep root output compact; write durable docs/plans when they compound.
- Queue/background independent descriptors and continue useful orchestration.
  Wait only for descriptors that block the next integration decision. Update
  descriptors from returned reports.
- Use root read/search tools and safe read-only shell commands for lightweight
  inspection.
- Load relevant skills directly when a domain-specific workflow applies. For
  Sirena diagram work, use ` + "`using-sirena`" + `.
- Do not run implementation shell commands, build/test commands, edit source
  files, or apply patches from the root premium/reason-tier session.

Right-sizing matrix:
- root: direct reads/searches and routing decisions; no subagent needed for
  ordinary context.
- ` + "`tiller-scout`" + `: ` + "`gpt-5.4-mini`" + ` for cheap bounded reconnaissance,
  inventories, docs/log snippets, and simple summaries.
- ` + "`tiller-worker`" + `: ` + "`gpt-5.5 medium`" + ` for bounded implementation, edits,
  builds, and tests.
- ` + "`tiller-debugger`" + `: ` + "`gpt-5.5 high`" + ` for root-cause analysis plus fixes.
- ` + "`tiller-investigator`" + `/` + "`tiller-reviewer`" + `: ` + "`gpt-5.5 xhigh`" + ` read-only for deep
  tracing, adversarial review, and high-stakes verification.
- ` + "`tiller-architect`" + `/` + "`tiller-deep-report`" + `: ` + "`gpt-5.5 xhigh`" + ` for architecture,
  research synthesis, and high-consequence tradeoffs.

Codex delegation mechanics:
- Use the normal Codex multi-agent tools (` + "`spawn_agent`" + `, ` + "`wait_agent`" + `,
  ` + "`send_input`" + `, ` + "`resume_agent`" + `, ` + "`close_agent`" + `) with ` + "`agent_type`" + ` set to one of
  the ` + "`tiller-*`" + ` agents.
- Keep delegated prompts bounded. Include the concrete task, relevant paths,
  expected output, and verification target when known.
- Continue useful orchestration while agents run. When a result returns, review
  it, integrate it, and close the agent.
- Require descriptor-compatible subagent reports to cover: Outcome; files
  changed or inspected; verification commands and results; caveats or residual
  risk; checkpoint candidate yes/no; recommended next action. Use returned
  reports to update task status and checkpoint decisions. Ask subagents to
  summarize long logs and point at files/reports instead of pasting bulky
  output.
- Treat coherent verified slices as checkpoint candidates. Ask execution agents
  to report exact changed files, verification, and caveats so the checkpoint can
  be committed cleanly with the configured checkpoint tool or normal Git/GitHub.
- If a root tool call is denied by ` + "`DenyExecution`" + `, do not retry a variant of
  the same root command. Use ` + "`spawn_agent`" + ` with the appropriate ` + "`agent_type`" + `,
  then ` + "`wait_agent`" + `/` + "`close_agent`" + `.
- Tell subagents to read relevant ` + "`.tiller/scratch/codex/`" + ` notes first when
  present and write final reports or handoff notes there when useful.

Depth model:
- Root orchestrator is depth 0.
- Depth-1 agents may dispatch bounded follow-up work.
- Depth-2 agents are terminal.

Reasoning-tier work should stay focused on investigation, review, planning,
and synthesis. Route mechanical edits and command execution to execution agents.
Prefer terse, direct, explicit technical artifacts and documentation: concrete
paths, commands, diagnostics, decisions, and next actions over broad prose.
` + tillerCodexNotesEnd + "\n"
}

func codexSkillSnippet() string {
	return strings.Join([]string{
		"---",
		"name: using-tiller",
		"description: Use when Codex is running in a Tiller-enabled project and needs to follow the root-orchestrator workflow, inspect context directly with read-only tools, delegate edits/builds/tests/debugging/review to tiller-* agents, or use the temporary ambient bypass while testing the harness.",
		"---",
		"",
		"# Using Tiller",
		"",
		"When Tiller ambient mode is active, the root Codex session is the orchestrator.",
		"SessionStart makes this visible up front; DenyExecution messages use this backend's `spawn_agent`, `wait_agent`, and `close_agent` guidance.",
		"Use `.tiller/scratch/codex/` for terse shared handoff notes, reports, and claims when useful.",
		"Use Git/GitHub for VCS. Use Graft for coordination, work claims, shared plans/notes, coord checks, structural inspection/review/blame when available.",
		"Checkpoint verified wins at natural boundaries. Prefer the repo's configured checkpoint tool when one is present; otherwise use normal Git/GitHub. Stage explicit paths, inspect the diff, and never include unrelated dirty work.",
		"",
		"## Root Workflow",
		"",
		"- Read files, search the tree, inspect git state, use Hyphae recall/pulse, and load relevant skills directly from the root.",
		"- Use read-only shell commands for inspection: `rg`, `cat`, `sed -n`, `nl`, `git status`, `git diff`, `git show`, `hypha recall`, `hypha pulse`, `canopy search`, `canopy graph`, and similar non-mutating commands.",
		"- Spend premium/reason-tier output on durable judgment artifacts: specs, plans, architecture notes, implementation docs, reviews, policy rationale, checkpoint decisions, and high-quality handoff briefs.",
		"- Maintain a descriptor-backed task list. Each descriptor should look like a portable subagent/task packet that can be mapped to Codex, Claude Code, OpenCode, Cursor, or future harnesses.",
		"- Descriptor fields: id/title, role/profile, objective, context paths, constraints, expected outputs, verification target, budget tier/model ceiling, sandbox/permission needs, dependencies/blockers, checkpoint criteria, and report contract.",
		"- Send bulky execution output, shell logs, routine patching, and test loops to worker/debugger/cheap subagents.",
		"- Keep root output compact; write durable docs/plans when they compound.",
		"- Queue/background independent descriptors and continue useful orchestration. Wait only for descriptors that block the next integration decision. Update descriptors from returned reports.",
		"- Prefer terse, direct, explicit technical artifacts and documentation: concrete paths, commands, diagnostics, decisions, and next actions over broad prose.",
		"- For Sirena diagrams, `.sir` files, `sirena.yaml`, Mermaid ingestion, mdpp Sirena fences, or diagram-baking work, use `using-sirena`.",
		"- Do not edit files, apply patches, run builds/tests, or perform implementation shell work from the root reason-tier session.",
		"- After a coherent verified slice lands, surface a checkpoint candidate with explicit paths, verification, and caveats. Use the configured checkpoint tool when present; otherwise use normal Git/GitHub.",
		"- If a root command is denied by `DenyExecution`, do not retry the same work with a different shell shape. Use `spawn_agent` with the appropriate `agent_type`, then `wait_agent`/`close_agent`.",
		"- Tell subagents to read relevant `.tiller/scratch/codex/` notes first when present and write final reports or handoff notes there when useful.",
		"",
		"## Right-Sizing Matrix",
		"",
		"- root: direct reads/searches and routing decisions; no subagent needed for ordinary context.",
		"- `tiller-scout`: `gpt-5.4-mini` for cheap bounded reconnaissance, inventories, docs/log snippets, and simple summaries.",
		"- `tiller-worker`: `gpt-5.5 medium` for bounded implementation, edits, builds, and tests.",
		"- `tiller-debugger`: `gpt-5.5 high` for root-cause analysis plus fixes.",
		"- `tiller-investigator`/`tiller-reviewer`: `gpt-5.5 xhigh` read-only for deep tracing, adversarial review, and high-stakes verification.",
		"- `tiller-architect`/`tiller-deep-report`: `gpt-5.5 xhigh` for architecture, research synthesis, and high-consequence tradeoffs.",
		"",
		"## Delegation",
		"",
		"- Use `tiller-scout` for cheap, bounded read-only reconnaissance and simple summaries.",
		"- Use `tiller-worker` for implementation, file edits, builds, tests, generated files, and other execution work.",
		"- Use `tiller-debugger` for root-cause debugging plus fixes.",
		"- Use `tiller-investigator` for deep read-only tracing or claim verification.",
		"- Use `tiller-reviewer` for adversarial review.",
		"- Use `tiller-architect` and `tiller-deep-report` only for architecture, technical design, research synthesis, and high-consequence trade-off analysis.",
		"- Require descriptor-compatible subagent reports to cover: Outcome; files changed or inspected; verification commands and results; caveats or residual risk; checkpoint candidate yes/no; recommended next action.",
		"- Use returned reports to update task status and checkpoint decisions. Ask subagents to summarize long logs and point at files/reports instead of pasting bulky output.",
		"- Tell execution subagents not to own VCS commits unless explicitly asked; they should report checkpointable wins with changed files, verification, and caveats.",
		"",
		"Use normal Codex multi-agent tools: `spawn_agent`, `wait_agent`, `send_input`, `resume_agent`, and `close_agent`.",
		"",
		"## Depth",
		"",
		"- Root orchestrator is depth 0.",
		"- Depth-1 agents may dispatch bounded follow-up work.",
		"- Depth-2 agents are terminal.",
		"",
		"## Temporary Bypass",
		"",
		"While testing Tiller itself, `tiller ambient disable` creates `.tiller/ambient.disabled` and causes ambient hooks to pass through. `tiller ambient enable` removes the marker.",
		"",
	}, "\n")
}

func opencodeConfigSnippet(instructionPath string) string {
	data := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"instructions": []string{instructionPath},
	}
	out, _ := json.MarshalIndent(data, "", "  ")
	return string(out) + "\n"
}

func opencodeOperatingNotesSnippet() string {
	return tillerOpenCodeNotesBegin + `
When Tiller ambient mode is active in OpenCode, use the ` + "`tiller-orchestrator`" + `
primary agent as the root orchestrator. OpenCode's native agent permissions
provide the first layer of guardrails: the root reads/searches and routes work,
while execution and deep review happen in ` + "`tiller-*`" + ` subagents.

Use ` + "`.tiller/scratch/opencode/`" + ` for terse shared handoff notes, reports,
and claims when useful. Use Git/GitHub for VCS. Use Graft for coordination,
work claims, shared plans/notes, coord checks, structural inspection/review,
and blame when available.

Checkpoint verified wins at natural boundaries. Prefer the repo's configured
checkpoint tool when one is present; otherwise use normal Git/GitHub. Stage
explicit paths, inspect the diff, and never include unrelated dirty work.

Root OpenCode session:
- Read enough context yourself to route the work and make integration
  decisions. Do not spawn a subagent just to read a file, search the tree, or
  inspect ordinary context.
- Spend premium/reason-tier output on durable judgment artifacts: specs, plans,
  architecture notes, implementation docs, reviews, policy rationale,
  checkpoint decisions, and high-quality handoff briefs.
- Maintain a descriptor-backed task list. Each descriptor should look like a
  portable subagent/task packet that can be mapped to Codex, Claude Code,
  OpenCode, Cursor, or future harnesses.
- Descriptor fields: id/title, role/profile, objective, context paths,
  constraints, expected outputs, verification target, budget tier/model
  ceiling, sandbox/permission needs, dependencies/blockers, checkpoint
  criteria, and report contract.
- Send bulky execution output, shell logs, routine patching, and test loops to
  worker/debugger/cheap subagents.
- Keep root output compact; write durable docs/plans when they compound.
- Queue/background independent descriptors and continue useful orchestration.
  Wait only for descriptors that block the next integration decision. Update
  descriptors from returned reports.
- Use root read/search tools and safe read-only shell commands for lightweight
  inspection.
- Do not run implementation shell commands, build/test commands, edit source
  files, or apply patches from the root orchestrator.
- After a coherent verified slice lands, surface a checkpoint candidate with
  explicit paths, verification, and caveats.

Right-sizing matrix:
- ` + "`tiller-scout`" + `: cheap bounded reconnaissance, inventories, docs/log
  snippets, and simple summaries.
- ` + "`tiller-worker`" + `: bounded implementation, edits, builds, and tests.
- ` + "`tiller-debugger`" + `: root-cause analysis plus fixes.
- ` + "`tiller-investigator`" + `/` + "`tiller-reviewer`" + `: read-only deep tracing,
  adversarial review, and high-stakes verification.
- ` + "`tiller-architect`" + `/` + "`tiller-deep-report`" + `: architecture, research
  synthesis, and high-consequence tradeoffs.

Require descriptor-compatible subagent reports to cover: Outcome; files changed
or inspected; verification commands and results; caveats or residual risk;
checkpoint candidate yes/no; recommended next action. Use returned reports to
update task status and checkpoint decisions. Ask subagents to summarize long
logs and point at files/reports instead of pasting bulky output.

Prefer terse, direct, explicit technical artifacts and documentation: concrete
paths, commands, diagnostics, decisions, and next actions over broad prose.
` + tillerOpenCodeNotesEnd + "\n"
}

func installOpenCodeConfig(configPath, instructionPath string, dryRun bool) (bool, error) {
	config, missing, err := readOpenCodeConfig(configPath)
	if err != nil {
		return false, err
	}
	if missing {
		config = map[string]any{
			"$schema": "https://opencode.ai/config.json",
		}
	}

	changed, err := ensureStringListContains(config, "instructions", instructionPath)
	if err != nil {
		return false, fmt.Errorf("merge OpenCode instructions: %w", err)
	}
	if _, ok := config["$schema"]; !ok {
		config["$schema"] = "https://opencode.ai/config.json"
		changed = true
	}
	if !changed && !missing {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return false, fmt.Errorf("mkdir OpenCode config dir: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal OpenCode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", configPath, err)
	}
	return true, nil
}

func installOpenCodeNotes(notesPath string, dryRun bool) (bool, error) {
	content := opencodeOperatingNotesSnippet()
	if existing, err := os.ReadFile(notesPath); err == nil {
		if string(existing) == content {
			return false, nil
		}
	} else if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", notesPath, err)
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(notesPath), 0o755); err != nil {
		return false, fmt.Errorf("mkdir OpenCode notes dir: %w", err)
	}
	if err := os.WriteFile(notesPath, []byte(content), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", notesPath, err)
	}
	return true, nil
}

func opencodeNotesHasManagedSnippet(notesPath string) (bool, error) {
	data, err := os.ReadFile(notesPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", notesPath, err)
	}
	return string(data) == opencodeOperatingNotesSnippet(), nil
}

func opencodeConfigHasInstruction(configPath, instructionPath string) (bool, error) {
	config, missing, err := readOpenCodeConfig(configPath)
	if err != nil {
		return false, err
	}
	if missing {
		return false, nil
	}
	values, ok := config["instructions"].([]any)
	if !ok {
		return false, nil
	}
	for _, raw := range values {
		if s, ok := raw.(string); ok && s == instructionPath {
			return true, nil
		}
	}
	return false, nil
}

func removeOpenCodeConfigInstruction(configPath, instructionPath string) (bool, error) {
	config, missing, err := readOpenCodeConfig(configPath)
	if err != nil {
		return false, err
	}
	if missing {
		return false, nil
	}
	values, ok := config["instructions"].([]any)
	if !ok {
		return false, nil
	}
	next := make([]any, 0, len(values))
	removed := false
	for _, raw := range values {
		if s, ok := raw.(string); ok && s == instructionPath {
			removed = true
			continue
		}
		next = append(next, raw)
	}
	if !removed {
		return false, nil
	}
	if len(next) == 0 {
		delete(config, "instructions")
	} else {
		config["instructions"] = next
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal OpenCode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", configPath, err)
	}
	return true, nil
}

func readOpenCodeConfig(configPath string) (map[string]any, bool, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", configPath, err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", configPath, err)
	}
	if config == nil {
		config = map[string]any{}
	}
	return config, false, nil
}

func ensureStringListContains(config map[string]any, key, value string) (bool, error) {
	raw, ok := config[key]
	if !ok {
		config[key] = []string{value}
		return true, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return false, fmt.Errorf("%s must be an array", key)
	}
	for _, existing := range values {
		if s, ok := existing.(string); ok && s == value {
			return false, nil
		}
	}
	config[key] = append(values, value)
	return true, nil
}

func codexSirenaSkillSnippet() string {
	return strings.Join([]string{
		"---",
		"name: using-sirena",
		"description: Use when Codex is creating, editing, reviewing, formatting, linting, rendering, baking, or documenting Sirena diagrams, .sir files, sirena.yaml workspaces, Mermaid-to-Sirena ingestion, mdpp Sirena fences, or Sirena architecture artifacts.",
		"---",
		"",
		"# Using Sirena",
		"",
		"Sirena is a pure-Go diagram language and renderer for architecture and systems diagrams, designed as mdpp's native diagram surface.",
		"",
		"## Workflow",
		"",
		"- For non-trivial Sirena work, run `hypha recall sirena` first. Canonical spec, plan, decisions, and concepts live at `hypha://m31labs/sirena`.",
		"- Keep artifacts terse, direct, and explicit. Prefer concrete `.sir`, `.view.sir`, `sirena.yaml`, Markdown fence, SVG, command, diagnostic, and path details over broad prose.",
		"- For Sirena repositories, follow the local `AGENTS.md` commit and planning conventions.",
		"- Treat Sirena output as structurally deterministic, not pixel-equivalent to Mermaid. Sirena applies its own layout and theme.",
		"",
		"## CLI",
		"",
		"- Parse: `sirena parse --json <file>`.",
		"- Format: `sirena fmt -w <file>...` or `sirena fmt --check <file>...`.",
		"- Lint: `sirena lint <workspace-or-file>`.",
		"- Render: `sirena render [-o out.svg] [--theme earth-default] [--strict-budget] [--from mermaid|sirena] [--infer] <file>`.",
		"- Bake Markdown fences: `sirena bake [--theme earth-default] [--infer] [--check|--dry-run] <markdown-file>...`.",
		"- Emit Go package graphs: `sirena emit [--format sir|svg] [--update path] <go-dir>`.",
		"- Scaffold: `sirena new system|view <name>`.",
		"",
		"## Mermaid And Mdpp",
		"",
		"- Mermaid ingestion supports flowcharts (`graph` and `flowchart`) into Sirena IR.",
		"- Flow directions, labeled edges, common node shapes, subgraphs, and edge variants are supported structurally.",
		"- Mermaid styling and interaction directives such as `style`, `classDef`, `linkStyle`, and `click` are dropped; non-flowchart diagrams such as `sequenceDiagram`, `pie`, and `gantt` are unsupported.",
		"- In mdpp, `sirena` and `sir` fences render through a wired Sirena renderer; without that renderer they should preserve source as passthrough.",
		"",
	}, "\n")
}

func installCodexConfig(configPath string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", configPath, err)
	}
	updated := mergeCodexConfigText(string(data))
	if string(data) == updated {
		return false, nil
	}
	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			return false, fmt.Errorf("mkdir config dir: %w", err)
		}
		if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", configPath, err)
		}
	}
	return true, nil
}

func installCodexOperatingNotes(notesPath string, dryRun bool) (bool, error) {
	if notesPath == "" {
		return false, nil
	}
	data, err := os.ReadFile(notesPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", notesPath, err)
	}
	updated := mergeCodexOperatingNotesText(string(data))
	if string(data) == updated {
		return false, nil
	}
	if !dryRun {
		if err := os.WriteFile(notesPath, []byte(updated), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", notesPath, err)
		}
	}
	return true, nil
}

func installCodexSkill(skillPath, content string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(skillPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", skillPath, err)
	}
	updated := content
	if string(data) == updated {
		return false, nil
	}
	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(skillPath), err)
		}
		if err := os.WriteFile(skillPath, []byte(updated), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", skillPath, err)
		}
	}
	return true, nil
}

func codexSkillHasManagedSnippet(skillPath, content string) (bool, error) {
	data, err := os.ReadFile(skillPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", skillPath, err)
	}
	return string(data) == content, nil
}

func mergeCodexOperatingNotesText(src string) string {
	section := codexOperatingNotesSnippet()
	if strings.TrimSpace(src) == "" {
		return "# Tiller Codex Operating Notes\n\n" + section
	}

	start := strings.Index(src, tillerCodexNotesBegin)
	end := strings.Index(src, tillerCodexNotesEnd)
	if start >= 0 && end >= start {
		end += len(tillerCodexNotesEnd)
		updated := src[:start] + strings.TrimRight(section, "\n") + src[end:]
		return ensureTrailingNewline(updated)
	}

	out := ensureTrailingNewline(src)
	if strings.TrimSpace(out) != "" {
		out += "\n"
	}
	return out + section
}

func mergeCodexConfigText(src string) string {
	out := setTomlKey(src, "features", "multi_agent", "true")
	out = setTomlKey(out, "agents", "max_threads", fmt.Sprintf("%d", defaultCodexAgentMaxThreads))
	out = setTomlKey(out, "agents", "max_depth", fmt.Sprintf("%d", defaultCodexAgentMaxDepth))
	return out
}

func codexConfigHasManagedDefaults(configPath string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", configPath, err)
	}
	_, changed := removeCodexConfigDefaultsText(string(data))
	return changed, nil
}

func removeCodexConfigDefaults(configPath string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", configPath, err)
	}
	updated, changed := removeCodexConfigDefaultsText(string(data))
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", configPath, err)
	}
	return true, nil
}

func removeCodexConfigDefaultsText(src string) (string, bool) {
	out, removedThreads := removeTomlKeyValue(src, "agents", "max_threads", fmt.Sprintf("%d", defaultCodexAgentMaxThreads))
	out, removedDepth := removeTomlKeyValue(out, "agents", "max_depth", fmt.Sprintf("%d", defaultCodexAgentMaxDepth))
	return out, removedThreads || removedDepth
}

func setTomlKey(src, section, key, value string) string {
	lines := splitTomlLines(src)
	start, end := findTomlSection(lines, section)
	assignment := key + " = " + value
	if start < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", assignment)
		return strings.Join(lines, "\n") + "\n"
	}

	for i := start + 1; i < end; i++ {
		if tomlLineKey(lines[i]) == key {
			lines[i] = assignment
			return strings.Join(lines, "\n") + "\n"
		}
	}
	lines = append(lines[:end], append([]string{assignment}, lines[end:]...)...)
	return strings.Join(lines, "\n") + "\n"
}

func removeTomlKeyValue(src, section, key, value string) (string, bool) {
	lines := splitTomlLines(src)
	start, end := findTomlSection(lines, section)
	if start < 0 {
		return ensureTrailingNewline(src), false
	}
	assignment := key + " = " + value
	for i := start + 1; i < end; i++ {
		if tomlLineKey(lines[i]) == key && strings.TrimSpace(lines[i]) == assignment {
			lines = append(lines[:i], lines[i+1:]...)
			return strings.Join(lines, "\n") + "\n", true
		}
	}
	return ensureTrailingNewline(src), false
}

func splitTomlLines(src string) []string {
	if src == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(src, "\n"), "\n")
}

func findTomlSection(lines []string, section string) (start, end int) {
	header := "[" + section + "]"
	start = -1
	end = len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			if trimmed == header {
				start = i
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}
	return start, end
}

func tomlLineKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	idx := strings.IndexByte(trimmed, '=')
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(trimmed[:idx])
}

func ensureTrailingNewline(src string) string {
	if src == "" || strings.HasSuffix(src, "\n") {
		return src
	}
	return src + "\n"
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
func loadOrInitSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return settings, nil
}

// writeSettings atomically writes the settings map to path (JSON indented).
// Creates the parent directory if needed.
func writeSettings(path string, settings map[string]any) error {
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
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return false
	}
	return filepath.Base(fields[0]) == "tiller" && fields[1] == "hook"
}

// mergeHookEntries adds entry to settings under hooks.PreToolUse and
// hooks.PostToolUse if an identical command is not already present.
// Returns the list of event names actually added (for reporting).
func mergeHookEntries(settings map[string]any, entry settingsHookEntry) []string {
	return mergeHookEntriesForEvents(settings, entry, []string{"PreToolUse", "PostToolUse"})
}

func mergeHookEntriesForEvents(settings map[string]any, entry settingsHookEntry, events []string) []string {
	hooks := getOrCreateMap(settings, "hooks")
	var added []string
	for _, eventName := range events {
		if mergeHookList(hooks, eventName, entry) {
			added = append(added, eventName)
		}
	}
	settings["hooks"] = hooks
	return added
}

// mergeHookList ensures entry is present in the hook list for eventName.
// Returns true if a new entry was added.
func mergeHookList(hooks map[string]any, eventName string, entry settingsHookEntry) bool {
	raw, ok := hooks[eventName]
	var list []any
	if ok {
		list, _ = raw.([]any)
	}

	// Check if our command is already present.
	cmd := entry.Hooks[0].Command
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hooksRaw, ok := m["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooksRaw {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if existingCmd, ok := hm["command"].(string); ok &&
				(existingCmd == cmd || (hookCommandMatches(existingCmd) && hookCommandMatches(cmd))) {
				return false // already present
			}
		}
	}

	// Not present - append.
	newEntry := map[string]any{
		"matcher": entry.Matcher,
		"hooks": []any{
			settingsHookCommandMap(entry.Hooks[0]),
		},
	}
	list = append(list, newEntry)
	hooks[eventName] = list
	return true
}

func settingsHookCommandMap(h settingsHookCommand) map[string]any {
	out := map[string]any{
		"type":    h.Type,
		"command": h.Command,
	}
	if h.Timeout != 0 {
		out["timeout"] = h.Timeout
	}
	if h.StatusMessage != "" {
		out["statusMessage"] = h.StatusMessage
	}
	return out
}

// removeHookEntries removes all tiller hook entries from settings.
// Returns the list of event names from which entries were removed.
func removeHookEntries(settings map[string]any) []string {
	return removeHookEntriesForEvents(settings, []string{"PreToolUse", "PostToolUse"})
}

func removeHookEntriesForEvents(settings map[string]any, events []string) []string {
	hooksRaw, ok := settings["hooks"]
	if !ok {
		return nil
	}
	hooks, ok := hooksRaw.(map[string]any)
	if !ok {
		return nil
	}

	var removed []string
	for _, eventName := range events {
		raw, ok := hooks[eventName]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
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
func filterTillerEntries(list []any) []any {
	var out []any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		hooksRaw, ok := m["hooks"].([]any)
		if !ok {
			out = append(out, item)
			continue
		}
		hasTiller := false
		for _, h := range hooksRaw {
			hm, ok := h.(map[string]any)
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
func getOrCreateMap(parent map[string]any, key string) map[string]any {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	m := map[string]any{}
	parent[key] = m
	return m
}
