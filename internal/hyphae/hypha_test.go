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

// TestParseEnvelopeID tests the JSON envelope parser used by TraceStart.
func TestParseEnvelopeID(t *testing.T) {
	// v0.1.9 JSON envelope — ID lives at d.ID.
	envelope := `{"c":"trace start","d":{"AgentID":"agent://tiller","ID":"trace.2026-06-10.test.ab12","Phase":"build","SpaceID":"hypha://m31labs/tiller","Status":"open","TaskRef":"run-abc"},"e":[],"ok":true,"s":1,"v":"0.1.9","w":[]}`
	got := parseEnvelopeID(envelope)
	if got != "trace.2026-06-10.test.ab12" {
		t.Errorf("parseEnvelopeID envelope: got %q, want %q", got, "trace.2026-06-10.test.ab12")
	}

	// Legacy plain-text — falls back to firstWord.
	got = parseEnvelopeID("trace-abc-123")
	if got != "trace-abc-123" {
		t.Errorf("parseEnvelopeID plain-text: got %q, want %q", got, "trace-abc-123")
	}

	// Empty input.
	got = parseEnvelopeID("")
	if got != "" {
		t.Errorf("parseEnvelopeID empty: got %q", got)
	}

	// Malformed JSON — falls back to firstWord.
	got = parseEnvelopeID("{not json}")
	if got != "{not" {
		t.Errorf("parseEnvelopeID malformed JSON: got %q, want %q", got, "{not")
	}
}

// TestParseEnvelopeFilePath tests the JSON envelope file_path parser used by SporeSubmit.
func TestParseEnvelopeFilePath(t *testing.T) {
	// v0.1.9 JSON envelope with FilePath field.
	envelope := `{"c":"spore submit","d":{"FilePath":"/home/user/.hyphae/spaces/m31labs-tiller/spores/sp.2026.md","ID":"sp.2026"},"e":[],"ok":true,"s":1,"v":"0.1.9","w":[]}`
	got := parseEnvelopeFilePath(envelope)
	if got != "/home/user/.hyphae/spaces/m31labs-tiller/spores/sp.2026.md" {
		t.Errorf("parseEnvelopeFilePath: got %q", got)
	}

	// No FilePath field — returns "".
	got = parseEnvelopeFilePath(`{"c":"x","d":{},"ok":true}`)
	if got != "" {
		t.Errorf("parseEnvelopeFilePath no FilePath: got %q", got)
	}

	// Plain text — returns "".
	got = parseEnvelopeFilePath("plain text output")
	if got != "" {
		t.Errorf("parseEnvelopeFilePath plain text: got %q", got)
	}
}

// TestHyphaStubRecordsArgv creates a hypha stub that emits v0.1.9 JSON
// envelopes on stdout for trace start/done, and verifies the full trace
// lifecycle including --space on tick and done.
func TestHyphaStubRecordsArgv(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a hypha stub that appends argv to a log file and emits the
	// v0.1.9 JSON envelope for trace start and trace done.
	logFile := filepath.Join(tmpDir, "hypha.log")
	stubPath := filepath.Join(tmpDir, "hypha")
	// The stub emits the JSON envelope for "trace start" (matching real v0.1.9
	// behaviour) and "trace done", plain text for tick (also matching real
	// behaviour observed in the live probe).
	stubScript := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1 $2" in
  "trace start") echo '{"c":"trace start","d":{"ID":"trace-abc-123","SpaceID":"hypha://m31labs/tiller"},"ok":true,"v":"0.1.9"}' ;;
  "trace done")  echo '{"c":"trace done","d":{"ID":"trace-abc-123","Status":"completed"},"ok":true,"v":"0.1.9"}' ;;
  "trace tick")  echo "tick: trace-abc-123" ;;
