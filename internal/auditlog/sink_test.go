package auditlog_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/audit"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/policy"
)

// TestOpenRunSinks verifies the two sink files are created under audit/.
func TestOpenRunSinks(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rs, err := auditlog.OpenRunSinks(runDir)
	if err != nil {
		t.Fatalf("OpenRunSinks: %v", err)
	}
	defer rs.Close()

	for _, name := range []string{"dispatch.jsonl", "toolgate.jsonl"} {
		p := filepath.Join(runDir, "audit", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

// TestWriteDecisionRoundTrip verifies that a written event round-trips and
// has Kind=="rules" (required for arbiter replay).
func TestWriteDecisionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sink, err := auditlog.Open(filepath.Join(dir, "toolgate.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sink.Close()

	event := audit.DecisionEvent{
		Kind:      "rules",
		RequestID: "test-req-1",
		BundleID:  "abc123",
		Context:   map[string]any{"agent": map[string]any{"role": "worker"}},
	}
	if err := sink.WriteDecision(context.Background(), event); err != nil {
		t.Fatalf("WriteDecision: %v", err)
	}

	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	var got audit.DecisionEvent
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got.Kind != "rules" {
		t.Errorf("Kind = %q, want %q", got.Kind, "rules")
	}
	if got.RequestID != "test-req-1" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "test-req-1")
	}
}

// TestConcurrentWrites verifies that concurrent writes all land as valid JSON lines.
func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	sink, err := auditlog.Open(filepath.Join(dir, "concurrent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			ev := audit.DecisionEvent{
				Kind:      "rules",
				RequestID: fmt.Sprintf("req-%d", idx),
				BundleID:  "bundle",
			}
			if err := sink.WriteDecision(context.Background(), ev); err != nil {
				t.Errorf("WriteDecision(%d): %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lines := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var ev audit.DecisionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d: invalid JSON: %v", lines+1, err)
		}
		lines++
	}
	if lines != n {
		t.Errorf("got %d lines, want %d", lines, n)
	}
}

// TestToolCallEventAllowCase writes an allow event and verifies Kind and arbitrace.
func TestToolCallEventAllowCase(t *testing.T) {
	loaded, err := policy.Load("toolgate", "")
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}

	req := policy.ToolCallRequest{
		Role:       "worker",
		Depth:      1,
		DispatchID: "d01",
		Tool:       "Bash",
		Command:    "go build ./...",
		RunID:      "test-run",
	}

	matched, trace := evalToolGate(t, loaded, req)

	dir := t.TempDir()
	sink, err := auditlog.Open(filepath.Join(dir, "toolgate.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := auditlog.ToolCallEvent(sink, "hook-1", loaded.SHA256, req, matched, trace); err != nil {
		t.Fatalf("ToolCallEvent: %v", err)
	}

	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	var ev audit.DecisionEvent
	if err := json.Unmarshal(data[:len(data)-1], &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Kind != "rules" {
		t.Errorf("Kind = %q, want %q", ev.Kind, "rules")
	}
	if ev.BundleID != loaded.SHA256 {
		t.Errorf("BundleID = %q, want %q", ev.BundleID, loaded.SHA256)
	}
	if len(ev.Arbitrace) == 0 {
		t.Error("Arbitrace is empty; expected non-empty arbitrace")
	}
	if len(ev.Context) == 0 {
		t.Error("Context is empty")
	}
}

// TestToolCallEventDenyCase writes a deny event for an orchestrator.
func TestToolCallEventDenyCase(t *testing.T) {
	loaded, err := policy.Load("toolgate", "")
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}

	req := policy.ToolCallRequest{
		Role:       "orchestrator",
		Depth:      0,
		DispatchID: "root",
		Tool:       "Bash",
		Command:    "ls -la",
		RunID:      "test-run",
	}

	matched, trace := evalToolGate(t, loaded, req)

	dir := t.TempDir()
	sink, err := auditlog.Open(filepath.Join(dir, "toolgate.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := auditlog.ToolCallEvent(sink, "hook-deny-1", loaded.SHA256, req, matched, trace); err != nil {
		t.Fatalf("ToolCallEvent: %v", err)
	}

	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	var ev audit.DecisionEvent
	if err := json.Unmarshal(data[:len(data)-1], &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Kind != "rules" {
		t.Errorf("Kind = %q, want %q", ev.Kind, "rules")
	}
	if len(ev.Rules) == 0 {
		t.Error("Rules is empty; expected matched rules")
	}
}

// TestArbiterReplay writes toolgate events and runs arbiter replay, verifying zero diffs.
// Requires /tmp/arbiter (built from arbiter CLI). Set ARBITER_BIN to override.
func TestArbiterReplay(t *testing.T) {
	arbiterBin := os.Getenv("ARBITER_BIN")
	if arbiterBin == "" {
		if _, err := os.Stat("/tmp/arbiter"); err == nil {
			arbiterBin = "/tmp/arbiter"
		} else {
			t.Skip("arbiter binary not found; build with: cd /home/draco/work/arbiter && go build -o /tmp/arbiter ./cmd/arbiter")
		}
	}

	loaded, err := policy.Load("toolgate", "")
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}

	dir := t.TempDir()
	sinkPath := filepath.Join(dir, "toolgate.jsonl")
	sink, err := auditlog.Open(sinkPath)
	if err != nil {
		t.Fatal(err)
	}

	// Write representative events covering allow and deny cases.
	cases := []policy.ToolCallRequest{
		{Role: "worker", Depth: 1, DispatchID: "d01", Tool: "Bash", Command: "go test ./...", RunID: "run1"},
		{Role: "orchestrator", Depth: 0, DispatchID: "root", Tool: "Read", FilePath: "/workspace/main.go", RunID: "run1"},
		{Role: "orchestrator", Depth: 0, DispatchID: "root", Tool: "Bash", Command: "tiller dispatch --role investigator --brief -", RunID: "run1"},
		{Role: "investigator", Depth: 1, DispatchID: "d02", Tool: "Bash", Command: "rg TODO ./src", RunID: "run1"},
	}

	for i, req := range cases {
		matched, trace := evalToolGate(t, loaded, req)
		if err := auditlog.ToolCallEvent(sink, fmt.Sprintf("hook-%d", i), loaded.SHA256, req, matched, trace); err != nil {
			t.Fatalf("case %d ToolCallEvent: %v", i, err)
		}
	}
	sink.Close()

	// Find toolgate.arb.
	toolgatePath := ""
	for _, candidate := range []string{
		"/home/draco/work/tiller/internal/policy/defaults/toolgate.arb",
		"/home/draco/work/tiller/policy/toolgate.arb",
	} {
		if _, err := os.Stat(candidate); err == nil {
			toolgatePath = candidate
			break
		}
	}
	if toolgatePath == "" {
		t.Fatal("toolgate.arb not found")
	}

	cmd := exec.Command(arbiterBin, "replay", toolgatePath, "--audit", sinkPath)
	out, runErr := cmd.CombinedOutput()
	t.Logf("arbiter replay output:\n%s", string(out))
	if runErr != nil {
		t.Fatalf("arbiter replay exited non-zero: %v\noutput: %s", runErr, string(out))
	}

	// Verify zero diffs.
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "changed:") && line != "changed: 0" {
			t.Errorf("arbiter replay reports diffs: %s", line)
		}
	}
}

// evalToolGate runs EvalGoverned against the toolgate policy and returns matched rules + trace.
func evalToolGate(t *testing.T, loaded *policy.Loaded, req policy.ToolCallRequest) ([]vm.MatchedRule, *govern.Arbitrace) {
	t.Helper()
	ctx := policy.ContextMap(req)
	dc := arbiter.DataFromStruct(req, loaded.Prog)
	matched, trace, err := arbiter.EvalGoverned(loaded.Prog, dc, loaded.Prog.Segments, ctx)
	if err != nil {
		t.Fatalf("EvalGoverned: %v", err)
	}
	return matched, trace
}
