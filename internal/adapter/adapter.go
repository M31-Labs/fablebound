// Package adapter defines the L3 provider-adapter seam for tiller
// (spec.tiller-provider-agnostic §2.1).
//
// The seam maps the six normative verbs onto Go constructs as follows:
//
//	Verb               → Where it lives
//	─────────────────────────────────────────────────────────────────────
//	present-brief      → Adapter.Prepare (materialises brief.md, writes
//	                       settings.json with both tiller hook blocks,
//	                       computes env identity: TILLER_* env vars
//	                       including TILLER_TIER)
//	run-turn           → Adapter.Run (drives agent until terminal;
//	                       writes report via DispatchSpec.Store)
//	emit-report        → Adapter.Run (writes report record via
//	                       DispatchSpec.Store.WriteReport before returning)
//	gate-tool-call     → out-of-process contract: the PreToolUse hook
//	                       block installed into settings.json by Prepare
//	                       invokes `tiller hook` — toolgate logic lives
//	                       in internal/hook and internal/policy, not here
//	emit-traces        → out-of-process contract: the PostToolUse hook
//	                       block installed by Prepare invokes `tiller hook`
//	                       which appends TraceEvents via the Store seam
//	request-dispatch   → tiller dispatch CLI surface; not an adapter method
//
// Adapter identity (role, depth, dispatch id, tier) always comes from the
// adapter's own channel — env vars at spawn or loop-local state — never from
// model output (spec §2.1).
package adapter

import (
	"context"
	"time"

	"m31labs.dev/tiller/internal/sandbox"
	"m31labs.dev/tiller/internal/scratch"
)

// DispatchSpec carries all the information a provider adapter needs to prepare
// and run a single dispatch. It is the in-process shape defined in
// spec.tiller-provider-agnostic §2.1.
//
// Store is the single-writer scratch bus (spec §3.1). All artifact writes
// (brief, report, trace events, adapter config) MUST go through Store; the
// adapter must not write dispatch artifacts by other means.
type DispatchSpec struct {
	Store scratch.Store

	RunID      string
	DispatchID string
	Role       string
	Tier       string          // reason|scrutiny|execute (spec §2.2)
	Provider   string          // anthropic|openai|local|…
	Model      string          // provider-specific model identifier
	Profile    string          // settings / toolgate class
	WorkDir    string          // absolute path to the workspace root
	Sandbox    *sandbox.Record // requested/active runtime isolation metadata, if any

	Depth    int
	MaxTurns int
	Timeout  time.Duration
}

// Result is the terminal outcome of a single dispatch as returned by
// Adapter.Run. It carries only what a supervisor needs to finalise the
// dispatch record in the scratch bus (mirroring internal/spawn/supervise.go's
// finalization logic).
type Result struct {
	// Status is the terminal dispatch status: completed|failed|halted.
	Status string

	// CostUSD is the total cost of the dispatch in US dollars (0 if unknown).
	CostUSD float64

	// NumTurns is the number of turns the agent ran (0 if unknown).
	NumTurns int

	// SessionID is the provider-assigned session identifier (empty if none).
	SessionID string
}

// Adapter binds one agent runtime to the shared scratch bus.
// It MUST implement the six verbs of the provider adapter seam
// (spec.tiller-provider-agnostic §2.1); verbs that are out-of-process
// contracts (gate-tool-call, emit-traces) are fulfilled by hook blocks
// installed during Prepare, not by Adapter methods.
//
// Enforcement classifies the adapter's ability to enforce the full
// gate-tool-call contract (spec §5.1):
//
//	"full"     — every tool call is intercepted and routed through toolgate
//	             before execution (e.g. PreToolUse hook in Claude Code adapters)
//	"degraded" — tool-call gating is best-effort or unavailable for this
//	             runtime; the limitation is recorded on the dispatch record
//	"sandboxed" — tool-call gating is unavailable or partial, but the dispatch
//	              is wrapped by active runtime isolation recorded in Sandbox
type Adapter interface {
	// Name returns the stable adapter identifier used for registration and
	// dispatch routing (e.g. "claude-headless", "claude-code", "command").
	Name() string

	// Enforcement returns the gate-enforcement level for this adapter:
	// "full", "degraded", or "sandboxed" (spec §5.1 plus sandbox extension).
	Enforcement() string

	// Prepare materialises the dispatch for execution. It MUST:
	//   - deliver the brief to the agent runtime (present-brief verb);
	//   - write the adapter config (settings.json) into the dispatch directory
	//     via DispatchSpec.Store.WriteAdapterConfig, including BOTH tiller
	//     hook blocks (PreToolUse gate-tool-call, PostToolUse emit-traces)
	//     for Claude-family adapters;
	//   - compute the env identity for the child process, setting all
	//     TILLER_* environment variables (TILLER_RUN_ID, TILLER_DISPATCH_ID,
	//     TILLER_ROLE, TILLER_TIER, TILLER_DEPTH, …).
	//
	// Prepare is idempotent; calling it twice on the same DispatchSpec
	// must not corrupt the dispatch.
	Prepare(ctx context.Context, s *DispatchSpec) error

	// Run drives the agent until it reaches a terminal state (run-turn and
	// emit-report verbs). It MUST:
	//   - execute the agent according to the adapter's runtime model;
	//   - write the final output as a report record via
	//     DispatchSpec.Store.WriteReport before returning;
	//   - return a non-nil *Result on success, even if the agent failed.
	//
	// Run returns (nil, err) only when the adapter itself cannot proceed
	// (e.g. binary not found, I/O error). Agent-level failures (non-zero
	// exit, timeout, IsError output) are encoded in Result.Status.
	Run(ctx context.Context, s *DispatchSpec) (*Result, error)
}
