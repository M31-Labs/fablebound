package claudecode

import (
	"context"
	"fmt"

	"m31labs.dev/tiller/internal/adapter"
)

// Adapter implements adapter.Adapter for the Claude Code interactive-session
// runtime.  Claude Code is an ambient/interactive adapter — it is not spawned
// by tiller; it installs tiller as a hook and invokes `tiller hook` on every
// tool call.
//
// Prepare and Run are therefore not meaningful for this adapter: the
// interactive session is managed externally by the user, not by tiller.
// Both methods return a descriptive error to signal mis-use.
type Adapter struct{}

// New returns a new claudecode Adapter.
func New() *Adapter { return &Adapter{} }

// Name returns the stable adapter identifier.
func (a *Adapter) Name() string { return "claude-code" }

// Enforcement returns "full": every tool call is intercepted through the
// tiller PreToolUse hook before execution.
func (a *Adapter) Enforcement() string { return "full" }

// Prepare is not supported for the interactive Claude Code adapter.
// The interactive session is managed by the user, not by tiller.
func (a *Adapter) Prepare(_ context.Context, _ *adapter.DispatchSpec) error {
	return fmt.Errorf("claude-code is the interactive ambient adapter; it is not spawnable")
}

// Run is not supported for the interactive Claude Code adapter.
// The interactive session is managed by the user, not by tiller.
func (a *Adapter) Run(_ context.Context, _ *adapter.DispatchSpec) (*adapter.Result, error) {
	return nil, fmt.Errorf("claude-code is the interactive ambient adapter; it is not spawnable")
}
