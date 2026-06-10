// Package hook implements tiller's PreToolUse gate and PostToolUse trace
// capture. It is invoked by Claude Code as a hook command:
//
//	{"type": "command", "command": "tiller hook"}
//
// stdin: Claude Code hook event JSON.
// stdout: hookSpecificOutput JSON (PreToolUse only).
// exit 0: allowed (or PostToolUse — always 0).
// exit 2: internal error (fail closed).
// Missing TILLER_ROLE: exit 0, no output (non-tiller session).
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
	"m31labs.dev/tiller/internal/adapter/claudecode"
	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
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

	// Write: new file content.
	Content string `json:"content"`

	// Edit: replacement text (used for write-class classification).
	NewString string `json:"new_string"`

	// Grep/Glob
	Pattern string `json:"pattern"`
	Query   string `json:"query"`

	// Task / Agent: subagent dispatch fields.
	SubagentType string `json:"subagent_type"` // e.g. "tiller-worker", "general-purpose", ""
	Model        string `json:"model"`         // explicit model override; "" means inherit
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
// Returns ok=false when TILLER_ROLE is not set (non-tiller session).
func ReadIdentity() (Identity, bool) {
	role := os.Getenv("TILLER_ROLE")
	if role == "" {
		return Identity{}, false
	}

	depth := 0
	if d := os.Getenv("TILLER_DEPTH"); d != "" {
		fmt.Sscanf(d, "%d", &depth)
	}

	dispatchID := os.Getenv("TILLER_DISPATCH_ID")
	runDir := os.Getenv("TILLER_RUN_DIR")
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
// meta.json to detect spoofed TILLER_ROLE or TILLER_DEPTH values.
// Returns an error (fail closed) if meta is missing, unreadable, or mismatches.
// The root dispatch has ID "root"; it must also exist in meta.json.
//
// Run-dir containment (Fix 1): before reading any meta, this function verifies
// that TILLER_RUN_DIR is lexically contained in
// <workspace>/.tiller/runs/ where <workspace> is read from the run's own
// manifest.json — a file the sandboxed worker cannot forge because toolgate
// confines writes to the worker's own scratch (dispatches/<id>/) and the
// manifest lives one level above (in the run root).  The canonical path of
// TILLER_RUN_DIR must also resolve back to the same workspace recorded in
// the manifest, preventing symlink-based bypasses.
//
// Residual trust assumption: a worker with an *execution* profile (worker/
// debugger) has broad Bash access including the ability to write files inside
// <workspace>/.tiller/runs/<run-id>/dispatches/<id>/ (its own scratch).
// It cannot write manifest.json or other dispatches' meta.json — those paths
// are outside its scratch and denied by toolgate.  A worker that can escape
// toolgate entirely (e.g. via an unpatched Bash escape) could forge a second
// run dir; but at that point the entire sandbox has failed, not just this
// check.  The containment check here raises the bar to require a genuine
// filesystem escape rather than a simple env-var substitution.
func verifyIdentity(id Identity) error {
	if id.RunDir == "" {
		// No run dir — can't verify; allow (non-run hook invocation).
		return nil
	}
	if id.DispatchID == "" {
		// No dispatch id — can't verify.
		return nil
	}

	// ── Step 1: canonical-path containment ───────────────────────────────────
	// Resolve the claimed run dir to a canonical path (no symlinks).
	canonRunDir, err := filepath.EvalSymlinks(id.RunDir)
	if err != nil {
		// If EvalSymlinks fails (dir doesn't exist or a component is missing)
		// treat it as untrusted — fail closed.
		return fmt.Errorf("untrusted run dir: cannot resolve %q: %w", id.RunDir, err)
	}

	// Open the store and read the run record to get the authoritative workspace.
	runsBase := filepath.Dir(canonRunDir)
	runID := filepath.Base(canonRunDir)
	st := fsstore.Open(runsBase)

	runRec, runErr := st.ReadRun(runID)
	if runErr != nil {
		return fmt.Errorf("untrusted run dir: cannot read run record from %q: %w", canonRunDir, runErr)
	}
	if runRec.Workspace == "" {
		return fmt.Errorf("untrusted run dir: run record workspace is empty in %q", canonRunDir)
	}

	// Resolve the workspace from the run record to a canonical path.
	canonWorkspace, err := filepath.EvalSymlinks(runRec.Workspace)
	if err != nil {
		// Fall back to Clean if the workspace doesn't exist yet (edge case during
		// early run setup), but be strict: accept only absolute paths.
		if !filepath.IsAbs(runRec.Workspace) {
			return fmt.Errorf("untrusted run dir: run workspace %q is not absolute", runRec.Workspace)
		}
		canonWorkspace = filepath.Clean(runRec.Workspace)
	}

	// The expected runs/ prefix is <workspace>/.tiller/runs/.
	expectedRunsDir := filepath.Join(canonWorkspace, ".tiller", "runs")

	// The canonical run dir must be a direct child of expectedRunsDir (one
	// path component below) — i.e. it must start with expectedRunsDir + "/" and
	// contain no further slashes beyond the run-id segment.
	if !strings.HasPrefix(canonRunDir, expectedRunsDir+string(filepath.Separator)) {
		return fmt.Errorf("untrusted run dir: %q is not under expected runs dir %q",
			canonRunDir, expectedRunsDir)
	}
	// Ensure it's exactly one segment below (no crafted subdirectory traversal).
	suffix := canonRunDir[len(expectedRunsDir)+1:]
	if strings.ContainsRune(suffix, filepath.Separator) {
		return fmt.Errorf("untrusted run dir: %q has unexpected depth inside runs dir %q",
			canonRunDir, expectedRunsDir)
	}

	// Cross-check: the run record's workspace, when re-derived from the canonical
	// run dir, must agree (3 levels up: runs/<id> → runs → .tiller → workspace).
	derivedWorkspace := filepath.Dir(filepath.Dir(filepath.Dir(canonRunDir)))
	if derivedWorkspace != canonWorkspace {
		return fmt.Errorf("untrusted run dir: derived workspace %q != run workspace %q",
			derivedWorkspace, canonWorkspace)
	}

	// ── Step 2: role/depth cross-check against dispatch record ───────────────
	d, err := st.ReadDispatch(runID, id.DispatchID)
	if err != nil {
		return fmt.Errorf("identity mismatch: cannot read dispatch for %q: %w", id.DispatchID, err)
	}

	if d.Role != id.Role {
		return fmt.Errorf("identity mismatch: env role %q != dispatch role %q for dispatch %q",
			id.Role, d.Role, id.DispatchID)
	}
	if d.Depth != id.Depth {
		return fmt.Errorf("identity mismatch: env depth %d != dispatch depth %d for dispatch %q",
			id.Depth, d.Depth, id.DispatchID)
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
		// Run dir is <workspace>/.tiller/runs/<id>; project dir is three levels up.
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
			fmt.Fprintf(os.Stderr, "tiller hook: audit write error: %v\n", auditErr)
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
	// Use the Store to open the audit sink (creates audit dir and file if needed).
	runsBase := filepath.Dir(runDir)
	runID := filepath.Base(runDir)
	st := fsstore.Open(runsBase)
	sink, closer, err := st.AuditSink(runID, "toolgate")
	if err != nil {
		return err
	}
	defer closer.Close()
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

// HookEventFull is a superset HookEvent that also captures ambient-mode fields.
type HookEventFull struct {
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	TranscriptPath string          `json:"transcript_path"`
	AgentID        string          `json:"agent_id"`
}

// handleAmbientPreToolUse evaluates the ambient policy for a fable session.
// Returns output JSON to write to stdout, or an error.
//
// TODO(ambient): Agent tool_input does not expose target model; cannot block
// fable-for-execution subagents at the hook layer — relying on persona model
// frontmatter + deny-reason nudge to steer the orchestrator toward the right
// fb-* persona for each task type.
func handleAmbientPreToolUse(event HookEvent, stdout io.Writer) error {
	req := policy.ToolCallRequest{
		Role:  "ambient-orchestrator",
		Depth: 0,
		Tool:  event.ToolName,
	}
	// Parse tool input for file_path / command / content.
	var input ToolInput
	if len(event.ToolInput) > 0 {
		_ = json.Unmarshal(event.ToolInput, &input)
	}
	req.Command = input.Command
	req.FilePath = input.FilePath

	// Populate CommandClass for Bash calls (used by AllowPermittedBash rule,
	// which covers both "readonly" and "self-uninstall" classes).
	//
	// IsSelfUninstall takes priority: if the command is exactly
	// "tiller uninstall [--print] [--project]", classify it as "self-uninstall"
	// so AllowPermittedBash can fire.  This class is NOT "readonly" (the command
	// mutates settings.json) — it is a distinct escape-hatch class.
	// For all other commands, ClassifyCommand determines "readonly" or "other".
	if event.ToolName == "Bash" {
		if IsSelfUninstall(input.Command) {
			req.CommandClass = "self-uninstall"
		} else {
			req.CommandClass = ClassifyCommand(input.Command)
		}
	}

	// Populate AgentType and AgentModelTier for Task/Agent calls.
	// Vendor model IDs are resolved via the claudecode package to keep them confined.
	if event.ToolName == "Task" || event.ToolName == "Agent" {
		req.AgentType = input.SubagentType
		req.AgentModelTier = claudecode.ModelTier(input.Model)
	}

	loaded, err := policy.Load("ambient", "")
	if err != nil {
		return fmt.Errorf("load ambient policy: %w", err)
	}

	result, err := policy.EvalToolCall(loaded, req)
	if err != nil {
		return fmt.Errorf("eval ambient policy: %w", err)
	}

	decision := "deny"
	switch result.Verdict {
	case policy.VerdictAllow:
		decision = "allow"
	case policy.VerdictAsk:
		decision = "ask"
	}

	toolName := event.ToolName
	reason := result.Reason
	if decision == "deny" {
		// Substitute {tool.name} placeholder that the policy may embed.
		if toolName != "" {
			reason = strings.ReplaceAll(reason, "{tool.name}", toolName)
		}
	}

	decisionReason := fmt.Sprintf("RULE: %s: %s", result.Rule, reason)
	if reason == "" {
		decisionReason = fmt.Sprintf("RULE: %s", result.Rule)
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
		return fmt.Errorf("marshal ambient output: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "%s\n", data)
	return err
}

// Run is the main entry point for `tiller hook`.
// Reads stdin, dispatches on hook_event_name, writes output and exits.
// On internal error it writes to stderr and returns an error (caller exits 2).
// Missing TILLER_ROLE exits 0 silently.
func Run(stdin io.Reader, stdout io.Writer, workspaceDir string) error {
	id, ok := ReadIdentity()
	if !ok {
		// Not a managed tiller session — check ambient mode.
		return runAmbient(stdin, stdout)
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
		// Verify identity against dispatch record (fail closed on mismatch).
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
			fmt.Fprintf(os.Stderr, "tiller hook: PostToolUse trace error: %v\n", err)
		}
		return nil

	default:
		// Unknown event type — emit a warning to stderr but still exit 0 (forward-compatible).
		fmt.Fprintf(os.Stderr, "tiller hook: warning: unknown hook_event_name %q (ignored)\n", event.HookEventName)
		return nil
	}
}

// runAmbient handles the case where TILLER_ROLE is unset: ambient mode.
// For PreToolUse, it checks the transcript model and enforces orchestrator-only
// policy if and only if the model is fable. For all other events or models,
// it exits 0 (passthrough / fail open).
func runAmbient(stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		// Fail open — don't block on read error.
		return nil
	}

	// Parse just enough to detect event type, agent_id, and transcript_path.
	var full HookEventFull
	if err := json.Unmarshal(data, &full); err != nil {
		// Fail open — unparseable input.
		return nil
	}

	// Only gate PreToolUse events.
	if full.HookEventName != "PreToolUse" {
		// PostToolUse in ambient mode: no run dir, skip trace, exit 0.
		return nil
	}

	// Subagent calls carry agent_id — pass through; they are not the root model.
	if full.AgentID != "" {
		return nil
	}

	// Determine model tier from transcript (vendor model IDs confined to claudecode package).
	tier, isFable := claudecode.DetectTier(full.TranscriptPath)
	_ = tier // tier is "reason" when isFable is true; reserved for future use
	if !isFable {
		// Not fable — ambient mode is invisible.
		return nil
	}

	// D4 observe-only: when the gated tool is Task or Agent AND a run context
	// exists (TILLER_RUN_DIR resolvable), append a dispatch TraceEvent.
	// Any error is silently swallowed — NEVER affects the hook decision.
	if full.ToolName == "Task" || full.ToolName == "Agent" {
		appendAmbientDispatchTrace(full)
	}

	// Model is fable: evaluate ambient orchestrator-only policy.
	// Reconstruct a plain HookEvent from the full event.
	event := HookEvent{
		HookEventName: full.HookEventName,
		ToolName:      full.ToolName,
		ToolInput:     full.ToolInput,
		ToolResponse:  full.ToolResponse,
	}
	return handleAmbientPreToolUse(event, stdout)
}

// appendAmbientDispatchTrace appends a kind:"dispatch" TraceEvent when a
// Task/Agent tool call is observed in the ambient fable session AND a run
// context is resolvable from TILLER_RUN_DIR.
//
// D4 observe-only: this is purely informational. Errors are silently swallowed;
// this function MUST NOT affect hook decisions or break fail-open.
func appendAmbientDispatchTrace(full HookEventFull) {
	runDir := os.Getenv("TILLER_RUN_DIR")
	if runDir == "" {
		// No run context — no-op (D4 observe-only).
		return
	}
	runID := filepath.Base(runDir)
	runsBase := filepath.Dir(runDir)
	st := fsstore.Open(runsBase)

	// Best-effort input summary for the dispatch event.
	var input ToolInput
	if len(full.ToolInput) > 0 {
		_ = json.Unmarshal(full.ToolInput, &input)
	}
	summary := inputSummary(full.ToolName, input)

	ev := scratch.TraceEvent{
		Ts:           time.Now().UTC().Format(time.RFC3339Nano),
		Kind:         "dispatch",
		RunID:        runID,
		DispatchID:   "", // ambient: no dispatch ID
		Role:         "ambient-orchestrator",
		Tool:         full.ToolName,
		InputSummary: summary,
	}
	// Swallow the error — D4 observe-only, must never block.
	_ = st.AppendTraceEvent(runID, "", ev)
}

// WorkspaceDir returns the workspace directory for path containment checks.
// It walks up from the run dir to find the workspace root (the dir containing .tiller/).
func WorkspaceDir(runDir string) string {
	if runDir == "" {
		wd, _ := os.Getwd()
		return wd
	}
	// runDir is <workspace>/.tiller/runs/<run-id>
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
