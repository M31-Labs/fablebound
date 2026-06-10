// Package policy provides Arbiter policy loading, schema definitions,
// context mapping, and evaluation for fablebound dispatch and tool-call gates.
package policy

// DispatchRequest is the input schema for dispatch.arb.
type DispatchRequest struct {
	Role        string `arb:"dispatch.role"`            // requested role
	Model       string `arb:"dispatch.model"`           // requested model; "" = policy default
	Background  bool   `arb:"dispatch.background"`
	BriefBytes  int    `arb:"dispatch.brief_bytes"`
	CallerRole  string `arb:"caller.role"`              // "user" when invoked outside a run
	CallerDepth int    `arb:"caller.depth"`             // FABLEBOUND_DEPTH of requester
	CallerID    string `arb:"caller.dispatch_id"`       // FABLEBOUND_DISPATCH_ID (lineage)
	RunID       string `arb:"run.id"`
	ActiveCount int    `arb:"run.active_dispatches"`    // scan of meta.json status==running
	FableCount  int    `arb:"run.fable_dispatches"`     // dispatches where model resolved fable
	FableBudget int    `arb:"run.fable_budget"`         // from manifest (default 2)
}

// ToolCallRequest is the input schema for toolgate.arb.
type ToolCallRequest struct {
	Role        string `arb:"agent.role"`
	Depth       int    `arb:"agent.depth"`
	DispatchID  string `arb:"agent.dispatch_id"`
	Tool        string `arb:"tool.name"`
	Command     string `arb:"tool.command"`             // Bash, else ""
	FilePath    string `arb:"tool.file_path"`           // Edit/Write/Read/NotebookEdit, else ""
	InScratch   bool   `arb:"tool.path_in_scratch"`     // computed in Go (§6.5)
	InWorkspace bool   `arb:"tool.path_in_workspace"`
	RunID       string `arb:"run.id"`
}