esac
exit 0
`
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
	if traceID != "trace-abc-123" {
		t.Errorf("TraceStart should parse JSON envelope id, got %q", traceID)
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

	// line[1]: trace tick with dispatched message AND --space
	if !strings.HasPrefix(lines[1], "trace tick") {
		t.Errorf("second call should be trace tick, got: %s", lines[1])
	}
	if !strings.Contains(lines[1], "dispatched") {
		t.Errorf("second tick should contain 'dispatched': %s", lines[1])
	}
	if !strings.Contains(lines[1], "--space") {
		t.Errorf("trace tick missing --space: %s", lines[1])
	}

	// line[2]: trace tick with status/cost AND --space
	if !strings.HasPrefix(lines[2], "trace tick") {
		t.Errorf("third call should be trace tick, got: %s", lines[2])
	}
	if !strings.Contains(lines[2], "completed") {
		t.Errorf("third tick should contain 'completed': %s", lines[2])
	}
	if !strings.Contains(lines[2], "--space") {
		t.Errorf("trace tick missing --space: %s", lines[2])
	}

	// line[3]: trace done with --status AND --space
	if !strings.HasPrefix(lines[3], "trace done") {
		t.Errorf("fourth call should be trace done, got: %s", lines[3])
	}
	if !strings.Contains(lines[3], "--status") {
		t.Errorf("trace done missing --status: %s", lines[3])
	}
	if !strings.Contains(lines[3], "--space") {
		t.Errorf("trace done missing --space: %s", lines[3])
	}
}

// TestTillerStatusToHypha verifies that tiller run-terminal statuses are
// correctly mapped to hypha v0.1.9 trace-done vocabulary.
func TestTillerStatusToHypha(t *testing.T) {
	cases := []struct {
		tiller string
		hypha  string
	}{
		{"completed", "succeeded"},
		{"failed", "failed"},
		{"halted", "killed"},
		{"stale", "killed"},
		{"", "killed"},
		{"unknown-future-status", "killed"},
	}
	for _, tc := range cases {
		got := tillerStatusToHypha(tc.tiller)
		if got != tc.hypha {
			t.Errorf("tillerStatusToHypha(%q) = %q, want %q", tc.tiller, got, tc.hypha)
		}
	}
}

// TestTraceDoneSendsSucceeded verifies that TraceDone maps "completed"→"succeeded"
// in the hypha argv, not passing tiller vocabulary directly.
func TestTraceDoneSendsSucceeded(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "hypha.log")
	stubPath := filepath.Join(tmpDir, "hypha")
	stubScript := "#!/bin/sh\necho \"$@\" >> " + logFile + "\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	orig := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+":"+orig)
	defer os.Setenv("PATH", orig)

	h := New(func(string, ...any) {})
	if !h.Available() {
		t.Skip("hypha stub not found on PATH")
	}

	// Mapping: completed→succeeded, failed→failed, halted→killed.
	for _, tc := range []struct{ in, want string }{
		{"completed", "succeeded"},
		{"failed", "failed"},
		{"halted", "killed"},
	} {
		// Clear log file between calls.
		_ = os.Remove(logFile)
		h.TraceDone("trace-test-id", tc.in)
		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatalf("read hypha log for status %q: %v", tc.in, err)
		}
		line := strings.TrimSpace(string(data))
		if !strings.Contains(line, "--status "+tc.want) {
			t.Errorf("TraceDone(%q): want --status %q in argv, got: %q", tc.in, tc.want, line)
		}
		if strings.Contains(line, "--status "+tc.in) && tc.in != tc.want {
			t.Errorf("TraceDone(%q): tiller vocabulary leaked into hypha argv: %q", tc.in, line)
		}
	}
}

// TestHyphaStubLegacyPlainText verifies TraceStart falls back to firstWord
// when the stub emits legacy plain-text (pre-v0.1.9 behaviour).
func TestHyphaStubLegacyPlainText(t *testing.T) {
	tmpDir := t.TempDir()

	logFile := filepath.Join(tmpDir, "hypha.log")
	stubPath := filepath.Join(tmpDir, "hypha")
	// Legacy stub: emits just the trace id as a plain word.
	stubScript := `#!/bin/sh
echo "$@" >> ` + logFile + `
case "$1 $2" in
  "trace start") echo "trace-legacy-456" ;;
esac
exit 0
`
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	orig := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+":"+orig)
	defer os.Setenv("PATH", orig)

	h := New(func(string, ...any) {})
	if !h.Available() {
		t.Skip("hypha stub not found on PATH (test env issue)")
	}

	traceID := h.TraceStart("run-legacy", "legacy phase", HyphaSpace)
	if traceID != "trace-legacy-456" {
		t.Errorf("legacy plain-text: expected %q, got %q", "trace-legacy-456", traceID)
	}
}
