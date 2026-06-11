// Package claudeheadless implements the claude-headless adapter for tiller.
//
// Claude-headless drives the `claude -p` managed (headless) subprocess: it
// assembles the env identity, writes brief.md and settings.json via the Store
// seam, then spawns a detached `tiller _supervise` process and polls the
// dispatch record until a terminal state is reached.
//
// Verb mapping (spec.tiller-provider-agnostic §2.1):
//
//	present-brief   → Prepare: writes brief.md via Store.WriteBrief (already done
//	                   by dispatch.go before Prepare is called; Prepare is a no-op
//	                   for this verb when the brief is pre-written)
//	run-turn        → Run: spawns detached _supervise, polls to terminal
//	emit-report     → _supervise/Supervise: writes report.md via Store.WriteReport
//	gate-tool-call  → out-of-process: PreToolUse hook block in settings.json
//	emit-traces     → out-of-process: PostToolUse hook block in settings.json
//	request-dispatch→ tiller dispatch CLI; not an adapter method
//
// TILLER_TIER, Provider, and Model are resolved by the caller (dispatch.go via
// tier.Resolve from models.toml) before Prepare is called. The adapter treats
// spec.Tier as authoritative and does not derive it from the model string.
package claudeheadless

import (
	"context"
	"fmt"
	"os"
	"time"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/spawn"
)

// Adapter implements the claude-headless adapter: it drives `claude -p`
// (managed/headless mode) via a detached `tiller _supervise` subprocess.
//
// Construct via New; do not use the zero value directly.
type Adapter struct {
	// binary is the tiller executable path used to spawn _supervise.
	// If empty, os.Executable() is called at Run time.
	binary string
}

// New returns a new claude-headless Adapter. binary is the path to the tiller
// executable for spawning _supervise; pass "" to resolve via os.Executable at
// Run time.
func New(binary string) *Adapter {
	return &Adapter{binary: binary}
}

// Name returns "claude-headless".
func (a *Adapter) Name() string { return "claude-headless" }

// Enforcement returns "full": every tool call is intercepted by the
// PreToolUse hook block installed by Prepare.
func (a *Adapter) Enforcement() string { return "full" }

// Prepare materialises the dispatch for execution. It writes settings.json via
// spec.Store.WriteAdapterConfig, with BOTH PreToolUse and PostToolUse tiller
// hook blocks, using the child depth (spec.Depth) for permission-profile selection.
//
// spec.Tier, spec.Provider, and spec.Model are expected to be set by the caller
// (dispatch.go via tier.Resolve) before Prepare is called.
//
// The brief.md is written by dispatch.go before Prepare is called; Prepare
// does not re-write it.
//
// Prepare is idempotent: re-calling it on the same spec overwrites settings.json
// with the same content and leaves the dispatch record unchanged.
func (a *Adapter) Prepare(ctx context.Context, s *adapter.DispatchSpec) error {
	// Determine the profile for settings generation. Fall back to "orchestrator"
	// if Profile is empty (should not happen in practice; dispatch.go always sets it).
	profile := s.Profile
	if profile == "" {
		profile = "orchestrator"
	}

	// Generate settings.json using the child depth.
	settingsJSON, err := spawn.Settings(profile, s.Depth)
	if err != nil {
		return fmt.Errorf("claudeheadless: generate settings: %w", err)
	}

	if err := s.Store.WriteAdapterConfig(s.RunID, s.DispatchID, settingsJSON); err != nil {
		return fmt.Errorf("claudeheadless: write settings.json: %w", err)
	}

	return nil
}

// Run spawns a detached `tiller _supervise` subprocess and polls the dispatch
// record until it reaches a terminal state (run-turn and emit-report verbs).
//
// Polling uses an exponential back-off starting at 200ms, capped at 2s, with
// an overall cap from spec.Timeout (0 = no timeout; caller uses its own timeout).
//
// Returns (*Result, nil) on success. If the adapter itself cannot proceed
// (binary not found, spawn error), returns (nil, err).
func (a *Adapter) Run(ctx context.Context, s *adapter.DispatchSpec) (*adapter.Result, error) {
	binary := a.binary
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("claudeheadless: find executable: %w", err)
		}
	}

	// runDir is derived from the store's on-disk layout.
	// fsstore roots at runsBase; dispatch records live at runsBase/runID/dispatches/dID/.
	// We need runDir = runsBase/runID; RunDir on the spec provides this.
	runDir := s.WorkDir // caller sets WorkDir to the run directory
	if runDir == "" {
		return nil, fmt.Errorf("claudeheadless: WorkDir (run dir) is required")
	}

	if err := spawn.SpawnDetached(binary, runDir, s.DispatchID); err != nil {
		return nil, fmt.Errorf("claudeheadless: spawn _supervise: %w", err)
	}

	// Poll until terminal.
	result, err := pollToTerminal(ctx, s)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// pollToTerminal polls the dispatch record until it reaches a terminal state.
// ctx cancellation aborts the poll; if spec.Timeout > 0 the poll also has an
// internal deadline (but dispatch.go / cli layer manages the user-facing timeout).
func pollToTerminal(ctx context.Context, s *adapter.DispatchSpec) (*adapter.Result, error) {
	const baseInterval = 200 * time.Millisecond
	const maxInterval = 2 * time.Second

	interval := baseInterval

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		d, err := s.Store.ReadDispatch(s.RunID, s.DispatchID)
		if err == nil && d.IsTerminal() {
			return &adapter.Result{
				Status:    d.Status,
				CostUSD:   d.CostUSD,
				NumTurns:  d.NumTurns,
				SessionID: d.SessionID,
			}, nil
		}

		time.Sleep(interval)
		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}
