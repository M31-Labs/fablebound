package scratch_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func TestRenderStatusMarkdown(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:           "20260612-100000-status",
		Task:         "implement status snapshot\nignore second line",
		Workspace:    "/workspace/tiller",
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	d1, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	if err := st.WriteDispatch(runID, &scratch.Dispatch{
		ID:         d1,
		Role:       "worker",
		Model:      "gpt-5.5",
		Status:     "completed",
		Depth:      1,
		StartedAt:  now.Add(-50 * time.Minute),
		Tier:       "execute",
		TokenUsage: &scratch.TokenUsage{InputTokens: 100, OutputTokens: 25, TotalTokens: 125},
	}); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:         "agent-001",
		Backend:    "codex",
		Role:       "worker",
		Tier:       "execute",
		Model:      "gpt-5.5",
		Status:     scratch.AgentRunStatusCompleted,
		SpawnedAt:  now.Add(-40 * time.Minute),
		Summary:    "implemented renderer",
		TokenUsage: &scratch.TokenUsage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50},
	}); err != nil {
		t.Fatalf("WriteAgentRun: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:           "cp-001",
		AgentRunID:   "agent-001",
		Status:       scratch.CheckpointStatusFresh,
		ReportedAt:   now.Add(-5 * time.Minute),
		ChangedFiles: []string{"internal/scratch/status.go"},
		Summary:      "status snapshot ready",
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:         "ledger-001",
		Backend:    "codex",
		Kind:       "codex.session_start",
		Status:     "observed",
		At:         now.Add(-30 * time.Minute),
		TokenUsage: &scratch.TokenUsage{OutputTokens: 7, TotalTokens: 7},
		Summary:    "Codex ambient session started",
		Refs:       []string{"tool:spawn_agent"},
	}); err != nil {
		t.Fatalf("AppendLedgerEvent: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:      "ambient-task-001",
		Backend: "codex",
		Kind:    "ambient.task_descriptor",
		Status:  scratch.AgentRunStatusRequested,
		At:      now.Add(-10 * time.Minute),
		Summary: "tiller-worker: implement descriptor capture",
		Refs:    []string{"tool:spawn_agent", "agent_type:tiller-worker", "descriptor_id:abc123", "objective_hash:def456"},
	}); err != nil {
		t.Fatalf("AppendLedgerEvent descriptor: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:         "ambient-distillation-001",
		AgentRunID: "agent-001",
		Backend:    "codex",
		Kind:       "ambient.distillation",
		Status:     scratch.AgentRunStatusCompleted,
		At:         now.Add(-4 * time.Minute),
		Summary:    "Renderer status is implemented; read status.md distillation before opening raw ledger logs.",
		Refs:       []string{"tool:spawn_agent", "descriptor_id:abc123", "attempt_id:attempt789"},
	}); err != nil {
		t.Fatalf("AppendLedgerEvent distillation: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now, RecentLimit: 3})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"# Tiller Run Status",
		"Generated snapshot from `manifest.json`, `dispatches/*/meta.json`, `agents/*.json`, `checkpoint_candidates.jsonl`, and `ledger.jsonl`.",
		"Updated: 2026-06-12T10:00:00Z",
		"- run_id: 20260612-100000-status",
		"- task: implement status snapshot ignore second line",
		"## Dispatches",
		"- total: 1",
		"- by_status: completed=1",
		"- by_tier: execute=1",
		"## Agents",
		"- by_status: completed=1",
		"- `agent-001` codex worker completed 2026-06-12T09:20:00Z",
		"## Task Descriptors",
		"- by_status: requested=1",
		"- `tiller-worker` codex requested 2026-06-12T09:50:00Z",
		"summary: tiller-worker: implement descriptor capture",
		"refs: tool:spawn_agent, agent_type:tiller-worker, descriptor_id:abc123, objective_hash:def456",
		"## Distillation",
		"Reusable compressed state for the orchestrator; read these entries before raw logs or transcripts.",
		"- by_status: completed=1",
		"- `codex` completed 2026-06-12T09:56:00Z",
		"summary: Renderer status is implemented; read status.md distillation before opening raw ledger logs.",
		"refs: tool:spawn_agent, descriptor_id:abc123, attempt_id:attempt789",
		"## Checkpoint Candidates",
		"- `cp-001` fresh agent-001 2026-06-12T09:55:00Z",
		"## Arbiter Next Action",
		"- action: wait",
		"- confidence: 82",
		"- risk: low",
		"- target: agents",
		"- budget_posture: ok",
		"- fallback: false",
		"## Observed Token Usage",
		"- combined_observed: input=140 output=42 cache_create=0 cache_read=0 reasoning=0 total=182",
		"## Recent Ledger Events",
		"- `codex.session_start` codex observed 2026-06-12T09:30:00Z",
		"## Next Useful Read Paths",
		"- `status.md`",
		"- `dispatches/d01/report.md`",
		"- `agents/agent-001.json`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
}

func TestRenderStatusMarkdownSpendBudgetBands(t *testing.T) {
	cases := []struct {
		name            string
		outputTokens    int64
		reasoningTokens int64
		wantOutput      string
		wantReasoning   string
	}{
		{
			name:            "ok",
			outputTokens:    40,
			reasoningTokens: 10,
			wantOutput:      "- output_budget: configured=100 percent_used=40.00% band=ok",
			wantReasoning:   "- reasoning_budget: configured=50 percent_used=20.00% band=ok",
		},
		{
			name:            "warn",
			outputTokens:    80,
			reasoningTokens: 40,
			wantOutput:      "- output_budget: configured=100 percent_used=80.00% band=warn",
			wantReasoning:   "- reasoning_budget: configured=50 percent_used=80.00% band=warn",
		},
		{
			name:            "over",
			outputTokens:    100,
			reasoningTokens: 50,
			wantOutput:      "- output_budget: configured=100 percent_used=100.00% band=over",
			wantReasoning:   "- reasoning_budget: configured=50 percent_used=100.00% band=over",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			st := fsstore.Open(base)
			now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
			runID, err := st.CreateRun(&scratch.Run{
				ID:        "20260612-120000-" + tc.name,
				Task:      "budget band",
				Workspace: "/workspace/tiller",
				Status:    "running",
				CreatedAt: now,
			})
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
				ID:      "ledger-usage-" + tc.name,
				Backend: "codex",
				Kind:    "codex.lifecycle",
				Status:  "observed",
				At:      now,
				TokenUsage: &scratch.TokenUsage{
					OutputTokens:    tc.outputTokens,
					ReasoningTokens: tc.reasoningTokens,
					TotalTokens:     tc.outputTokens + tc.reasoningTokens,
				},
			}); err != nil {
				t.Fatalf("AppendLedgerEvent: %v", err)
			}

			data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{
				UpdatedAt:            now,
				OutputTokenBudget:    100,
				ReasoningTokenBudget: 50,
				BudgetWarnRatio:      0.80,
			})
			if err != nil {
				t.Fatalf("RenderStatusMarkdown: %v", err)
			}
			got := string(data)
			for _, want := range []string{
				"## Spend Budget",
				"Advisory only: bands use observed token metadata",
				"- ledger_observed_output: " + formatInt64ForTest(tc.outputTokens),
				"- combined_observed_output: " + formatInt64ForTest(tc.outputTokens),
				"- budget_warn_ratio: 0.80",
				tc.wantOutput,
				tc.wantReasoning,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("status markdown missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestRenderStatusMarkdownTaskDescriptorsRollUpAttempts(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-110000-attempts",
		Task:      "roll up attempts",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	for i, attempt := range []string{"attempt-a", "attempt-b"} {
		if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
			ID:      "ambient-task-" + attempt,
			Backend: "codex",
			Kind:    "ambient.task_descriptor",
			Status:  scratch.AgentRunStatusRequested,
			At:      now.Add(time.Duration(i) * time.Minute),
			Summary: "tiller-worker: retry same work",
			Refs:    []string{"tool:spawn_agent", "agent_type:tiller-worker", "descriptor_id:abc123", "objective_hash:def456", "attempt_id:" + attempt},
		}); err != nil {
			t.Fatalf("AppendLedgerEvent descriptor %s: %v", attempt, err)
		}
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:         "ambient-result-attempt-b",
		AgentRunID: "codex-agent-1",
		Backend:    "codex",
		Kind:       "ambient.task_result",
		Status:     scratch.AgentRunStatusCompleted,
		At:         now.Add(2 * time.Minute),
		Summary:    "retry completed",
		Refs:       []string{"tool:spawn_agent", "agent_type:tiller-worker", "descriptor_id:abc123", "objective_hash:def456", "attempt_id:attempt-b", "backend_agent_id:backend-1"},
	}); err != nil {
		t.Fatalf("AppendLedgerEvent result: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now.Add(3 * time.Minute), RecentLimit: 5})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## Task Descriptors",
		"- total: 1",
		"- by_status: completed=1",
		"- `tiller-worker` codex completed 2026-06-12T11:01:00Z",
		"attempt_id:attempt-a",
		"attempt_id:attempt-b",
		"backend_agent_id:backend-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
}

func TestRenderStatusMarkdownStaleLateWorkPopulated(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-130000-stale-late",
		Task:      "triage stale late work",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:           "agent-late",
		Backend:      "codex",
		Role:         "worker",
		Status:       scratch.AgentRunStatusLate,
		SpawnedAt:    now.Add(-20 * time.Minute),
		Summary:      "finished after root moved on",
		ChangedFiles: []string{"internal/scratch/status.go"},
		Caveats:      []string{"needs root triage"},
	}); err != nil {
		t.Fatalf("WriteAgentRun late: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-stale",
		Backend:   "claude",
		Role:      "summary",
		Status:    scratch.AgentRunStatusStale,
		SpawnedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun stale: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-superseded",
		Backend:   "codex",
		Role:      "debugger",
		Status:    scratch.AgentRunStatusSuperseded,
		SpawnedAt: now.Add(-40 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun superseded: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-completed",
		Backend:   "codex",
		Role:      "worker",
		Status:    scratch.AgentRunStatusCompleted,
		SpawnedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun completed: %v", err)
	}

	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:           "cp-late-valid",
		AgentRunID:   "agent-late",
		Status:       scratch.CheckpointStatusLateValid,
		ReportedAt:   now.Add(-5 * time.Minute),
		Summary:      "candidate needs conflict check",
		ChangedFiles: []string{"README.md"},
		Caveats:      []string{"late but applies cleanly"},
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate late_valid: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-late-stale",
		AgentRunID: "agent-stale",
		Status:     scratch.CheckpointStatusLateStale,
		ReportedAt: now.Add(-15 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate late_stale: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-conflicting",
		AgentRunID: "agent-superseded",
		Status:     scratch.CheckpointStatusConflicting,
		ReportedAt: now.Add(-25 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate conflicting: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-fresh",
		AgentRunID: "agent-completed",
		Status:     scratch.CheckpointStatusFresh,
		ReportedAt: now.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate fresh: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now, RecentLimit: 10})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## Stale/Late Work",
		"- agents:",
		"  - total: 3",
		"  - by_status: late=1, stale=1, superseded=1",
		"    - `agent-late` codex worker late 2026-06-12T12:40:00Z",
		"      summary: finished after root moved on",
		"      changed_files: internal/scratch/status.go",
		"      caveats: needs root triage",
		"- checkpoint_candidates:",
		"  - total: 3",
		"  - by_status: conflicting=1, late_stale=1, late_valid=1",
		"    - `cp-late-valid` late_valid agent-late 2026-06-12T12:55:00Z",
		"      summary: candidate needs conflict check",
		"      changed_files: README.md",
		"      caveats: late but applies cleanly",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"    - `agent-completed`",
		"    - `cp-fresh`",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("status markdown included non-attention item %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderStatusMarkdownStaleLateWorkEmpty(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-140000-no-stale-late",
		Task:      "nothing to triage",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-ok",
		Backend:   "codex",
		Role:      "worker",
		Status:    scratch.AgentRunStatusCompleted,
		SpawnedAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-ok",
		AgentRunID: "agent-ok",
		Status:     scratch.CheckpointStatusFresh,
		ReportedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	staleLate := strings.Index(got, "## Stale/Late Work\n\n- none")
	if staleLate < 0 {
		t.Fatalf("status markdown missing empty stale/late marker:\n%s", got)
	}
	tokenUsage := strings.Index(got, "## Observed Token Usage")
	if tokenUsage < 0 {
		t.Fatalf("status markdown missing token usage section:\n%s", got)
	}
	if staleLate > tokenUsage {
		t.Fatalf("stale/late section should appear before token usage:\n%s", got)
	}
}

func TestRenderStatusMarkdownRecommendedNextActionsMultiAction(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-150000-actions",
		Task:      "derive actions",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-late",
		Backend:   "codex",
		Role:      "worker",
		Status:    scratch.AgentRunStatusLate,
		SpawnedAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun late: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-running",
		Backend:   "codex",
		Role:      "worker",
		Status:    scratch.AgentRunStatusRunning,
		SpawnedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun running: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-proposed",
		AgentRunID: "agent-running",
		Status:     scratch.CheckpointStatusProposed,
		ReportedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate proposed: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-late-valid",
		AgentRunID: "agent-late",
		Status:     scratch.CheckpointStatusLateValid,
		ReportedAt: now.Add(-15 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate late_valid: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:      "usage",
		Backend: "codex",
		Kind:    "codex.lifecycle",
		Status:  "observed",
		At:      now,
		TokenUsage: &scratch.TokenUsage{
			OutputTokens:    90,
			ReasoningTokens: 5,
			TotalTokens:     95,
		},
	}); err != nil {
		t.Fatalf("AppendLedgerEvent usage: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{
		UpdatedAt:            now,
		OutputTokenBudget:    100,
		ReasoningTokenBudget: 50,
		BudgetWarnRatio:      0.80,
	})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## Arbiter Next Action",
		"- action: distill",
		"- target: distillation",
		"## Recommended Next Actions",
		"- `triage_stale_work`: attention agents=1 checkpoint_candidates=1 ids=agent-late, cp-late-valid",
		"- `checkpoint_candidate`: review checkpoint candidates=2 ids=cp-late-valid, cp-proposed",
		"- `compact_status`: spend budget band output=warn reasoning=ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`wait_for_agents`") {
		t.Fatalf("wait_for_agents should be absorbed by higher-priority actions:\n%s", got)
	}
	staleLate := strings.Index(got, "## Stale/Late Work")
	arbiter := strings.Index(got, "## Arbiter Next Action")
	recommended := strings.Index(got, "## Recommended Next Actions")
	tokenUsage := strings.Index(got, "## Observed Token Usage")
	if staleLate < 0 || arbiter < 0 || recommended < 0 || tokenUsage < 0 || !(staleLate < arbiter && arbiter < recommended && recommended < tokenUsage) {
		t.Fatalf("arbiter and recommended actions should be between stale/late and token usage:\n%s", got)
	}
}

