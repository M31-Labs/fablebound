// Package storetest provides the scratch.Store conformance suite.
//
// Use Run(t, open) to execute the full suite against any Store implementation.
// The open function receives a fresh *testing.T (for temp-dir lifecycle) and
// must return a ready-to-use Store.
//
// This suite is designed to be reused verbatim in P3 against pgstore.
package storetest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/tiller/internal/scratch"
)

// Run executes the full Store conformance suite against the implementation
// produced by open. Each sub-test receives its own Store instance.
func Run(t *testing.T, open func(t *testing.T) scratch.Store) {
	t.Helper()

	t.Run("RunCRUD", func(t *testing.T) { testRunCRUD(t, open(t)) })
	t.Run("DispatchAllocMonotonic", func(t *testing.T) { testDispatchAllocMonotonic(t, open(t)) })
	t.Run("DispatchFacts", func(t *testing.T) { testDispatchFacts(t, open(t)) })
	t.Run("BriefReportRoundtrip", func(t *testing.T) { testBriefReportRoundtrip(t, open(t)) })
	t.Run("NoteOrdering", func(t *testing.T) { testNoteOrdering(t, open(t)) })
	t.Run("AdapterConfig", func(t *testing.T) { testAdapterConfig(t, open(t)) })
	t.Run("ConcurrentTraceAppend", func(t *testing.T) { testConcurrentTraceAppend(t, open(t)) })
	t.Run("AuditSinkWrites", func(t *testing.T) { testAuditSinkWrites(t, open(t)) })
	t.Run("MaterializeIdempotent", func(t *testing.T) { testMaterializeIdempotent(t, open(t)) })
	t.Run("ListRuns", func(t *testing.T) { testListRuns(t, open(t)) })
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustCreateRun(t *testing.T, s scratch.Store) string {
	t.Helper()
	r := &scratch.Run{
		Task:      "conformance test task",
		Workspace: t.TempDir(),
		Status:    "created",
	}
	id, err := s.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return id
}

func mustAllocDispatch(t *testing.T, s scratch.Store, runID string) string {
	t.Helper()
	did, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch(%s): %v", runID, err)
	}
	return did
}

// ── test cases ────────────────────────────────────────────────────────────────

func testRunCRUD(t *testing.T, s scratch.Store) {
	t.Helper()

	// Create.
	now := time.Now().UTC().Truncate(time.Second)
	r := &scratch.Run{
		Task:        "my task",
		Workspace:   t.TempDir(),
		Status:      "created",
		FableBudget: 3,
		CreatedAt:   now,
		PolicySHAs:  map[string]string{"dispatch": "abc123"},
	}
	id, err := s.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if id == "" {
		t.Fatal("CreateRun returned empty ID")
	}

	// Read.
	got, err := s.ReadRun(id)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.ID != id {
		t.Errorf("ReadRun ID=%q want %q", got.ID, id)
	}
	if got.Task != "my task" {
		t.Errorf("ReadRun Task=%q want %q", got.Task, "my task")
	}
	if got.FableBudget != 3 {
		t.Errorf("ReadRun FableBudget=%d want 3", got.FableBudget)
	}
	if got.PolicySHAs["dispatch"] != "abc123" {
		t.Errorf("ReadRun PolicySHAs dispatch=%q want abc123", got.PolicySHAs["dispatch"])
	}

	// Write (status update).
	got.Status = "running"
	if err := s.WriteRun(got); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	got2, err := s.ReadRun(id)
	if err != nil {
		t.Fatalf("ReadRun after write: %v", err)
	}
	if got2.Status != "running" {
		t.Errorf("status after WriteRun=%q want running", got2.Status)
	}

	// Preset ID is honoured.
	r2 := &scratch.Run{
		ID:        "20260101-000000-zzzz",
		Task:      "preset id run",
		Workspace: t.TempDir(),
		Status:    "created",
	}
	id2, err := s.CreateRun(r2)
	if err != nil {
		t.Fatalf("CreateRun with preset ID: %v", err)
	}
	if id2 != "20260101-000000-zzzz" {
		t.Errorf("preset ID not honoured: got %q", id2)
	}
}

