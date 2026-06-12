package policy

import (
	"testing"

	"m31labs.dev/tiller/internal/run"
)

// dispatchLoaded caches the loaded dispatch policy for tests.
var dispatchLoaded *Loaded
var toolgateLoaded *Loaded
var ambientNextActionLoaded *Loaded

func init() {
	var err error
	dispatchLoaded, err = Load("dispatch", "")
	if err != nil {
		panic("load dispatch policy: " + err.Error())
	}
	toolgateLoaded, err = Load("toolgate", "")
	if err != nil {
		panic("load toolgate policy: " + err.Error())
	}
	ambientNextActionLoaded, err = Load("ambient_next_action", "")
	if err != nil {
		panic("load ambient_next_action policy: " + err.Error())
	}
}

// --- Dispatch tests ---

func TestDispatch_ReasonWorker_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:         "worker",
		Tier:         "reason",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        "20260609-000000-aa01",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyReasonTierForExecution" {
		t.Errorf("rule = %q, want DenyReasonTierForExecution", res.Rule)
	}
}

func TestDispatch_ReasonChiefArchitect_Allow(t *testing.T) {
	req := DispatchRequest{
		Role:         "chief-architect",
		Tier:         "reason",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        "20260609-000000-aa02",
		ReasonBudget: 2,
		ReasonCount:  0,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Errorf("verdict = %s, want Allow (rule=%s reason=%s)", res.Verdict, res.Rule, res.Reason)
	}
	if res.Route.Tier != "reason" {
		t.Errorf("route.tier = %q, want reason", res.Route.Tier)
	}
}