func TestRenderStatusMarkdownRecommendedNextActionsProceed(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-160000-proceed",
		Task:      "no action",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.WriteAgentRun(runID, &scratch.AgentRun{
		ID:        "agent-done",
		Backend:   "codex",
		Role:      "worker",
		Status:    scratch.AgentRunStatusCompleted,
		SpawnedAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("WriteAgentRun: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:         "cp-accepted",
		AgentRunID: "agent-done",
		Status:     scratch.CheckpointStatusAccepted,
		ReportedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## Recommended Next Actions",
		"- `proceed`: no stale work, checkpoint candidate, spend warning, or outstanding agent work",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"`triage_stale_work`", "`checkpoint_candidate`", "`compact_status`", "`wait_for_agents`"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("status markdown included unexpected action %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderStatusMarkdownArbiterNextActionCheckpoint(t *testing.T) {
	base := t.TempDir()
	st := fsstore.Open(base)
	now := time.Date(2026, 6, 12, 17, 0, 0, 0, time.UTC)
	runID, err := st.CreateRun(&scratch.Run{
		ID:        "20260612-170000-arbiter",
		Task:      "checkpoint action",
		Workspace: "/workspace/tiller",
		Status:    "running",
		CreatedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:           "cp-fresh",
		Status:       scratch.CheckpointStatusFresh,
		ReportedAt:   now.Add(-2 * time.Minute),
		ChangedFiles: []string{"internal/scratch/status.go"},
		Verification: []string{"go test ./internal/scratch"},
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate: %v", err)
	}

	data, err := scratch.RenderStatusMarkdown(st, runID, scratch.StatusOptions{UpdatedAt: now})
	if err != nil {
		t.Fatalf("RenderStatusMarkdown: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"## Arbiter Next Action",
		"- action: checkpoint",
		"- confidence: 90",
		"- risk: low",
		"- reason: fresh verified checkpoint candidate is ready",
		"- target: checkpoint",
		"- budget_posture: ok",
		"- fallback: false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, got)
		}
	}
}

func formatInt64ForTest(value int64) string {
	return strconv.FormatInt(value, 10)
}
