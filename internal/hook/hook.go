// Package hook implements fablebound's PreToolUse gate and PostToolUse trace
// capture. It is invoked by Claude Code as a hook command:
//
//	{"type": "command", "command": "fablebound hook"}
//
// stdin: Claude Code hook event JSON.
// stdout: hookSpecificOutput JSON (PreToolUse only).
// exit 0: allowed (or PostToolUse — always 0).
// exit 2: internal error (fail closed).
// Missing FABLEBOUND_ROLE: exit 0, no output (non-fablebound session).
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
	"m31labs.dev/fablebound/internal/auditlog"
	"m31labs.dev/fablebound/internal/policy"
	"m31labs.dev/fablebound/internal/run"
)

// HookEvent is the JSON stdin payload from Claude Code.
// Claude Code sends the complete hook event as a flat JSON object.
type HookEvent struct {
	HookEventName string `json:"hook_event_name"`

	// Tool identity.
	ToolName string `json:"tool_name"`

	// Tool input fields (Claude Code uses snake_case flat fields in tool_input).
	ToolInput json.RawMessage `json:"tool_input"`

	// PostToolUse additional fields.
	ToolResponse json.RawMessage `json:"tool_response"`
}

// ToolInput holds the structured input for each tool.
type ToolInput struct {
	// Bash
	Command string `json:"command"`

	// File tools (Read, Write, Edit, Glob, Grep, NotebookEdit)
	FilePath string `json:"file_path"`

	// Grep/Glob
	Pattern string `json:"pattern"`
	Query   string `json:"query"`
}

// ToolResponse holds the structured response for PostToolUse.
type ToolResponse struct {
	IsError bool   `json:"is_error"`
	Output  string `json:"output,omitempty"`
}

// PreToolOutput is the hookSpecificOutput for PreToolUse.
type PreToolOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// HookSpecificOutputWrapper wraps the output per Claude Code protocol.
type HookSpecificOutputWrapper struct {
	HookSpecificOutput PreToolOutput `json:"hookSpecificOutput"`
}

// Identity holds the agent identity derived exclusively from environment.
type Identity struct {
	Role       string
	Depth      int
	DispatchID string
	RunDir     string
	RunID      string
}

// ReadIdentity reads agent identity from environment variables.
// Returns ok=false when FABLEBOUND_ROLE is not set (non-fablebound session).
func ReadIdentity() (Identity, bool) {
	role := os.Getenv("FABLEBOUND_ROLE")
	if role == "" {
		return Identity{}, false
	}

	depth := 0
	if d := os.Getenv("FABLEBOUND_DEPTH"); d != "" {
		fmt.Sscanf(d, "%d", &depth)
	}

	dispatchID := os.Getenv("FABLEBOUND_DISPATCH_ID")
	runDir := os.Getenv("FABLEBOUND_RUN_DIR")
	runID := ""
	if runDir != "" {
		runID = filepath.Base(runDir)
	}

	return Identity{
		Role:       role,
		Depth:      depth,
		DispatchID: dispatchID,
		RunDir:     runDir,
		RunID:      runID,
	}, true
}

// verifyIdentity cross-checks the env-sourced Identity against the dispatch's
// meta.json to detect spoofed FABLEBOUND_ROLE or FABLEBOUND_DEPTH values.
// Returns an error (fail closed) if meta is missing, unreadable, or mismatches.
// The root dispatch has ID "root"; it must also exist in meta.json.
func verifyIdentity(id Identity) error {
	if id.RunDir == "" {
		// No run dir — can't verify; allow (non-run hook invocation).
		return nil
	}
	if id.DispatchID == "" {
		// No dispatch id — can't verify.
		return nil
	}

	meta, err := run.ReadMeta(id.RunDir, id.DispatchID)
	if err != nil {
		return fmt.Errorf("identity mismatch: cannot read meta for dispatch %q: %w", id.DispatchID, err)
	}

	if meta.Role != id.Role {
		return fmt.Errorf("identity mismatch: env role %q != meta role %q for dispatch %q",
			id.Role, meta.Role, id.DispatchID)
	}
	if meta.Depth != id.Depth {
		return fmt.Errorf("identity mismatch: env depth %d != meta depth %d for dispatch %q",
			id.Depth, meta.Depth, id.DispatchID)
	}
	return nil
}

