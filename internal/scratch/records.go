// Package scratch defines the provider-agnostic store interface for tiller run
// artifacts. It corresponds to spec.tiller-provider-agnostic §3.1 — the shared
// scratch bus record model.
//
// All record types here are the canonical store-level view. The fsstore
// implementation maps these onto the existing .tiller/runs/ on-disk layout so
// that v1 and v2 code produce identical artifacts during the migration.
package scratch

import (
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/procutil"
	"m31labs.dev/tiller/internal/sandbox"
)

// Run is the run-level record (spec §3.1 "manifest" row).
// Maps to manifest.json in the fsstore.
type Run struct {
	ID            string // YYYYMMDD-HHMMSS-<base36>
	Task          string // first line of task.md / brief text
	Workspace     string // absolute path to workspace root
	Status        string // created|running|completed|failed|halted
	ReasonBudget  int    // max reason dispatches; default 2 (v1 compat; was fable_budget on disk)
	MaxDepth      int    // max dispatch depth; 0 means use default (2).
	CreatedAt     time.Time
	EndedAt       *time.Time        `json:"ended_at,omitempty"`
	RootSessionID string            `json:"root_session_id,omitempty"`
	PolicySHAs    map[string]string `json:"policy_shas,omitempty"` // kind→sha256
	HyphaTraceID  string            `json:"hypha_trace_id,omitempty"`
	// StoreMode is the store kind used for this run (fs|pg|tee).
	// Written by the parent `tiller run` so children can inherit the store.
	// Empty means "fs" (default). NEVER store the DSN here.
	StoreMode string `json:"store_mode,omitempty"`
}

// Dispatch is the per-dispatch record (spec §3.1 "dispatch" row).
// Maps to dispatches/<id>/meta.json in the fsstore.
//
// Fields introduced for v2 use omitempty so that a Dispatch containing only
// v1 fields marshals byte-identical to a v1 meta.json.
type Dispatch struct {
	ID             string      `json:"id"`
	Parent         string      `json:"parent,omitempty"`
	Role           string      `json:"role"`
	Model          string      `json:"model"`
	Profile        string      `json:"profile"`
	Status         string      `json:"status"` // pending|claimed|running|completed|failed|halted|stale|denied
	Depth          int         `json:"depth"`
	SupervisorPID  int         `json:"supervisor_pid,omitempty"`
	MaxTurns       int         `json:"max_turns,omitempty"`
	TimeoutMinutes int         `json:"timeout_minutes,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	EndedAt        *time.Time  `json:"ended_at,omitempty"`
	Exit           int         `json:"exit,omitempty"`
	CostUSD        float64     `json:"cost_usd,omitempty"`
	NumTurns       int         `json:"num_turns,omitempty"`
	SessionID      string      `json:"session_id,omitempty"`
	TokenUsage     *TokenUsage `json:"token_usage,omitempty"`
	// v2 fields — omitempty so v1 meta.json stays byte-stable
	Tier        string          `json:"tier,omitempty"`        // reason|scrutiny|execute
	Enforcement string          `json:"enforcement,omitempty"` // full|degraded|sandboxed; default "full"
	Provider    string          `json:"provider,omitempty"`    // anthropic|openai|local|…
	Adapter     string          `json:"adapter,omitempty"`     // claude-headless|claude-code|…
	Sandbox     *sandbox.Record `json:"sandbox,omitempty"`
	// Dispatch pool fields (inert until P4) — omitempty for byte stability
	ClaimedBy  string     `json:"claimed_by,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	// DenyReason is set when Status=="denied" (pool-time gate denial) or
	// when a non-gate failure occurs. omitempty so v1 metas stay byte-stable.
	DenyReason string `json:"deny_reason,omitempty"`
}

const (
	AgentRunStatusRequested  = "requested"
	AgentRunStatusSpawned    = "spawned"
	AgentRunStatusRunning    = "running"
	AgentRunStatusCompleted  = "completed"
	AgentRunStatusFailed     = "failed"
	AgentRunStatusHalted     = "halted"
	AgentRunStatusLate       = "late"
	AgentRunStatusStale      = "stale"
	AgentRunStatusSuperseded = "superseded"
	AgentRunStatusClosed     = "closed"

	CheckpointStatusProposed    = "proposed"
	CheckpointStatusFresh       = "fresh"
	CheckpointStatusLateValid   = "late_valid"
	CheckpointStatusLateStale   = "late_stale"
	CheckpointStatusConflicting = "conflicting"
	CheckpointStatusAccepted    = "accepted"
	CheckpointStatusRejected    = "rejected"
)

