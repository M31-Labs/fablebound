package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"m31labs.dev/tiller/internal/policy"
)

// ClaudeArgs holds all the parameters needed to build a claude invocation.
type ClaudeArgs struct {
	// RunDir is the run scratch directory (TILLER_RUN_DIR for the child).
	RunDir string
	// DispatchID is the id being spawned (e.g. "d01").
	DispatchID string
	// Role is the agent role (e.g. "investigator").
	Role string
	// CallerDepth is the TILLER_DEPTH of the caller; child = caller+1.
	CallerDepth int
	// Route contains tier, max_turns, timeout from the policy decision.
	Route policy.Route
	// Model is the resolved model identifier (e.g. "fable", "opus", "sonnet").
	// Set by the caller from tier.Resolve; takes precedence over tier-derived model.
	Model string
	// BriefPath is the path to the brief file (passed as -p argument).
	BriefPath string
	// SettingsPath is the path to the generated settings.json for this dispatch.
	SettingsPath string
	// RolePromptPath is the path to the role's .md file.
	RolePromptPath string
}

// ClaudeBin returns the path to the claude binary, respecting TILLER_CLAUDE_BIN.
func ClaudeBin() string {
	if v := os.Getenv("TILLER_CLAUDE_BIN"); v != "" {
		return v
	}
	return "claude"
}

// BuildArgs returns the argv slice for the claude invocation.
// The brief file content is passed as -p (prompt); other args per SPEC §2.1.
//
// Argv assembled:
//
//	claude -p <brief> --model <model> --settings <settings.json>
//	       --permission-mode dontAsk --append-system-prompt <role.md>
//	       --output-format json [--max-turns N]
func BuildArgs(a ClaudeArgs) ([]string, error) {
	if a.BriefPath == "" {
		return nil, fmt.Errorf("spawn: BriefPath is required")
	}
	if a.SettingsPath == "" {
		return nil, fmt.Errorf("spawn: SettingsPath is required")
	}
	if a.Route.Tier == "" {
		return nil, fmt.Errorf("spawn: Route.Tier is required")
	}

	// Model is resolved by the caller via tier.Resolve; fall back to a
	// tier-derived default only when the dispatch record predates P2.6.
	model := a.Model
	if model == "" {
		model = tierModelFallback(a.Route.Tier)
	}

	args := []string{
		ClaudeBin(),
		"-p", a.BriefPath,
		"--model", model,
		"--settings", a.SettingsPath,
		"--permission-mode", "dontAsk",
		"--output-format", "json",
	}

	if a.RolePromptPath != "" {
		args = append(args, "--append-system-prompt", a.RolePromptPath)
	}

	if a.Route.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(a.Route.MaxTurns))
	}

	return args, nil
}

// tierModelFallback maps a tier name to a model identifier.
// Used only when reading a pre-P2.6 dispatch record that lacks a Model field;
// new records always carry Model from tier.Resolve.
func tierModelFallback(t string) string {
	switch t {
	case "reason":
		return "fable"
	case "scrutiny":
		return "opus"
	default:
		return "sonnet"
	}
}

// BuildEnv returns the environment for the spawned claude process.
// Inherits the current process environment and overlays tiller-specific vars.
func BuildEnv(a ClaudeArgs) []string {
	childDepth := a.CallerDepth + 1

	overrides := map[string]string{
		"TILLER_DEPTH":       strconv.Itoa(childDepth),
		"TILLER_RUN_DIR":     a.RunDir,
		"TILLER_DISPATCH_ID": a.DispatchID,
		"TILLER_ROLE":        a.Role,
		"TILLER_TIER":        a.Route.Tier,
	}

	// Start with the current environment, filtering out keys we override.
	overrideKeys := make(map[string]bool, len(overrides))
	for k := range overrides {
		overrideKeys[k] = true
	}

	base := os.Environ()
	env := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key := kvKey(kv)
		if !overrideKeys[key] {
			env = append(env, kv)
		}
	}

	for k, v := range overrides {
		env = append(env, k+"="+v)
	}

	return env
}

// kvKey returns the key portion of a "KEY=VALUE" environment string.
func kvKey(kv string) string {
	for i, c := range kv {
		if c == '=' {
			return kv[:i]
		}
	}
	return kv
}

// RolePromptPath returns the path to the role's prompt file under the run's
// .tiller/roles/ directory (or the workspace roles dir).
// It checks the project-local override first, then falls back to the embedded
// defaults materialized by tiller init.
func RolePromptPath(runDir, role string) string {
	if runDir == "" {
		return ""
	}
	// runDir is <workspace>/.tiller/runs/<run-id>
	// project dir is 3 levels up
	projectDir := filepath.Dir(filepath.Dir(filepath.Dir(runDir)))
	candidate := filepath.Join(projectDir, ".tiller", "roles", role+".md")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