// HandlePreToolUse processes a PreToolUse event.
// Returns the output JSON to write to stdout, or an error (exit 2, fail closed).
func HandlePreToolUse(id Identity, event HookEvent, workspaceDir string) ([]byte, error) {
	var input ToolInput
	if len(event.ToolInput) > 0 {
		if err := json.Unmarshal(event.ToolInput, &input); err != nil {
			return nil, fmt.Errorf("parse tool_input: %w", err)
		}
	}

	// Compute path containment facts in Go (never in policy).
	inScratch, inWorkspace := computePathFacts(input.FilePath, id.RunDir, workspaceDir)

	req := policy.ToolCallRequest{
		Role:        id.Role,
		Depth:       id.Depth,
		DispatchID:  id.DispatchID,
		Tool:        event.ToolName,
		Command:     input.Command,
		FilePath:    input.FilePath,
		InScratch:   inScratch,
		InWorkspace: inWorkspace,
		RunID:       id.RunID,
	}

	// Load toolgate policy (from run's project dir or embedded default).
	projectDir := ""
	if id.RunDir != "" {
		// Run dir is <workspace>/.fablebound/runs/<id>; project dir is three levels up.
		projectDir = filepath.Dir(filepath.Dir(filepath.Dir(id.RunDir)))
	}
	loaded, err := policy.Load("toolgate", projectDir)
	if err != nil {
		return nil, fmt.Errorf("load toolgate policy: %w", err)
	}

	// Evaluate toolgate.
	ctx := policy.ContextMap(req)
	dc := arbiter.DataFromStruct(req, loaded.Prog)
	matched, trace, err := arbiter.EvalGoverned(loaded.Prog, dc, loaded.Prog.Segments, ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate toolgate: %w", err)
	}

	verdict, ruleName, reason := policy.Decide(matched)

	// Write audit event to run's toolgate audit file.
	if id.RunDir != "" {
		if auditErr := writeAuditEvent(id.RunDir, id.DispatchID, loaded, req, matched, trace); auditErr != nil {
			// Audit failure is non-fatal for the decision but we log it.
			fmt.Fprintf(os.Stderr, "fablebound hook: audit write error: %v\n", auditErr)
		}
	}

	decision := "deny"
	switch verdict {
	case policy.VerdictAllow:
		decision = "allow"
	case policy.VerdictAsk:
		decision = "ask"
	}

	decisionReason := fmt.Sprintf("RULE: %s: %s", ruleName, reason)
	if reason == "" {
		decisionReason = fmt.Sprintf("RULE: %s", ruleName)
	}

	out := HookSpecificOutputWrapper{
		HookSpecificOutput: PreToolOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       decision,
			PermissionDecisionReason: decisionReason,
		},
	}

	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal output: %w", err)
	}
	return data, nil
}

// writeAuditEvent writes a toolgate DecisionEvent to the run's audit/toolgate.jsonl.
func writeAuditEvent(runDir, requestID string, loaded *policy.Loaded, req policy.ToolCallRequest, matched []vm.MatchedRule, trace *govern.Arbitrace) error {
	if runDir == "" {
		return nil
	}
	auditDir := filepath.Join(runDir, "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return fmt.Errorf("mkdir audit: %w", err)
	}
	sink, err := auditlog.Open(filepath.Join(auditDir, "toolgate.jsonl"))
	if err != nil {
		return err
	}
	defer sink.Close()
	return auditlog.ToolCallEvent(sink, requestID, loaded.SHA256, req, matched, trace)
}

