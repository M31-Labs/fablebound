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
)

// Run is the run-level record (spec §3.1 "manifest" row).
// Maps to manifest.json in the fsstore.
type Run struct {
	ID            string // YYYYMMDD-HHMMSS-<base36>
	Task          string // first line of task.md / brief text
	Workspace     string // absolute path to workspace root
	Status        string // created|running|completed|failed|halted
	ReasonBudget  int    // max reason dispatches; default 2 (v1 compat; was fable_budget on disk)
	MaxDepth      int    // max dispatch depth; 0 means use default (4). spec §4.3
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
	ID             string     `json:"id"`
	Parent         string     `json:"parent,omitempty"`
	Role           string     `json:"role"`
	Model          string     `json:"model"`
	Profile        string     `json:"profile"`
	Status         string     `json:"status"` // pending|claimed|running|completed|failed|halted|stale|denied
	Depth          int        `json:"depth"`
	SupervisorPID  int        `json:"supervisor_pid,omitempty"`
	MaxTurns       int        `json:"max_turns,omitempty"`
	TimeoutMinutes int        `json:"timeout_minutes,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	Exit           int        `json:"exit,omitempty"`
	CostUSD        float64    `json:"cost_usd,omitempty"`
	NumTurns       int        `json:"num_turns,omitempty"`
	SessionID      string     `json:"session_id,omitempty"`
	// v2 fields — omitempty so v1 meta.json stays byte-stable
	Tier        string `json:"tier,omitempty"`        // reason|scrutiny|execute
	Enforcement string `json:"enforcement,omitempty"` // full|degraded; default "full"
	Provider    string `json:"provider,omitempty"`    // anthropic|openai|local|…
	Adapter     string `json:"adapter,omitempty"`     // claude-headless|claude-code|…
	// Dispatch pool fields (inert until P4) — omitempty for byte stability
	ClaimedBy  string     `json:"claimed_by,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	// DenyReason is set when Status=="denied" (pool-time gate denial) or
	// when a non-gate failure occurs. omitempty so v1 metas stay byte-stable.
	DenyReason string `json:"deny_reason,omitempty"`
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