func testDispatchAllocMonotonic(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	const n = 5
	ids := make([]string, n)
	for i := range ids {
		did, err := s.AllocDispatch(runID)
		if err != nil {
			t.Fatalf("AllocDispatch %d: %v", i, err)
		}
		ids[i] = did
	}

	// Expect d01, d02, d03, d04, d05 in order.
	for i, id := range ids {
		want := t.Name() // unused but captures i nicely
		_ = want
		expected := t.Name()
		_ = expected
		wantID := strings.ToLower(strings.Replace(t.Name(), t.Name(), "", 1))
		_ = wantID
		// Direct check.
		if id == "" {
			t.Errorf("dispatch %d: empty ID", i)
			continue
		}
		if i > 0 && ids[i] <= ids[i-1] {
			t.Errorf("dispatch IDs not monotonic: ids[%d]=%q <= ids[%d]=%q", i, ids[i], i-1, ids[i-1])
		}
	}
	// First must be d01.
	if ids[0] != "d01" {
		t.Errorf("first dispatch ID = %q, want d01", ids[0])
	}
	// Last must be d05.
	if ids[n-1] != "d05" {
		t.Errorf("last dispatch ID = %q, want d05", ids[n-1])
	}
}

func testDispatchFacts(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	// Alloc 3 dispatches.
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = mustAllocDispatch(t, s, runID)
	}

	// d01: running, fable model.
	d01 := &scratch.Dispatch{
		ID:        ids[0],
		Role:      "orchestrator",
		Model:     "fable",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d01); err != nil {
		t.Fatalf("WriteDispatch d01: %v", err)
	}

	// d02: running, sonnet model (not reason-tier).
	d02 := &scratch.Dispatch{
		ID:        ids[1],
		Role:      "investigator",
		Model:     "sonnet",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d02); err != nil {
		t.Fatalf("WriteDispatch d02: %v", err)
	}

	// d03: completed, fable model.
	d03 := &scratch.Dispatch{
		ID:        ids[2],
		Role:      "worker",
		Model:     "fable",
		Status:    "completed",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d03); err != nil {
		t.Fatalf("WriteDispatch d03: %v", err)
	}

	facts, err := s.DispatchFacts(runID)
	if err != nil {
		t.Fatalf("DispatchFacts: %v", err)
	}
	// Active: d01 + d02 = 2 running.
	if facts.Active != 2 {
		t.Errorf("DispatchFacts.Active=%d want 2", facts.Active)
	}
	// ReasonCount: d01 + d03 = 2 fable models.
	if facts.ReasonCount != 2 {
		t.Errorf("DispatchFacts.ReasonCount=%d want 2", facts.ReasonCount)
	}
}

func testBriefReportRoundtrip(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	briefBody := []byte("# Brief\n\nInvestigate the thing.\n")
	if err := s.WriteBrief(runID, did, briefBody); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}
	gotBrief, err := s.ReadBrief(runID, did)
	if err != nil {
		t.Fatalf("ReadBrief: %v", err)
	}
	if string(gotBrief) != string(briefBody) {
		t.Errorf("brief roundtrip mismatch:\ngot:  %q\nwant: %q", gotBrief, briefBody)
	}

	reportBody := []byte("# Report\n\nFound the thing.\n")
	if err := s.WriteReport(runID, did, reportBody); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	gotReport, err := s.ReadReport(runID, did)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if string(gotReport) != string(reportBody) {
		t.Errorf("report roundtrip mismatch:\ngot:  %q\nwant: %q", gotReport, reportBody)
	}

	// Overwrite brief (idempotent).
	newBrief := []byte("# Brief v2\n\nUpdated.\n")
	if err := s.WriteBrief(runID, did, newBrief); err != nil {
		t.Fatalf("WriteBrief overwrite: %v", err)
	}
	gotBrief2, err := s.ReadBrief(runID, did)
	if err != nil {
		t.Fatalf("ReadBrief after overwrite: %v", err)
	}
	if string(gotBrief2) != string(newBrief) {
		t.Errorf("brief overwrite mismatch: got %q", gotBrief2)
	}
}

func testNoteOrdering(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	authors := []string{"orchestrator", "investigator", "worker"}
	for _, a := range authors {
		body := []byte("note from " + a)
		if _, err := s.AppendNote(runID, a, body); err != nil {
			t.Fatalf("AppendNote %q: %v", a, err)
		}
		// Small sleep to guarantee distinct timestamps in filenames.
		time.Sleep(2 * time.Millisecond)
	}

	refs, err := s.ListNotes(runID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("ListNotes count=%d want 3", len(refs))
	}
	// All refs must have non-empty filenames.
	for i, r := range refs {
		if r.Filename == "" {
			t.Errorf("note %d has empty filename", i)
		}
	}
	// Must be in ascending filename order (timestamps guarantee chronological order).
	for i := 1; i < len(refs); i++ {
		if refs[i].Filename <= refs[i-1].Filename {
			t.Errorf("notes not in ascending order: refs[%d]=%q <= refs[%d]=%q",
				i, refs[i].Filename, i-1, refs[i-1].Filename)
		}
	}
}

