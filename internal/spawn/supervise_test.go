package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// TestTrimOutput verifies trimOutput correctly finds the first line whose first
// non-space character is '{'.
func TestTrimOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name:  "noisy prefix line before JSON",
			input: "some log line\n" + `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name: "noisy prefix line containing { mid-line",
			// The critical bug fix: a line like "building {thing}" should NOT be
			// treated as the JSON line even though it contains '{'.
			input: "building {thing} at 12:00\n" + `{"type":"result","result":"ok"}` + "\n",
			want:  `{"type":"result","result":"ok"}` + "\n",
		},
		{
			name:  "leading whitespace before {",
			input: "  \t  \n" + `{"type":"result"}` + "\n",
			want:  `{"type":"result"}` + "\n",
		},
		{
			name:  "multiple noisy lines then JSON",
			input: "line 1\nline 2 has {curly}\nline3\n" + `{"type":"result","cost_usd":0.01}` + "\n",
			want:  `{"type":"result","cost_usd":0.01}` + "\n",
		},
		{
			name:  "no JSON line — returns full buffer",
			input: "no json here\njust plain text\n",
			want:  "no json here\njust plain text\n",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "JSON without trailing newline",
			input: `{"type":"result"}`,
			want:  `{"type":"result"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(trimOutput([]byte(tc.input)))
			if got != tc.want {
				t.Errorf("trimOutput(%q)\n  got:  %q\n  want: %q", tc.input, got, tc.want)
			}
		})
	}
}

