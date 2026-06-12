package spawn

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseClaudeResult_StubFormat verifies backward-compat with the stub / legacy
// single-object format: {"type":"result","cost_usd":0.001,...}
func TestParseClaudeResult_StubFormat(t *testing.T) {
	input := []byte(`{"type":"result","result":"stub report","cost_usd":0.001,"num_turns":1,"session_id":"stub-session-99","is_error":false}` + "\n")

	cr, err := parseClaudeResult(input)
	if err != nil {
		t.Fatalf("parseClaudeResult stub format: %v", err)
	}
	if cr.CostUSD != 0.001 {
		t.Errorf("CostUSD = %f, want 0.001", cr.CostUSD)
	}
	if cr.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", cr.NumTurns)
	}
	if cr.SessionID != "stub-session-99" {
		t.Errorf("SessionID = %q, want stub-session-99", cr.SessionID)
	}
	if cr.Result != "stub report" {
		t.Errorf("Result = %q, want stub report", cr.Result)
	}
	if cr.IsError {
		t.Error("IsError = true, want false")
	}
}

// TestParseClaudeResult_RealFormat verifies parsing of the claude 2.1.172 JSON array
// format where the cost field is "total_cost_usd" (not "cost_usd") and the output
// is a JSON array of event objects, with the result record last.
func TestParseClaudeResult_RealFormat(t *testing.T) {
	// Load fixture verbatim from testdata — this is representative of real
	// claude 2.1.172 --output-format json output (array shape, total_cost_usd).
	fixturePath := filepath.Join("testdata", "transcripts", "claude-2.1.172-real.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	cr, err := parseClaudeResult(data)
	if err != nil {
		t.Fatalf("parseClaudeResult real format: %v", err)
	}

	// Ground truth from fixture: total_cost_usd=0.419658, num_turns=3,
	// session_id="4c7c7d2f-0000-0000-0000-000000000001"
	const wantCost = 0.419658
	if cr.CostUSD != wantCost {
		t.Errorf("CostUSD = %f, want %f (total_cost_usd from result record)", cr.CostUSD, wantCost)
	}
	if cr.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", cr.NumTurns)
	}
	if cr.SessionID != "4c7c7d2f-0000-0000-0000-000000000001" {
		t.Errorf("SessionID = %q, want 4c7c7d2f-0000-0000-0000-000000000001", cr.SessionID)
	}
	if cr.Result == "" {
		t.Error("Result is empty, want non-empty text")
	}
	if cr.IsError {
		t.Error("IsError = true, want false")
	}
	if cr.TokenUsage == nil {
		t.Fatal("TokenUsage is nil, want parsed usage")
	}
	if cr.TokenUsage.InputTokens != 5000 {
		t.Errorf("InputTokens = %d, want 5000", cr.TokenUsage.InputTokens)
	}
	if cr.TokenUsage.OutputTokens != 800 {
		t.Errorf("OutputTokens = %d, want 800", cr.TokenUsage.OutputTokens)
	}
	if cr.TokenUsage.CacheCreationInputTokens != 10000 {
		t.Errorf("CacheCreationInputTokens = %d, want 10000", cr.TokenUsage.CacheCreationInputTokens)
	}
	if cr.TokenUsage.CacheReadInputTokens != 2000 {
		t.Errorf("CacheReadInputTokens = %d, want 2000", cr.TokenUsage.CacheReadInputTokens)
	}
}

// TestParseClaudeResult_RealFormat_Inline verifies the real format inline
// (does not depend on reading the fixture file) to make the bug obvious.
func TestParseClaudeResult_RealFormat_Inline(t *testing.T) {
	// Minimal real-format array with total_cost_usd in the result record.
	// This is the shape that claude 2.1.172 emits with --output-format json.
	input := []byte(`[{"type":"system","session_id":"sess-abc","subtype":"init"},` +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]},"session_id":"sess-abc"},` +
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"done","session_id":"sess-abc","total_cost_usd":1.164776}]` + "\n")

	cr, err := parseClaudeResult(input)
	if err != nil {
		t.Fatalf("parseClaudeResult real inline: %v", err)
	}
	if cr.CostUSD != 1.164776 {
		t.Errorf("CostUSD = %f, want 1.164776", cr.CostUSD)
	}
	if cr.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", cr.NumTurns)
	}
	if cr.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want sess-abc", cr.SessionID)
	}
}

func TestParseClaudeResult_LegacyUsage(t *testing.T) {
	input := []byte(`{"type":"result","result":"stub report","cost_usd":0.001,"num_turns":1,"session_id":"stub-session-99","is_error":false,"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}` + "\n")

	cr, err := parseClaudeResult(input)
	if err != nil {
		t.Fatalf("parseClaudeResult legacy usage: %v", err)
	}
	if cr.TokenUsage == nil {
		t.Fatal("TokenUsage is nil, want parsed usage")
	}
	if cr.TokenUsage.InputTokens != 12 || cr.TokenUsage.OutputTokens != 34 || cr.TokenUsage.TotalTokens != 46 {
		t.Fatalf("TokenUsage mismatch: %#v", cr.TokenUsage)
	}
}
