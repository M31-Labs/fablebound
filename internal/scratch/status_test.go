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
		"## Checkpoint Candidates",
		"- `cp-001` fresh agent-001 2026-06-12T09:55:00Z",
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

func formatInt64ForTest(value int64) string {
	return strconv.FormatInt(value, 10)
}