// computePathFacts computes whether a file path is inside the run's scratch dir
// and inside the workspace, using EvalSymlinks for canonicalisation.
func computePathFacts(filePath, runDir, workspaceDir string) (inScratch, inWorkspace bool) {
	if filePath == "" {
		return false, false
	}

	canonical, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		// File may not exist yet (e.g. a Write to a new file).
		// Use Clean on the given path as a best-effort canonical form.
		canonical = filepath.Clean(filePath)
	}

	if runDir != "" {
		canonRunDir := canonPath(runDir)
		if strings.HasPrefix(canonical, canonRunDir+string(filepath.Separator)) ||
			canonical == canonRunDir {
			inScratch = true
		}
	}

	if workspaceDir != "" {
		canonWork := canonPath(workspaceDir)
		if strings.HasPrefix(canonical, canonWork+string(filepath.Separator)) ||
			canonical == canonWork {
			inWorkspace = true
		}
	}

	return inScratch, inWorkspace
}

func canonPath(p string) string {
	if c, err := filepath.EvalSymlinks(p); err == nil {
		return c
	}
	return filepath.Clean(p)
}

// Run is the main entry point for `fablebound hook`.
// Reads stdin, dispatches on hook_event_name, writes output and exits.
// On internal error it writes to stderr and returns an error (caller exits 2).
// Missing FABLEBOUND_ROLE exits 0 silently.
func Run(stdin io.Reader, stdout io.Writer, workspaceDir string) error {
	id, ok := ReadIdentity()
	if !ok {
		// Not a fablebound session; exit 0 silently.
		return nil
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	var event HookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("parse hook event: %w", err)
	}

	switch event.HookEventName {
	case "PreToolUse":
		// Verify identity against meta.json (fail closed on mismatch).
		if err := verifyIdentity(id); err != nil {
			return err
		}
		out, err := HandlePreToolUse(id, event, workspaceDir)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", out); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil

	case "PostToolUse":
		// PostToolUse always exits 0; trace failures log to stderr.
		if err := HandlePostToolUse(id, event); err != nil {
			fmt.Fprintf(os.Stderr, "fablebound hook: PostToolUse trace error: %v\n", err)
		}
		return nil

	default:
		// Unknown event type — emit a warning to stderr but still exit 0 (forward-compatible).
		fmt.Fprintf(os.Stderr, "fablebound hook: warning: unknown hook_event_name %q (ignored)\n", event.HookEventName)
		return nil
	}
}

// WorkspaceDir returns the workspace directory for path containment checks.
// It walks up from the run dir to find the workspace root (the dir containing .fablebound/).
func WorkspaceDir(runDir string) string {
	if runDir == "" {
		wd, _ := os.Getwd()
		return wd
	}
	// runDir is <workspace>/.fablebound/runs/<run-id>
	// workspace = runDir/../../../  (3 levels up)
	candidate := filepath.Dir(filepath.Dir(filepath.Dir(runDir)))
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	wd, _ := os.Getwd()
	return wd
}

// inputSummary extracts a 256-byte truncated summary of the tool input for traces.
func inputSummary(toolName string, input ToolInput) string {
	var s string
	switch toolName {
	case "Bash":
		s = input.Command
	case "Read", "Write", "Edit", "NotebookEdit":
		s = input.FilePath
	case "Glob":
		s = input.Pattern
	case "Grep":
		if input.Pattern != "" {
			s = input.Pattern
		} else {
			s = input.Query
		}
	default:
		s = input.FilePath
	}
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// toolStatus maps a ToolResponse to "ok" or "error".
func toolStatus(resp ToolResponse) string {
	if resp.IsError {
		return "error"
	}
	return "ok"
}

// isReadTool returns true for tools that produce context reads.
func isReadTool(toolName string) bool {
	switch toolName {
	case "Read", "Glob", "Grep":
		return true
	}
	return false
}