// setupSuperviseFixture creates a minimal run + dispatch directory structure
// suitable for calling Supervise directly (white-box test helper).
// It returns (runDir, dispatchID, runsBase, st).
func setupSuperviseFixture(t *testing.T) (runDir, dispatchID string, st *fsstore.FS) {
	t.Helper()

	workspace := t.TempDir()
	runsBase := filepath.Join(workspace, ".tiller", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}

	st = fsstore.Open(runsBase)
	r := &scratch.Run{
		Task:      "test task",
		Workspace: workspace,
		Status:    "running",
		CreatedAt: time.Now(),
	}
	runID, err := st.CreateRun(r)
	if err != nil {
		t.Fatal(err)
	}
	runDir = filepath.Join(runsBase, runID)

	dispatchID = "d-supervise-test"
	dispDir := filepath.Join(runDir, "dispatches", dispatchID)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a minimal brief.md and settings.json (Supervise reads these paths).
	if err := os.WriteFile(filepath.Join(dispDir, "brief.md"), []byte("test brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &scratch.Dispatch{
		ID:        dispatchID,
		Role:      "worker",
		Model:     "sonnet",
		Tier:      "execute",
		Profile:   "orchestrator",
		Status:    "running",
		Depth:     1,
		StartedAt: time.Now(),
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		t.Fatal(err)
	}

	return runDir, dispatchID, st
}

// writeStubBin writes a small bash stub that emits the given stdout content and
// exits 0. Returns the path to the executable.
func writeStubBin(t *testing.T, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "stub")
	// Escape any single-quotes in the content by ending/opening the single-quoted string.
	escaped := strings.ReplaceAll(stdout, "'", `'"'"'`)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", escaped)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSupervise_CapExceeded verifies that when stdout exceeds maxTranscriptParseBytes:
//   - The supervisor does NOT OOM (it streams to disk)
//   - transcript.json contains all emitted bytes
//   - report.md explains the cap was exceeded
//   - dispatch status is "halted"
func TestSupervise_CapExceeded(t *testing.T) {
	runDir, dispatchID, st := setupSuperviseFixture(t)
	runID := filepath.Base(runDir)

	// Override the cap to a tiny value so we can trigger it cheaply.
	orig := maxTranscriptParseBytes
	maxTranscriptParseBytes = 64 // 64 bytes
	defer func() { maxTranscriptParseBytes = orig }()

	// Stub emits more than 64 bytes of output — a simple repeated string.
	bigOutput := strings.Repeat("X", 200) // 200 bytes, well over 64-byte test cap
	stub := writeStubBin(t, bigOutput)

	// Supervise reads TILLER_CLAUDE_BIN from env.
	t.Setenv("TILLER_CLAUDE_BIN", stub)
	t.Setenv("TILLER_RUN_DIR", runDir)

	err := Supervise(SuperviseArgs{
		RunDir:     runDir,
		DispatchID: dispatchID,
	})
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}

	dispDir := filepath.Join(runDir, "dispatches", dispatchID)

	// transcript.json must contain the full emitted bytes.
	transcriptData, err := os.ReadFile(filepath.Join(dispDir, "transcript.json"))
	if err != nil {
		t.Fatalf("read transcript.json: %v", err)
	}
	if string(transcriptData) != bigOutput {
		t.Errorf("transcript.json content mismatch:\n  got  len=%d\n  want len=%d", len(transcriptData), len(bigOutput))
	}

	// report.md must mention the cap-exceeded explanation.
	reportData, err := os.ReadFile(filepath.Join(dispDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	reportStr := string(reportData)
	if !strings.Contains(reportStr, "exceeds parse cap") {
		t.Errorf("report.md should mention 'exceeds parse cap', got: %q", reportStr)
	}

	// Dispatch status must be "halted".
	d, err := st.ReadDispatch(runID, dispatchID)
	if err != nil {
		t.Fatalf("read dispatch: %v", err)
	}
	if d.Status != "halted" {
		t.Errorf("dispatch.Status = %q, want halted", d.Status)
	}
}

// TestSupervise_UnderCap_ParseSucceeds verifies that for output within the cap,
// parseClaudeResult still finds the trailing result event and report.md = Result.
// This also confirms the normal (under-cap) path works end-to-end after the
// streaming refactor.
func TestSupervise_UnderCap_ParseSucceeds(t *testing.T) {
	runDir, dispatchID, st := setupSuperviseFixture(t)
	runID := filepath.Base(runDir)

	// A real claude ≥2.1.172 array-format output with several events leading up
	// to the result record. This exercises the "find trailing result event" path.
	arrayOutput := `[{"type":"system","session_id":"sess-test","subtype":"init"},` +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"working..."}]},"session_id":"sess-test"},` +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]},"session_id":"sess-test"},` +
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"parsed result text","session_id":"sess-test","total_cost_usd":0.042}]` + "\n"

	stub := writeStubBin(t, arrayOutput)

	t.Setenv("TILLER_CLAUDE_BIN", stub)
	t.Setenv("TILLER_RUN_DIR", runDir)

	err := Supervise(SuperviseArgs{
		RunDir:     runDir,
		DispatchID: dispatchID,
	})
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}

	dispDir := filepath.Join(runDir, "dispatches", dispatchID)

	// transcript.json must contain the full JSON array.
	transcriptData, err := os.ReadFile(filepath.Join(dispDir, "transcript.json"))
	if err != nil {
		t.Fatalf("read transcript.json: %v", err)
	}
	if string(transcriptData) != arrayOutput {
		t.Errorf("transcript.json content mismatch:\n  got  %q\n  want %q", string(transcriptData), arrayOutput)
	}

	// report.md must contain the parsed result text (from the result event).
	reportData, err := os.ReadFile(filepath.Join(dispDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	if string(reportData) != "parsed result text" {
		t.Errorf("report.md = %q, want %q", string(reportData), "parsed result text")
	}

	// Dispatch status must be "completed" and cost correct.
	d, err := st.ReadDispatch(runID, dispatchID)
	if err != nil {
		t.Fatalf("read dispatch: %v", err)
	}
	if d.Status != "completed" {
		t.Errorf("dispatch.Status = %q, want completed", d.Status)
	}
	if d.CostUSD != 0.042 {
		t.Errorf("dispatch.CostUSD = %f, want 0.042", d.CostUSD)
	}
	if d.NumTurns != 2 {
		t.Errorf("dispatch.NumTurns = %d, want 2", d.NumTurns)
	}
}
