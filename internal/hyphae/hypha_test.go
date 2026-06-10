package hyphae

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHyphaNotOnPath verifies that all hypha calls are silent no-ops when
// hypha is not on PATH.
func TestHyphaNotOnPath(t *testing.T) {
	// Temporarily override PATH to an empty/nonexistent dir.
	orig := os.Getenv("PATH")
	t.Setenv("PATH", "/dev/null/nonexistent")
	defer os.Setenv("PATH", orig)

	var logged []string
	log := func(format string, _ ...any) {
		logged = append(logged, format)
	}

	h := New(log)
	if h.Available() {
		t.Fatal("hypha should not be available with empty PATH")
	}

	// All calls must be no-ops and not panic.
	id := h.TraceStart("run-xyz", "test phase", "")
	if id != "" {
		t.Errorf("TraceStart should return empty when hypha unavailable, got %q", id)
	}
	h.TraceTick("", "some message") // no-op on empty id
	h.TraceDone("", "completed")    // no-op on empty id

	_, err := h.SporeSubmit("/tmp/spore.md", "", "")
	if err == nil {
		t.Error("SporeSubmit should return error when hypha unavailable")
	}

	// At least one log line should mention hypha not found.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "not found") || strings.Contains(l, "not available") {
			found = true
		}
	}
	if !found {
		t.Logf("logged: %v", logged)
		t.Error("expected log message about hypha not found")
	}
}

// TestHyphaStubRecordsArgv creates a hypha stub on PATH that records all
// invocations, then verifies the correct argv sequence for a trace lifecycle.
func TestHyphaStubRecordsArgv(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a hypha stub that appends argv to a log file.
	logFile := filepath.Join(tmpDir, "hypha.log")
	stubPath := filepath.Join(tmpDir, "hypha")
	stubScript := "#!/bin/sh\necho \"$@\" >> " + logFile + "\ncase \"$1 $2\" in\n  \"trace start\") echo \"trace-abc-123\" ;;\nesac\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// Override PATH so our stub is found first.
	orig := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+":"+orig)
	defer os.Setenv("PATH", orig)

	var logs []string
	log := func(format string, _ ...any) {
		logs = append(logs, format)
	}

	h := New(log)
	if !h.Available() {
		t.Skip("hypha stub not found on PATH (test env issue)")
	}

	// Simulate trace lifecycle.
	traceID := h.TraceStart("run-20260610-abc1", "first task line", HyphaSpace)
	if traceID == "" {
		t.Error("TraceStart should return trace id from stub")
	}

	h.TraceTick(traceID, "d01 worker(sonnet) dispatched by root")
	h.TraceTick(traceID, "d01 completed $0.0123")
	h.TraceDone(traceID, "completed")

	// Read recorded argv lines.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read hypha log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	// Expect 4 lines: start, tick(dispatched), tick(finished), done.
	if len(lines) < 4 {
		t.Fatalf("expected >=4 hypha calls, got %d: %v", len(lines), lines)
	}

	// line[0]: trace start with --space
	if !strings.HasPrefix(lines[0], "trace start") {
		t.Errorf("first call should be trace start, got: %s", lines[0])
	}
	if !strings.Contains(lines[0], "--space") {
		t.Errorf("trace start missing --space: %s", lines[0])
	}
	if !strings.Contains(lines[0], HyphaSpace) {
		t.Errorf("trace start missing space URI: %s", lines[0])
	}

	// line[1]: trace tick with dispatched message
	if !strings.HasPrefix(lines[1], "trace tick") {
		t.Errorf("second call should be trace tick, got: %s", lines[1])
	}
	if !strings.Contains(lines[1], "dispatched") {
		t.Errorf("second tick should contain 'dispatched': %s", lines[1])
	}

	// line[2]: trace tick with status/cost
	if !strings.HasPrefix(lines[2], "trace tick") {
		t.Errorf("third call should be trace tick, got: %s", lines[2])
	}
	if !strings.Contains(lines[2], "completed") {
		t.Errorf("third tick should contain 'completed': %s", lines[2])
	}

	// line[3]: trace done
	if !strings.HasPrefix(lines[3], "trace done") {
		t.Errorf("fourth call should be trace done, got: %s", lines[3])
	}
	if !strings.Contains(lines[3], "--status") {
		t.Errorf("trace done missing --status: %s", lines[3])
	}
}