// AgentRun records backend lifecycle metadata for an abstracted agent session.
type AgentRun struct {
	ID             string      `json:"id"`
	RunID          string      `json:"run_id,omitempty"`
	DispatchID     string      `json:"dispatch_id,omitempty"`
	Backend        string      `json:"backend"`
	BackendAgentID string      `json:"backend_agent_id,omitempty"`
	Role           string      `json:"role,omitempty"`
	Tier           string      `json:"tier,omitempty"`
	Model          string      `json:"model,omitempty"`
	Effort         string      `json:"effort,omitempty"`
	TokenUsage     *TokenUsage `json:"token_usage,omitempty"`
	ParentRunID    string      `json:"parent_run_id,omitempty"`
	ParentAgentID  string      `json:"parent_agent_id,omitempty"`
	BaseGitRev     string      `json:"base_git_rev,omitempty"`
	BaseDirtyHash  string      `json:"base_dirty_hash,omitempty"`
	ClaimedPaths   []string    `json:"claimed_paths,omitempty"`
	SpawnedAt      time.Time   `json:"spawned_at"`
	CompletedAt    *time.Time  `json:"completed_at,omitempty"`
	ReportedAt     *time.Time  `json:"reported_at,omitempty"`
	Status         string      `json:"status"`
	ChangedFiles   []string    `json:"changed_files,omitempty"`
	Verification   []string    `json:"verification,omitempty"`
	Caveats        []string    `json:"caveats,omitempty"`
	DiffHash       string      `json:"diff_hash,omitempty"`
	Summary        string      `json:"summary,omitempty"`
	Refs           []string    `json:"refs,omitempty"`
}

