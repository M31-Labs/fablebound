package policy

import (
	"testing"
)

// dispatchLoaded caches the loaded dispatch policy for tests.
var dispatchLoaded *Loaded
var toolgateLoaded *Loaded

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
}

// --- Dispatch tests ---

func TestDispatch_FableWorker_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:        "worker",
		Model:       "fable",
		CallerRole:  "orchestrator",
		CallerDepth: 0,
		RunID:       "20260609-000000-aa01",
		FableBudget: 2,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictDeny {
		t.Errorf("verdict = %s, want Deny", res.Verdict)
	}
	if res.Rule != "DenyFableForExecution" {
		t.Errorf("rule = %q, want DenyFableForExecution", res.Rule)
	}
}

func TestDispatch_FableChiefArchitect_Allow(t *testing.T) {
	req := DispatchRequest{
		Role:        "chief-architect",
		Model:       "fable",
		CallerRole:  "orchestrator",
		CallerDepth: 0,
		RunID:       "20260609-000000-aa02",
		FableBudget: 2,
		FableCount:  0,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Errorf("verdict = %s, want Allow (rule=%s reason=%s)", res.Verdict, res.Rule, res.Reason)
	}
	if res.Route.Model != "fable" {
		t.Errorf("route.model = %q, want fable", res.Route.Model)
	}
}

func TestDispatch_Depth1_Allow_Depth2_Deny(t *testing.T) {
	// depth 1 → Allow
	req1 := DispatchRequest{
		Role:        "worker",
		CallerRole:  "orchestrator",
		CallerDepth: 1,
		RunID:       "20260609-000000-aa03",
		FableBudget: 2,
	}
	res1, err := EvalDispatch(dispatchLoaded, req1)
	if err != nil {
		t.Fatalf("EvalDispatch depth1: %v", err)
	}
	if res1.Verdict != VerdictAllow {
		t.Errorf("depth1: verdict = %s, want Allow (rule=%s)", res1.Verdict, res1.Rule)
	}

	// depth 2 → Deny DenyTerminalDepth
	req2 := DispatchRequest{
		Role:        "worker",
		CallerRole:  "worker",
		CallerDepth: 2,
		RunID:       "20260609-000000-aa04",
		FableBudget: 2,
	}
	res2, err := EvalDispatch(dispatchLoaded, req2)
	if err != nil {
		t.Fatalf("EvalDispatch depth2: %v", err)
	}
	if res2.Verdict != VerdictDeny {
		t.Errorf("depth2: verdict = %s, want Deny", res2.Verdict)
	}
	if res2.Rule != "DenyTerminalDepth" {
		t.Errorf("depth2: rule = %q, want DenyTerminalDepth", res2.Rule)
	}
}

func TestDispatch_ReviewerCaller_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:        "worker",
		CallerRole:  "reviewer",
		CallerDepth: 1,
		RunID:       "20260609-000000-aa05",
		FableBudget: 2,
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
		Role:        "worker",
		CallerRole:  "investigator",
		CallerDepth: 1,
		RunID:       "20260609-000000-aa06",
		FableBudget: 2,
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

// TestDispatch_InvestigatorRoute_Opus verifies that investigator always routes
// to opus (canonical combo; canary removed).
func TestDispatch_InvestigatorRoute_Opus(t *testing.T) {
	req := DispatchRequest{
		Role:        "investigator",
		CallerRole:  "orchestrator",
		CallerDepth: 0,
		RunID:       "20260609-000000-aa07",
		FableBudget: 2,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Fatalf("verdict = %s, want Allow (rule=%s)", res.Verdict, res.Rule)
	}
	if res.Route.Model != "opus" {
		t.Errorf("route.model = %q, want opus", res.Route.Model)
	}
}

// TestDispatch_ReviewerRoute_Opus verifies that reviewer routes to opus.
func TestDispatch_ReviewerRoute_Opus(t *testing.T) {
	req := DispatchRequest{
		Role:        "reviewer",
		CallerRole:  "orchestrator",
		CallerDepth: 0,
		RunID:       "20260609-000000-aa08",
		FableBudget: 2,
	}
	res, err := EvalDispatch(dispatchLoaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}
	if res.Verdict != VerdictAllow {
		t.Fatalf("verdict = %s, want Allow (rule=%s)", res.Verdict, res.Rule)
	}
	if res.Route.Model != "opus" {
		t.Errorf("route.model = %q, want opus", res.Route.Model)
	}
}

func TestDispatch_UnknownRole_Deny(t *testing.T) {
	req := DispatchRequest{
		Role:        "wizard",
		CallerRole:  "orchestrator",
		CallerDepth: 0,
		RunID:       "20260609-000000-aa99",
		FableBudget: 2,
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