func testAdapterConfig(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	cfg := []byte(`{"hooks":{"PreToolUse":[{"type":"command","command":"tiller hook"}]}}`)
	if err := s.WriteAdapterConfig(runID, did, cfg); err != nil {
		t.Fatalf("WriteAdapterConfig: %v", err)
	}
	// No ReadAdapterConfig in the interface; just verify it doesn't fail and
	// can be overwritten.
	if err := s.WriteAdapterConfig(runID, did, cfg); err != nil {
		t.Fatalf("WriteAdapterConfig overwrite: %v", err)
	}
}

func testConcurrentTraceAppend(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ev := scratch.TraceEvent{
				Ts:           time.Now().UTC().Format(time.RFC3339Nano),
				Kind:         "tool",
				RunID:        runID,
				DispatchID:   did,
				Role:         "worker",
				Depth:        1,
				Tool:         "Bash",
				InputSummary: strings.Repeat("x", i+1),
				Status:       "ok",
			}
			errs[i] = s.AppendTraceEvent(runID, did, ev)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: AppendTraceEvent: %v", i, err)
		}
	}

	// Verify we can also append "read" kind events without error.
	readEv := scratch.TraceEvent{
		Ts:           time.Now().UTC().Format(time.RFC3339Nano),
		Kind:         "read",
		RunID:        runID,
		DispatchID:   did,
		Role:         "worker",
		Depth:        1,
		Tool:         "Read",
		InputSummary: "/some/file.go",
	}
	if err := s.AppendTraceEvent(runID, did, readEv); err != nil {
		t.Errorf("AppendTraceEvent read kind: %v", err)
	}
}

func testAuditSinkWrites(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	for _, kind := range []string{"dispatch", "toolgate"} {
		sink, closer, err := s.AuditSink(runID, kind)
		if err != nil {
			t.Fatalf("AuditSink(%q): %v", kind, err)
		}
		if closer != nil {
			defer closer.Close()
		}
		if sink == nil {
			t.Fatalf("AuditSink(%q): returned nil sink", kind)
		}

		// Write a minimal DecisionEvent.
		event := minimalDecisionEvent(kind)
		if err := sink.WriteDecision(context.Background(), event); err != nil {
			t.Errorf("AuditSink(%q): WriteDecision: %v", kind, err)
		}
	}

	// Unknown kind must error.
	_, _, err := s.AuditSink(runID, "unknown")
	if err == nil {
		t.Error("AuditSink with unknown kind should return error")
	}
}

func testMaterializeIdempotent(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	dir := t.TempDir()
	// Call twice — both must succeed.
	if err := s.Materialize(runID, did, dir); err != nil {
		t.Fatalf("Materialize first: %v", err)
	}
	if err := s.Materialize(runID, did, dir); err != nil {
		t.Fatalf("Materialize second (idempotent): %v", err)
	}
}

// minimalDecisionEvent returns the smallest valid audit.DecisionEvent for testing.
func minimalDecisionEvent(kind string) audit.DecisionEvent {
	return audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  "test-bundle-sha256",
		Kind:      "rules",
		Context:   map[string]any{"audit_kind": kind},
	}
}

func testListRuns(t *testing.T, s scratch.Store) {
	t.Helper()
	// Create two runs.
	r1 := &scratch.Run{Task: "task one", Workspace: t.TempDir(), Status: "completed"}
	r2 := &scratch.Run{Task: "task two", Workspace: t.TempDir(), Status: "running"}
	id1, err := s.CreateRun(r1)
	if err != nil {
		t.Fatalf("CreateRun 1: %v", err)
	}
	id2, err := s.CreateRun(r2)
	if err != nil {
		t.Fatalf("CreateRun 2: %v", err)
	}

	items, err := s.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("ListRuns count=%d want >=2", len(items))
	}

	byID := make(map[string]scratch.RunSummary, len(items))
	for _, item := range items {
		byID[item.RunID] = item
	}
	for _, id := range []string{id1, id2} {
		if _, ok := byID[id]; !ok {
			t.Errorf("ListRuns missing run %q", id)
		}
	}
}