// CheckpointCandidate records a coherent, reviewable worktree slice.
type CheckpointCandidate struct {
	ID            string    `json:"id"`
	RunID         string    `json:"run_id,omitempty"`
	AgentRunID    string    `json:"agent_run_id,omitempty"`
	DispatchID    string    `json:"dispatch_id,omitempty"`
	Backend       string    `json:"backend,omitempty"`
	Role          string    `json:"role,omitempty"`
	Tier          string    `json:"tier,omitempty"`
	Model         string    `json:"model,omitempty"`
	Effort        string    `json:"effort,omitempty"`
	ParentRunID   string    `json:"parent_run_id,omitempty"`
	ParentAgentID string    `json:"parent_agent_id,omitempty"`
	BaseGitRev    string    `json:"base_git_rev,omitempty"`
	BaseDirtyHash string    `json:"base_dirty_hash,omitempty"`
	ClaimedPaths  []string  `json:"claimed_paths,omitempty"`
	ReportedAt    time.Time `json:"reported_at"`
	Status        string    `json:"status"`
	ChangedFiles  []string  `json:"changed_files,omitempty"`
	Verification  []string  `json:"verification,omitempty"`
	Caveats       []string  `json:"caveats,omitempty"`
	DiffHash      string    `json:"diff_hash,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	Refs          []string  `json:"refs,omitempty"`
}

// LedgerEvent records append-only lifecycle/audit facts that do not belong to
// a single dispatch trace stream.
type LedgerEvent struct {
	ID                  string      `json:"id,omitempty"`
	RunID               string      `json:"run_id,omitempty"`
	AgentRunID          string      `json:"agent_run_id,omitempty"`
	CheckpointCandidate string      `json:"checkpoint_candidate_id,omitempty"`
	DispatchID          string      `json:"dispatch_id,omitempty"`
	Backend             string      `json:"backend,omitempty"`
	Kind                string      `json:"kind"`
	Status              string      `json:"status,omitempty"`
	At                  time.Time   `json:"at"`
	TokenUsage          *TokenUsage `json:"token_usage,omitempty"`
	Summary             string      `json:"summary,omitempty"`
	Refs                []string    `json:"refs,omitempty"`
}

// TokenUsage is provider-neutral token accounting metadata for a model turn,
// dispatch, or lifecycle event. Unknown values are left zero and should be
// omitted by storing a nil *TokenUsage on the parent record.
type TokenUsage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
	TotalTokens              int64 `json:"total_tokens,omitempty"`
}

// Empty reports whether u contains no known usage counters.
func (u TokenUsage) Empty() bool {
	return u.InputTokens == 0 &&
		u.OutputTokens == 0 &&
		u.CacheCreationInputTokens == 0 &&
		u.CacheReadInputTokens == 0 &&
		u.ReasoningTokens == 0 &&
		u.TotalTokens == 0
}

// ValidAgentRunStatus reports whether status is a known AgentRun status.
func ValidAgentRunStatus(status string) bool {
	switch status {
	case AgentRunStatusRequested, AgentRunStatusSpawned, AgentRunStatusRunning,
		AgentRunStatusCompleted, AgentRunStatusFailed, AgentRunStatusHalted,
		AgentRunStatusLate, AgentRunStatusStale, AgentRunStatusSuperseded,
		AgentRunStatusClosed:
		return true
	}
	return false
}

// ValidCheckpointStatus reports whether status is a known CheckpointCandidate status.
func ValidCheckpointStatus(status string) bool {
	switch status {
	case CheckpointStatusProposed, CheckpointStatusFresh, CheckpointStatusLateValid,
		CheckpointStatusLateStale, CheckpointStatusConflicting,
		CheckpointStatusAccepted, CheckpointStatusRejected:
		return true
	}
	return false
}

// IsTerminal returns true if the dispatch status is a terminal state.
func (d *Dispatch) IsTerminal() bool {
	switch d.Status {
	case "completed", "failed", "halted", "stale", "denied":
		return true
	}
	return false
}

// IsOrphan returns true if this is a "running" dispatch whose supervisor
// process no longer exists. SupervisorPID == 0 means the PID was never
// recorded (older dispatches); those are not treated as orphans.
//
// Deprecated: prefer IsOrphanIn(runDir) which adds a cmdline identity check
// that prevents a recycled PID from being mistaken for a live supervisor.
// IsOrphan falls back to the kill-only check (no PID-reuse protection).
func (d *Dispatch) IsOrphan() bool {
	if d.Status != "running" {
		return false
	}
	if d.SupervisorPID <= 0 {
		return false
	}
	// kill -0 checks whether the process exists without sending a signal.
	err := syscall.Kill(d.SupervisorPID, 0)
	// ESRCH = no such process → orphan.
	return err == syscall.ESRCH
}

// IsOrphanIn returns true if this is a "running" dispatch whose supervisor
// process is no longer alive or cannot be verified as the correct supervisor.
// runDir is the absolute path to the run directory and is used to build the
// cmdline identity token "_supervise <runDir> <id>" that guards against PID
// reuse (a recycled PID whose owner is an unrelated process is treated as
// orphaned). SupervisorPID == 0 means the PID was never recorded; those
// dispatches are not treated as orphans.
func (d *Dispatch) IsOrphanIn(runDir string) bool {
	if d.Status != "running" {
		return false
	}
	if d.SupervisorPID <= 0 {
		return false
	}
	return !procutil.SupervisorAlive(d.SupervisorPID, runDir, d.ID)
}

// DispatchNode is a node in the dispatch tree returned by BuildDispatchTree.
// It mirrors run.Node but uses scratch.Dispatch instead of run.Meta.
type DispatchNode struct {
	Dispatch *Dispatch
	Children []*DispatchNode
}

// TraceEvent is one entry appended to dispatches/<id>/tool_trace.jsonl or
// dispatches/<id>/context_trace.jsonl (spec §3.1 "trace-event" record).
type TraceEvent struct {
	Ts           string `json:"ts"`
	Kind         string `json:"kind"` // "tool" | "read" | "dispatch" | "report"
	RunID        string `json:"run_id"`
	DispatchID   string `json:"dispatch_id"`
	Role         string `json:"role,omitempty"`
	Depth        int    `json:"depth,omitempty"`
	Tool         string `json:"tool,omitempty"`
	InputSummary string `json:"input_summary,omitempty"`
	Status       string `json:"status,omitempty"` // "ok" | "error" (tool events)
	// Dispatch-event only fields (kind:"dispatch").
	ChildID string `json:"child_id,omitempty"`
	Model   string `json:"model,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Cost / turns for report events (kind:"report").
	CostUSD  float64 `json:"cost_usd,omitempty"`
	NumTurns int     `json:"num_turns,omitempty"`
}

// NoteRef is a reference to a note document in notes/<filename>.
type NoteRef struct {
	Filename  string // relative filename inside notes/
	Author    string // role or identifier of the author
	WrittenAt time.Time
}

// RunSummary is the lightweight summary row for ListRuns.
type RunSummary struct {
	RunID         string
	Status        string
	TaskFirstLine string
	DispatchCount int
	TotalCostUSD  float64
}

// Facts holds the aggregate dispatch counters used by dispatch.arb governance.
// Subsumes run.ActiveCount + run.FableCount.
type Facts struct {
	Active      int // dispatches with status == "running"
	ReasonCount int // dispatches where model == "fable" (v1 compat) OR tier == "reason"
}
