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
var hookBlock = map[string]interface{}{
	"matcher": ".*",
	"hooks": []interface{}{
		map[string]interface{}{
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

	doc := map[string]interface{}{
		"permissions": perms,
		"hooks": map[string]interface{}{
			"PreToolUse":  []interface{}{hookBlock},
			"PostToolUse": []interface{}{hookBlock},
		},
	}

	return json.MarshalIndent(doc, "", "  ")
}

// fableAllowEntry returns the Bash(tiller …) entry appropriate for the
// given depth.  At depth ≥ 2 it returns the terminal-scoped form that only
// permits `tiller note *`.
func fableAllowEntry(depth int) string {
	if depth >= 2 {
		return "Bash(tiller note *)"
	}
	return "Bash(tiller *)"
}

// buildPermissions returns the permissions map for the given profile/depth.
func buildPermissions(profile string, depth int) (map[string]interface{}, error) {
	fableEntry := fableAllowEntry(depth)

	switch profile {
	case "orchestrator":
		return orchestratorPerms(fableEntry), nil
	case "insight":
		return insightPerms(fableEntry), nil
	case "readonly":
		return readonlyPerms(fableEntry), nil
	case "execution":
		return executionPerms(fableEntry), nil
	default:
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
}

// orchestratorPerms builds the permission set for the orchestrator profile.
// The orchestrator reads and dispatches; it never edits, writes, or runs
// arbitrary Bash.
func orchestratorPerms(fableEntry string) map[string]interface{} {
	return map[string]interface{}{
		"deny": orchestratorDeny(),
		"allow": []interface{}{
			"Read",
			"Glob",
			"Grep",
			fableEntry,
			"Bash(hypha *)",
		},
		"ask": []interface{}{},
	}
}

// orchestratorDeny is the exact deny list for the orchestrator (and derived
// profiles), per SPEC §4.
func orchestratorDeny() []interface{} {
	return []interface{}{
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
func insightPerms(fableEntry string) map[string]interface{} {
	return map[string]interface{}{
		"deny": orchestratorDeny(),
		"allow": []interface{}{
			"Read",
			"Glob",
			"Grep",
			"Edit",
			"Write",
			fableEntry,
			"Bash(hypha *)",
		},
		"ask": []interface{}{},
	}
}

// readonlyPerms builds the permission set for the readonly profile.
// readonly = orchestrator permissions + read-only Bash prefixes.
func readonlyPerms(fableEntry string) map[string]interface{} {
	return map[string]interface{}{
		"deny": orchestratorDeny(),
		"allow": []interface{}{
			"Read",
			"Glob",
			"Grep",
			fableEntry,
			"Bash(hypha *)",
			"Bash(rg *)",
			"Bash(ls *)",
			"Bash(git log*)",
			"Bash(git diff*)",
			"Bash(git show*)",
			"Bash(go doc*)",
			"Bash(gts *)",
		},
		"ask": []interface{}{},
	}
}

// executionPerms builds the permission set for the execution profile.
// execution is broad (Read/Glob/Grep/Edit/Write/Bash) with only Agent and
// NotebookEdit denied.
// At depth >= 2 (terminal), Bash(tiller dispatch*) is added to the deny
// list as a settings-layer guardrail. The toolgate policy also enforces
// DenyTerminalDispatch, but defence-in-depth means the settings layer should
// be explicit. Broad Bash is still allowed so terminal workers can execute
// arbitrary commands — they just cannot spawn child dispatches.
func executionPerms(fableEntry string) map[string]interface{} {
	_ = fableEntry // included via broad Bash below; kept for hook clarity
	deny := []interface{}{
		"Agent",
		"NotebookEdit",
	}
	if fableEntry == "Bash(tiller note *)" {
		// depth >= 2: add settings-layer dispatch deny as defence-in-depth
		deny = append(deny, "Bash(tiller dispatch*)")
	}
	return map[string]interface{}{
		"deny": deny,
		"allow": []interface{}{
			"Read",
			"Glob",
			"Grep",
			"Edit",
			"Write",
			"Bash",
		},
		"ask": []interface{}{},
	}
}
