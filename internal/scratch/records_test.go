package scratch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLifecycleStatusValidation(t *testing.T) {
	for _, status := range []string{
		AgentRunStatusRequested,
		AgentRunStatusSpawned,
		AgentRunStatusRunning,
		AgentRunStatusCompleted,
		AgentRunStatusFailed,
		AgentRunStatusHalted,
		AgentRunStatusLate,
		AgentRunStatusStale,
		AgentRunStatusSuperseded,
		AgentRunStatusClosed,
	} {
		if !ValidAgentRunStatus(status) {
			t.Fatalf("%q agent status should be valid", status)
		}
	}
	if ValidAgentRunStatus("done") {
		t.Fatal("unknown agent status should be invalid")
	}
	for _, status := range []string{
		CheckpointStatusProposed,
		CheckpointStatusFresh,
		CheckpointStatusLateValid,
		CheckpointStatusLateStale,
		CheckpointStatusConflicting,
		CheckpointStatusAccepted,
		CheckpointStatusRejected,
	} {
		if !ValidCheckpointStatus(status) {
			t.Fatalf("%q checkpoint status should be valid", status)
		}
	}
	if ValidCheckpointStatus("candidate") {
		t.Fatal("unknown checkpoint status should be invalid")
	}
}

func TestLifecycleRecordsJSONRoundtrip(t *testing.T) {
	reportedAt := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	completedAt := reportedAt.Add(-time.Minute)
	ar := AgentRun{
		ID:             "agent-001",
		RunID:          "run-001",
		DispatchID:     "d01",
		Backend:        "codex",
		BackendAgentID: "backend-001",
		Role:           "worker",
		Tier:           "execute",
		Model:          "gpt-5.5",
		Effort:         "medium",
		TokenUsage:     &TokenUsage{InputTokens: 100, OutputTokens: 25, ReasoningTokens: 7},
		ParentRunID:    "parent-run",
		ParentAgentID:  "parent-agent",
		BaseGitRev:     "abc123",
		BaseDirtyHash:  "dirty456",
		ClaimedPaths:   []string{"internal/scratch"},
		SpawnedAt:      reportedAt.Add(-2 * time.Minute),
		CompletedAt:    &completedAt,
		ReportedAt:     &reportedAt,
		Status:         AgentRunStatusCompleted,
		ChangedFiles:   []string{"internal/scratch/records.go"},
		Verification:   []string{"go test ./internal/scratch"},
		Caveats:        []string{"pgstore skipped without dsn"},
		DiffHash:       "diff789",
		Summary:        "added lifecycle records",
		Refs:           []string{"dispatches/d01/report.md"},
	}

	data, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("Marshal AgentRun: %v", err)
	}
	var got AgentRun
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal AgentRun: %v", err)
	}
	if got.ID != ar.ID || got.BaseGitRev != ar.BaseGitRev || got.Status != ar.Status {
		t.Fatalf("AgentRun mismatch: got %#v want %#v", got, ar)
	}
	if got.TokenUsage == nil || got.TokenUsage.InputTokens != 100 || got.TokenUsage.OutputTokens != 25 {
		t.Fatalf("AgentRun token usage mismatch: got %#v", got.TokenUsage)
	}

	candidate := CheckpointCandidate{
		ID:            "cp-001",
		RunID:         ar.RunID,
		AgentRunID:    ar.ID,
		BaseGitRev:    ar.BaseGitRev,
		BaseDirtyHash: ar.BaseDirtyHash,
		ReportedAt:    reportedAt,
		Status:        CheckpointStatusProposed,
		ChangedFiles:  ar.ChangedFiles,
		Verification:  ar.Verification,
		Summary:       ar.Summary,
	}
	data, err = json.Marshal(candidate)
	if err != nil {
		t.Fatalf("Marshal CheckpointCandidate: %v", err)
	}
	var gotCandidate CheckpointCandidate
	if err := json.Unmarshal(data, &gotCandidate); err != nil {
		t.Fatalf("Unmarshal CheckpointCandidate: %v", err)
	}
	if gotCandidate.ID != candidate.ID || gotCandidate.BaseDirtyHash != candidate.BaseDirtyHash {
		t.Fatalf("CheckpointCandidate mismatch: got %#v want %#v", gotCandidate, candidate)
	}

	event := LedgerEvent{
		ID:                  "event-001",
		RunID:               ar.RunID,
		AgentRunID:          ar.ID,
		CheckpointCandidate: candidate.ID,
		Kind:                "checkpoint_candidate",
		At:                  reportedAt,
		TokenUsage:          &TokenUsage{OutputTokens: 9},
		Summary:             "candidate reported",
		Refs:                []string{"checkpoint_candidates.jsonl"},
	}
	data, err = json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal LedgerEvent: %v", err)
	}
	var gotEvent LedgerEvent
	if err := json.Unmarshal(data, &gotEvent); err != nil {
		t.Fatalf("Unmarshal LedgerEvent: %v", err)
	}
	if gotEvent.ID != event.ID || gotEvent.CheckpointCandidate != event.CheckpointCandidate {
		t.Fatalf("LedgerEvent mismatch: got %#v want %#v", gotEvent, event)
	}
	if gotEvent.TokenUsage == nil || gotEvent.TokenUsage.OutputTokens != 9 {
		t.Fatalf("LedgerEvent token usage mismatch: got %#v", gotEvent.TokenUsage)
	}
}

func TestTokenUsageOmittedWhenUnknown(t *testing.T) {
	data, err := json.Marshal(Dispatch{ID: "d01", Role: "worker"})
	if err != nil {
		t.Fatalf("Marshal Dispatch: %v", err)
	}
	if string(data) == "" {
		t.Fatal("empty json")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw dispatch: %v", err)
	}
	if _, ok := raw["token_usage"]; ok {
		t.Fatalf("token_usage should be omitted when unknown: %s", data)
	}

	u := TokenUsage{}
	if !u.Empty() {
		t.Fatal("zero TokenUsage should be empty")
	}
	u.OutputTokens = 1
	if u.Empty() {
		t.Fatal("non-zero TokenUsage should not be empty")
	}
}
