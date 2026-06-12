// Package policy provides Arbiter policy loading, schema definitions,
// context mapping, and evaluation for tiller dispatch and tool-call gates.
package policy

// DispatchRequest is the input schema for dispatch.arb.
type DispatchRequest struct {
	Role             string `arb:"dispatch.role"` // requested role
	Tier             string `arb:"dispatch.tier"` // requested tier: reason|scrutiny|execute; "" = policy default
	Background       bool   `arb:"dispatch.background"`
	BriefBytes       int    `arb:"dispatch.brief_bytes"`
	Queued           bool   `arb:"dispatch.queued"`      // true when --queue flag is set (write pending, no spawn)
	Enforcement      string `arb:"dispatch.enforcement"` // "full"|"degraded"|"sandboxed"; default "full"
	SandboxMode      string `arb:"dispatch.sandbox.mode"`
	SandboxProfile   string `arb:"dispatch.sandbox.profile"`
	HorizonManifests int    `arb:"dispatch.horizon.manifests"`
	CallerRole       string `arb:"caller.role"`        // "user" when invoked outside a run
	CallerDepth      int    `arb:"caller.depth"`       // TILLER_DEPTH of requester
	CallerID         string `arb:"caller.dispatch_id"` // TILLER_DISPATCH_ID (lineage)
	RunID            string `arb:"run.id"`
	ActiveCount      int    `arb:"run.active_dispatches"` // scan of meta.json status==running
	ReasonCount      int    `arb:"run.reason_dispatches"` // dispatches where tier == "reason"
	ReasonBudget     int    `arb:"run.reason_budget"`     // from manifest (default 2)
	MaxDepth         int    `arb:"run.max_depth"`         // max dispatch depth; manifest default 2
}

// ToolCallRequest is the input schema for toolgate.arb.
type ToolCallRequest struct {
	Role           string `arb:"agent.role"`
	Depth          int    `arb:"agent.depth"`
	DispatchID     string `arb:"agent.dispatch_id"`
	Tool           string `arb:"tool.name"`
	Command        string `arb:"tool.command"`         // Bash, else ""
	CommandClass   string `arb:"tool.command_class"`   // "readonly"|"other"; computed in Go for Bash
	FilePath       string `arb:"tool.file_path"`       // Edit/Write/Read/NotebookEdit, else ""
	InScratch      bool   `arb:"tool.path_in_scratch"` // computed in Go (§6.5)
	InWorkspace    bool   `arb:"tool.path_in_workspace"`
	AgentType      string `arb:"tool.agent_type"`       // Task/Agent: subagent_type field, "" if absent
	AgentModelTier string `arb:"tool.agent_model_tier"` // Task/Agent: "reason"|"scrutiny"|"execute"|"other"|""; computed via ambient backend config
	RunID          string `arb:"run.id"`
}

// AmbientNextActionRequest is the input schema for ambient_next_action.arb.
type AmbientNextActionRequest struct {
	RunStatus              string `arb:"run.status"`
	RunReasonBudgetUsed    int    `arb:"run.reason_budget_used"`
	RunReasonBudget        int    `arb:"run.reason_budget"`
	RunOutputBudgetBand    string `arb:"run.output_budget_band"`
	RunReasoningBudgetBand string `arb:"run.reasoning_budget_band"`

	WorkPendingDescriptorCount int `arb:"work.pending_descriptor_count"`
	WorkFailedDescriptorCount  int `arb:"work.failed_descriptor_count"`
	WorkPendingAgentCount      int `arb:"work.pending_agent_count"`
	WorkStaleAgentCount        int `arb:"work.stale_agent_count"`
	WorkFailedAgentCount       int `arb:"work.failed_agent_count"`
	WorkIterationCount         int `arb:"work.iteration_count"`

	DistillationAvailable  bool   `arb:"distillation.available"`
	DistillationCount      int    `arb:"distillation.count"`
	DistillationAgeSeconds int    `arb:"distillation.age_seconds"`
	DistillationStatus     string `arb:"distillation.status"`

	CheckpointFreshCount       int  `arb:"checkpoint.fresh_count"`
	CheckpointProposedCount    int  `arb:"checkpoint.proposed_count"`
	CheckpointLateCount        int  `arb:"checkpoint.late_count"`
	CheckpointConflictingCount int  `arb:"checkpoint.conflicting_count"`
	CheckpointHasVerification  bool `arb:"checkpoint.has_verification"`

	RiskChangedFilesCount int  `arb:"risk.changed_files_count"`
	RiskTouchesPolicy     bool `arb:"risk.touches_policy"`
	RiskTouchesSandbox    bool `arb:"risk.touches_sandbox"`
}