func TestDispatch_Depth1_Allow_Depth2_Deny(t *testing.T) {
	// depth 1 → Allow
	req1 := DispatchRequest{
		Role:         "worker",
		CallerRole:   "orchestrator",
		CallerDepth:  1,
		RunID:        "20260609-000000-aa03",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res1, err := EvalDispatch(dispatchLoaded, req1)
	if err != nil {
		t.Fatalf("EvalDispatch depth1: %v", err)
	}
	if res1.Verdict != VerdictAllow {
		t.Errorf("depth1: verdict = %s, want Allow (rule=%s)", res1.Verdict, res1.Rule)
	}

	// depth 2 direct → Deny DenyDirectSpawnAtDepth
	req2 := DispatchRequest{
		Role:         "worker",
		CallerRole:   "worker",
		CallerDepth:  2,
		Queued:       false,
		RunID:        "20260609-000000-aa04",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res2, err := EvalDispatch(dispatchLoaded, req2)
	if err != nil {
		t.Fatalf("EvalDispatch depth2: %v", err)
	}
	if res2.Verdict != VerdictDeny {
		t.Errorf("depth2: verdict = %s, want Deny", res2.Verdict)
	}
	if res2.Rule != "DenyDirectSpawnAtDepth" {
		t.Errorf("depth2: rule = %q, want DenyDirectSpawnAtDepth", res2.Rule)
	}
}

func TestDispatch_Depth3Queued_Allow(t *testing.T) {
	// depth 3 + queued + max_depth 4 → Allow (worker dispatching investigator)
	req := DispatchRequest{
		Role:         "investigator",
		CallerRole:   "worker",
		CallerDepth:  3,
		Queued:       true,
		RunID:        "20260609-000000-ab01",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch depth3 queued: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Errorf("depth3 queued: verdict = %s, want Allow (rule=%s reason=%s)", res.Verdict, res.Rule, res.Reason)
	}
}

func TestDispatch_Depth4MaxDepth4_Deny(t *testing.T) {
	// depth 4 + queued + max_depth 4 → Deny DenyDepthBeyondPolicy
	req := DispatchRequest{
		Role:         "worker",
		CallerRole:   "worker",
		CallerDepth:  4,
		Queued:       true,
		RunID:        "20260609-000000-ab02",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch depth4 max4: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("depth4 max4: verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyDepthBeyondPolicy" {
		t.Errorf("depth4 max4: rule = %q, want DenyDepthBeyondPolicy", res.Rule)
	}
}

func TestDispatch_DefaultMaxDepth2_DenyDepth2Queued(t *testing.T) {
	req := DispatchRequest{
		Role:         "worker",
		CallerRole:   "worker",
		CallerDepth:  run.DefaultMaxDepth,
		Queued:       true,
		RunID:        "20260609-000000-ab02b",
		ReasonBudget: 2,
		MaxDepth:     run.DefaultMaxDepth,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch depth2 queued max2: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("depth2 queued max2: verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyDepthBeyondPolicy" {
		t.Errorf("depth2 queued max2: rule = %q, want DenyDepthBeyondPolicy", res.Rule)
	}
}

func TestDispatch_Depth2Direct_DenyDirectSpawnAtDepth(t *testing.T) {
	// depth 2 direct (not queued) → Deny DenyDirectSpawnAtDepth
	req := DispatchRequest{
		Role:         "worker",
		CallerRole:   "worker",
		CallerDepth:  2,
		Queued:       false,
		RunID:        "20260609-000000-ab03",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch depth2 direct: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("depth2 direct: verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyDirectSpawnAtDepth" {
		t.Errorf("depth2 direct: rule = %q, want DenyDirectSpawnAtDepth", res.Rule)
	}
}

func TestDispatch_ReviewerCaller_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:         "worker",
		CallerRole:   "reviewer",
		CallerDepth:  1,
		RunID:        "20260609-000000-aa05",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyReviewerDispatch" {
		t.Errorf("rule = %q, want DenyReviewerDispatch", res.Rule)
	}
}

func TestDispatch_InvestigatorScope_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:         "worker",
		CallerRole:   "investigator",
		CallerDepth:  1,
		RunID:        "20260609-000000-aa06",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyInvestigatorScope" {
		t.Errorf("rule = %q, want DenyInvestigatorScope", res.Rule)
	}
}

// TestDispatch_InvestigatorRoute_Scrutiny verifies that investigator always routes
// to scrutiny tier (canonical combo; canary removed).
func TestDispatch_InvestigatorRoute_Scrutiny(t *testing.T) {
	req := DispatchRequest{
		Role:         "investigator",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        "20260609-000000-aa07",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Fatalf("verdict = %s, want Allow (rule=%s)", res.Verdict, res.Rule)
	}
	if res.Route.Tier != "scrutiny" {
		t.Errorf("route.tier = %q, want scrutiny", res.Route.Tier)
	}
}

// TestDispatch_ReviewerRoute_Scrutiny verifies that reviewer routes to scrutiny tier.
func TestDispatch_ReviewerRoute_Scrutiny(t *testing.T) {
	req := DispatchRequest{
		Role:         "reviewer",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        "20260609-000000-aa08",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Fatalf("verdict = %s, want Allow (rule=%s)", res.Verdict, res.Rule)
	}
	if res.Route.Tier != "scrutiny" {
		t.Errorf("route.tier = %q, want scrutiny", res.Route.Tier)
	}
}

func TestDispatch_UnknownRole_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:         "wizard",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        "20260609-000000-aa99",
		ReasonBudget: 2,
		MaxDepth:     4,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	// Either "no match" (combinator) or AllowDispatch not matching means deny.
	t.Logf("rule=%q reason=%q", res.Rule, res.Reason)
}

// --- ToolCall tests ---

func TestToolCall_OrchestratorBashLs_Deny(t *testing.T) {
	req := ToolCallRequest{
		Role:    "orchestrator",
		Depth:   0,
		Tool:    "Bash",
		Command: "ls",
		RunID:   "20260609-000000-tc01",
	}
	res, err := EvalToolCall(toolgateLoaded, req)
	if err != nil {
		t.Fatalf("EvalToolCall: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
}

func TestToolCall_WorkerHyphaMcpServe_Deny(t *testing.T) {
	req := ToolCallRequest{
		Role:    "worker",
		Depth:   1,
		Tool:    "Bash",
		Command: "hypha mcp serve",
		RunID:   "20260609-000000-tc02",
	}
	res, err := EvalToolCall(toolgateLoaded, req)
	if err != nil {
		t.Fatalf("EvalToolCall: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
}

func TestToolCall_Depth2TillerDispatch_Deny(t *testing.T) {
	req := ToolCallRequest{
		Role:    "worker",
		Depth:   2,
		Tool:    "Bash",
		Command: "tiller dispatch --role investigator --brief x",
		RunID:   "20260609-000000-tc03",
	}
	res, err := EvalToolCall(toolgateLoaded, req)
	if err != nil {
		t.Fatalf("EvalToolCall: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyTerminalDispatch" {
		t.Errorf("rule = %q, want DenyTerminalDispatch", res.Rule)
	}
}

func TestToolCall_ReviewerWrite_ScratchFalse_Deny(t *testing.T) {
	req := ToolCallRequest{
		Role:      "reviewer",
		Depth:     1,
		Tool:      "Write",
		FilePath:  "/workspace/outside.txt",
		InScratch: false,
		RunID:     "20260609-000000-tc04",
	}
	res, err := EvalToolCall(toolgateLoaded, req)
	if err != nil {
		t.Fatalf("EvalToolCall: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
}

func TestToolCall_ReviewerWrite_ScratchTrue_Allow(t *testing.T) {
	req := ToolCallRequest{
		Role:      "reviewer",
		Depth:     1,
		Tool:      "Write",
		FilePath:  "/runs/abc/notes/review.md",
		InScratch: true,
		RunID:     "20260609-000000-tc05",
	}
	res, err := EvalToolCall(toolgateLoaded, req)
	if err != nil {
		t.Fatalf("EvalToolCall: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Errorf("verdict = %s, want Allow (rule=%s reason=%s)", res.Verdict, res.Rule, res.Reason)
	}
}

func TestAmbientNextActionDecisions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AmbientNextActionRequest)
		want   string
	}{
		{
			name: "checkpoint",
			mutate: func(req *AmbientNextActionRequest) {
				req.DistillationAvailable = true
				req.DistillationCount = 1
				req.DistillationAgeSeconds = 120
				req.DistillationStatus = "fresh"
				req.CheckpointFreshCount = 1
				req.CheckpointHasVerification = true
				req.RiskChangedFilesCount = 2
			},
			want: "checkpoint",
		},
		{
			name: "wait",
			mutate: func(req *AmbientNextActionRequest) {
				req.WorkPendingDescriptorCount = 1
				req.WorkPendingAgentCount = 1
			},
			want: "wait",
		},
		{
			name: "distill stale",
			mutate: func(req *AmbientNextActionRequest) {
				req.WorkStaleAgentCount = 1
				req.DistillationStatus = "stale"
			},
			want: "distill",
		},
		{
			name: "distill spend warn",
			mutate: func(req *AmbientNextActionRequest) {
				req.RunOutputBudgetBand = "warn"
			},
			want: "distill",
		},
		{
			name: "debug",
			mutate: func(req *AmbientNextActionRequest) {
				req.WorkFailedAgentCount = 1
			},
			want: "debug",
		},
		{
			name: "retry",
			mutate: func(req *AmbientNextActionRequest) {
				req.WorkFailedDescriptorCount = 1
			},
			want: "retry",
		},
		{
			name: "review",
			mutate: func(req *AmbientNextActionRequest) {
				req.RiskTouchesPolicy = true
				req.RiskChangedFilesCount = 1
			},
			want: "review",
		},
		{
			name: "halt",
			mutate: func(req *AmbientNextActionRequest) {
				req.RunOutputBudgetBand = "over"
				req.WorkPendingAgentCount = 1
			},
			want: "halt",
		},
		{
			name:   "proceed",
			mutate: func(req *AmbientNextActionRequest) {},
			want:   "proceed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := baselineAmbientNextActionRequest()
			tt.mutate(&req)
			res, err := EvalAmbientNextAction(ambientNextActionLoaded, req)
			if err != nil {
				t.Fatalf("EvalAmbientNextAction: %v", err)
			}
			if res.Decision.Action != tt.want {
				t.Fatalf("action = %q, want %q; decision=%+v", res.Decision.Action, tt.want, res.Decision)
			}
			if res.Decision.Confidence == 0 || res.Decision.Risk == "" || res.Decision.Reason == "" || res.Decision.Target == "" || res.Decision.BudgetPosture == "" {
				t.Fatalf("decision missing required fields: %+v", res.Decision)
			}
		})
	}
}

func baselineAmbientNextActionRequest() AmbientNextActionRequest {
	return AmbientNextActionRequest{
		RunStatus:              "running",
		RunReasonBudget:        2,
		RunOutputBudgetBand:    "ok",
		RunReasoningBudgetBand: "ok",
	}
}
