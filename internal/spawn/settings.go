// Package spawn handles Claude process lifecycle: settings generation,
// argument assembly, and supervision.
package spawn

import (
	"encoding/json"
	"fmt"
)

// hookBlock is the common PreToolUse / PostToolUse hook definition embedded in
// every generated settings file.  "tiller hook" switches on stdin's
// hook_event_name field (PreToolUse → toolgate gate; PostToolUse → trace capture).
var hookBlock = map[string]any{
	"matcher": ".*",
	"hooks": []any{
		map[string]any{
			"type":    "command",
			"command": "tiller hook",
		},
	},
}

// Settings generates a Claude Code settings JSON document for a given
// profile and depth.
//
// Profiles (normative per SPEC §4):
//
//	orchestrator — read + tiller/hypha effectors only; no Edit/Write
//	insight       — orchestrator + Edit/Write (hook confines to scratch)
//	readonly      — orchestrator + read-only Bash prefixes; no Edit/Write
//	execution     — broad: Read/Glob/Grep/Edit/Write/Bash; deny Agent/NotebookEdit
//
// Depth semantics (SPEC §3):
//
//	depth < 2  — allow Bash(tiller *)
//	depth >= 2 — replace Bash(tiller *) with Bash(tiller note *);
//	             terminal agents cannot express a dispatch.
//
// ALL profiles at ALL depths embed PreToolUse AND PostToolUse hook blocks.
func Settings(profile string, depth int) ([]byte, error) {
	perms, err := buildPermissions(profile, depth)
	if err != nil {
		return nil, err
	}

	doc := map[string]any{
		"permissions": perms,
		"hooks": map[string]any{
			"PreToolUse":  []any{hookBlock},
			"PostToolUse": []any{hookBlock},
		},
	}

	return json.MarshalIndent(doc, "", "  ")
}

// fableAllowEntries returns the Bash(tiller …) entries appropriate for the
// given depth.  At depth ≥ 2 it returns the queue-and-note-only forms:
// Depth>=2 agents may queue (--queue) but not directly spawn; mirrors
// dispatch.arb:DenyDirectSpawnAtDepth and toolgate.arb:DenyTerminalDispatch.
func fableAllowEntries(depth int) []string {
	if depth >= 2 {
		return []string{"Bash(tiller dispatch --queue *)", "Bash(tiller note *)"}
	}
	return []string{"Bash(tiller *)"}
}

// buildPermissions returns the permissions map for the given profile/depth.
func buildPermissions(profile string, depth int) (map[string]any, error) {
	fableEntries := fableAllowEntries(depth)

	switch profile {
	case "orchestrator":
		return orchestratorPerms(fableEntries), nil
	case "insight":
		return insightPerms(fableEntries), nil
	case "readonly":
		return readonlyPerms(fableEntries), nil
	case "execution":
		return executionPerms(depth), nil
	default:
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
}

// orchestratorPerms builds the permission set for the orchestrator profile.
// The orchestrator reads and dispatches; it never edits, writes, or runs
// arbitrary Bash.
func orchestratorPerms(fableEntries []string) map[string]any {
	allow := []any{"Read", "Glob", "Grep"}
	for _, e := range fableEntries {
		allow = append(allow, e)
	}
	allow = append(allow, "Bash(hypha *)")
	return map[string]any{
		"deny":  orchestratorDeny(),
		"allow": allow,
		"ask":   []any{},
	}
}

// orchestratorDeny is the exact deny list for the orchestrator (and derived
// profiles), per SPEC §4.
func orchestratorDeny() []any {
	return []any{
		"Edit",
		"Write",
		"NotebookEdit",
		"Agent",
		"WebFetch",
		"WebSearch",
	}
}

// insightPerms builds the permission set for the insight profile.
// insight = orchestrator permissions + Edit + Write (hook constrains to scratch).
func insightPerms(fableEntries []string) map[string]any {
	allow := []any{"Read", "Glob", "Grep", "Edit", "Write"}
	for _, e := range fableEntries {
		allow = append(allow, e)
	}
	allow = append(allow, "Bash(hypha *)")
	return map[string]any{
		"deny":  orchestratorDeny(),
		"allow": allow,
		"ask":   []any{},
	}
}

// readonlyPerms builds the permission set for the readonly profile.
// readonly = orchestrator permissions + read-only Bash prefixes.
func readonlyPerms(fableEntries []string) map[string]any {
	allow := []any{"Read", "Glob", "Grep"}
	for _, e := range fableEntries {
		allow = append(allow, e)
	}
	allow = append(allow,
		"Bash(hypha *)",
		"Bash(rg *)",
		"Bash(ls *)",
		"Bash(git log*)",
		"Bash(git diff*)",
		"Bash(git show*)",
		"Bash(go doc*)",
		"Bash(gts *)",
	)
	return map[string]any{
		"deny":  orchestratorDeny(),
		"allow": allow,
		"ask":   []any{},
	}
}

// executionPerms builds the permission set for the execution profile.
// execution is broad (Read/Glob/Grep/Edit/Write/Bash) with only Agent and
// NotebookEdit denied.
// At depth >= 2 (terminal), broad Bash(tiller *) is replaced with the
// queue-and-note-only allow list as a settings-layer guardrail.
// Depth>=2 agents may queue (--queue) but not directly spawn; mirrors
// dispatch.arb:DenyDirectSpawnAtDepth and toolgate.arb:DenyTerminalDispatch.
func executionPerms(depth int) map[string]any {
	deny := []any{
		"Agent",
		"NotebookEdit",
	}
	var allow []any
	if depth >= 2 {
		// Terminal workers: explicit narrow allow list — queue + note only.
		// Broad Bash(tiller dispatch*) without --queue is blocked by toolgate.
		deny = append(deny, "Bash(tiller dispatch *)")
		allow = []any{
			"Read",
			"Glob",
			"Grep",
			"Edit",
			"Write",
			"Bash",
			"Bash(tiller dispatch --queue *)",
			"Bash(tiller note *)",
		}
	} else {
		allow = []any{
			"Read",
			"Glob",
			"Grep",
			"Edit",
			"Write",
			"Bash",
		}
	}
	return map[string]any{
		"deny":  deny,
		"allow": allow,
		"ask":   []any{},
	}
}
