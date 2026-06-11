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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/tiller/internal/scratch"
)

// jsonUnmarshal is a local alias so the storetest package can remain
// self-contained and the main test code can use it without direct import.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// Run executes the full Store conformance suite against the implementation
// produced by open. Each sub-test receives its own Store instance.
func Run(t *testing.T, open func(t *testing.T) scratch.Store) {
	t.Helper()

	t.Run("RunCRUD", func(t *testing.T) { testRunCRUD(t, open(t)) })
	t.Run("DispatchAllocMonotonic", func(t *testing.T) { testDispatchAllocMonotonic(t, open(t)) })
	t.Run("DispatchAllocConcurrent", func(t *testing.T) { testDispatchAllocConcurrent(t, open(t)) })
	t.Run("DispatchFacts", func(t *testing.T) { testDispatchFacts(t, open(t)) })
	t.Run("BriefReportRoundtrip", func(t *testing.T) { testBriefReportRoundtrip(t, open(t)) })
	t.Run("NoteOrdering", func(t *testing.T) { testNoteOrdering(t, open(t)) })
	t.Run("AdapterConfig", func(t *testing.T) { testAdapterConfig(t, open(t)) })
	t.Run("ConcurrentTraceAppend", func(t *testing.T) { testConcurrentTraceAppend(t, open(t)) })
	t.Run("AuditSinkWrites", func(t *testing.T) { testAuditSinkWrites(t, open(t)) })
	t.Run("MaterializeIdempotent", func(t *testing.T) { testMaterializeIdempotent(t, open(t)) })
	t.Run("ListRuns", func(t *testing.T) { testListRuns(t, open(t)) })
	t.Run("ReadAdapterConfig", func(t *testing.T) { testReadAdapterConfig(t, open(t)) })
	t.Run("RenderTree", func(t *testing.T) { testRenderTree(t, open(t)) })
	t.Run("BuildRunSummaryJSON", func(t *testing.T) { testBuildRunSummaryJSON(t, open(t)) })
	t.Run("BuildDispatchTree", func(t *testing.T) { testBuildDispatchTree(t, open(t)) })
	// P4.1 claim semantics.
	t.Run("ClaimConcurrent", func(t *testing.T) { testClaimConcurrent(t, open(t)) })
	t.Run("ClaimExpireRequeue", func(t *testing.T) { testClaimExpireRequeue(t, open(t)) })
	t.Run("ClaimRenewPreventsExpiry", func(t *testing.T) { testClaimRenewPreventsExpiry(t, open(t)) })
	t.Run("ClaimReleaseTerminal", func(t *testing.T) { testClaimReleaseTerminal(t, open(t)) })
	t.Run("ListPendingDispatches", func(t *testing.T) { testListPendingDispatches(t, open(t)) })
	// F1 running-state expiry (pool crash recovery).
	t.Run("RunningStateExpireWithLease", func(t *testing.T) { testRunningStateExpireWithLease(t, open(t)) })
	t.Run("RunningStateNoLeaseUntouched", func(t *testing.T) { testRunningStateNoLeaseUntouched(t, open(t)) })
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
		Task:         "my task",
		Workspace:    t.TempDir(),
		Status:       "created",
		ReasonBudget: 3,
		CreatedAt:    now,
		PolicySHAs:   map[string]string{"dispatch": "abc123"},
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
	if got.ReasonBudget != 3 {
		t.Errorf("ReadRun ReasonBudget=%d want 3", got.ReasonBudget)
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

// testDispatchAllocConcurrent verifies that AllocDispatch is safe under
// concurrent contention: 8 goroutines each allocate N IDs; the full set must
// be unique and form a monotonically-ordered sequence with no gaps.
func testDispatchAllocConcurrent(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	const goroutines = 8
	const allocsPerGoroutine = 5
	const total = goroutines * allocsPerGoroutine

	var mu sync.Mutex
	var wg sync.WaitGroup
	allIDs := make([]string, 0, total)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := make([]string, allocsPerGoroutine)
			for i := 0; i < allocsPerGoroutine; i++ {
				id, err := s.AllocDispatch(runID)
				if err != nil {
					t.Errorf("concurrent AllocDispatch: %v", err)
					return
				}
				local[i] = id
			}
			mu.Lock()
			allIDs = append(allIDs, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(allIDs) != total {
		t.Fatalf("expected %d IDs, got %d", total, len(allIDs))
	}

	// All IDs must be unique.
	seen := make(map[string]bool, total)
	for _, id := range allIDs {
		if id == "" {
			t.Error("concurrent AllocDispatch returned empty ID")
			continue
		}
		if seen[id] {
			t.Errorf("duplicate dispatch ID: %q", id)
		}
		seen[id] = true
	}

	// The set must span d01..d{total} with no gaps (all present).
	for i := 1; i <= total; i++ {
		want := fmt.Sprintf("d%02d", i)
		if !seen[want] {
			t.Errorf("missing dispatch ID %q in concurrent alloc set", want)
		}
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

func testReadAdapterConfig(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	cfg := []byte(`{"hooks":{"PreToolUse":[{"type":"command","command":"tiller hook"}]}}`)
	if err := s.WriteAdapterConfig(runID, did, cfg); err != nil {
		t.Fatalf("WriteAdapterConfig: %v", err)
	}
	got, err := s.ReadAdapterConfig(runID, did)
	if err != nil {
		t.Fatalf("ReadAdapterConfig: %v", err)
	}
	if string(got) != string(cfg) {
		t.Errorf("ReadAdapterConfig mismatch:\ngot:  %q\nwant: %q", got, cfg)
	}
}

func testRenderTree(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	d := &scratch.Dispatch{
		ID:        did,
		Role:      "worker",
		Model:     "sonnet",
		Status:    "completed",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	tree, err := s.RenderTree(runID)
	if err != nil {
		t.Fatalf("RenderTree: %v", err)
	}
	// Must contain the dispatch id and role.
	if !strings.Contains(tree, did) {
		t.Errorf("RenderTree output missing dispatch id %q:\n%s", did, tree)
	}
}

func testBuildRunSummaryJSON(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	data, err := s.BuildRunSummaryJSON(runID)
	if err != nil {
		t.Fatalf("BuildRunSummaryJSON: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("BuildRunSummaryJSON returned empty data")
	}
	// Must be valid JSON.
	var obj map[string]any
	if err := jsonUnmarshal(data, &obj); err != nil {
		t.Errorf("BuildRunSummaryJSON returned invalid JSON: %v", err)
	}
}

func testBuildDispatchTree(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	d := &scratch.Dispatch{
		ID:        did,
		Role:      "investigator",
		Model:     "sonnet",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	root, err := s.BuildDispatchTree(runID)
	if err != nil {
		t.Fatalf("BuildDispatchTree: %v", err)
	}
	if root == nil {
		t.Fatal("BuildDispatchTree returned nil root")
	}
	// The tree must contain at least one node for did.
	found := findDispatchNode(root, did)
	if !found {
		t.Errorf("BuildDispatchTree: dispatch %q not found in tree", did)
	}
}

// findDispatchNode recursively searches for a node with the given dispatch ID.
func findDispatchNode(n *scratch.DispatchNode, id string) bool {
	if n == nil {
		return false
	}
	if n.Dispatch != nil && n.Dispatch.ID == id {
		return true
	}
	for _, child := range n.Children {
		if findDispatchNode(child, id) {
			return true
		}
	}
	return false
}

// ── P4.1 Claim conformance tests ──────────────────────────────────────────────

// mustSeedPending writes a minimal pending dispatch. AllocDispatch inserts a
// placeholder; WriteDispatch sets the full shape with status=pending.
func mustSeedPending(t *testing.T, s scratch.Store, runID string) string {
	t.Helper()
	did := mustAllocDispatch(t, s, runID)
	d := &scratch.Dispatch{
		ID:        did,
		Role:      "worker",
		Model:     "sonnet",
		Status:    "pending",
		StartedAt: time.Now().UTC(),
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch (seed pending) %s: %v", did, err)
	}
	return did
}

// testClaimConcurrent: 8 concurrent ClaimDispatch calls → exactly 1 true.
func testClaimConcurrent(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustSeedPending(t, s, runID)

	const goroutines = 8
	results := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			executor := fmt.Sprintf("exec-%d", i)
			won, err := s.ClaimDispatch(runID, did, executor, 10*time.Second)
			if err != nil {
				t.Errorf("goroutine %d ClaimDispatch: %v", i, err)
				return
			}
			results[i] = won
		}()
	}
	wg.Wait()

	wins := 0
	for _, w := range results {
		if w {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("ClaimDispatch concurrent: %d winners (want exactly 1)", wins)
	}

	// Dispatch status must be "claimed".
	d, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch after claim: %v", err)
	}
	if d.Status != "claimed" {
		t.Errorf("status after claim=%q want claimed", d.Status)
	}
	if d.ClaimedBy == "" {
		t.Error("claimed_by is empty after claim")
	}
	if d.LeaseUntil == nil {
		t.Error("lease_until is nil after claim")
	}
}

// testClaimExpireRequeue: claim with 50ms lease, wait, ExpireLeases re-queues,
// then a second claim on the same dispatch succeeds.
func testClaimExpireRequeue(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustSeedPending(t, s, runID)

	won, err := s.ClaimDispatch(runID, did, "exec-a", 50*time.Millisecond)
	if err != nil || !won {
		t.Fatalf("first ClaimDispatch: won=%v err=%v", won, err)
	}

	// Wait for lease to expire.
	time.Sleep(120 * time.Millisecond)

	requeued, err := s.ExpireLeases(runID)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	found := false
	for _, id := range requeued {
		if id == did {
			found = true
		}
	}
	if !found {
		t.Errorf("ExpireLeases: %q not in requeued list %v", did, requeued)
	}

	// Status must be pending again.
	d, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch after expire: %v", err)
	}
	if d.Status != "pending" {
		t.Errorf("status after expire=%q want pending", d.Status)
	}
	if d.ClaimedBy != "" {
		t.Errorf("claimed_by not cleared after expire: %q", d.ClaimedBy)
	}

	// Second claim must succeed.
	won2, err := s.ClaimDispatch(runID, did, "exec-b", 5*time.Second)
	if err != nil {
		t.Fatalf("second ClaimDispatch: %v", err)
	}
	if !won2 {
		t.Error("second ClaimDispatch after expiry should succeed")
	}
}

// testClaimRenewPreventsExpiry: renewing extends so ExpireLeases does NOT requeue.
func testClaimRenewPreventsExpiry(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustSeedPending(t, s, runID)

	won, err := s.ClaimDispatch(runID, did, "exec-renew", 50*time.Millisecond)
	if err != nil || !won {
		t.Fatalf("ClaimDispatch: won=%v err=%v", won, err)
	}

	// Renew before expiry to a long lease.
	if err := s.RenewLease(runID, did, "exec-renew", 10*time.Second); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}

	// Wait past original 50ms window.
	time.Sleep(120 * time.Millisecond)

	requeued, err := s.ExpireLeases(runID)
	if err != nil {
		t.Fatalf("ExpireLeases after renew: %v", err)
	}
	for _, id := range requeued {
		if id == did {
			t.Errorf("ExpireLeases requeued %q but lease was renewed", did)
		}
	}

	// Status must still be "claimed".
	d, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch after renew+expire: %v", err)
	}
	if d.Status != "claimed" {
		t.Errorf("status after renew=%q want claimed", d.Status)
	}
}

// testClaimReleaseTerminal: ReleaseDispatch(completed) is terminal —
// ExpireLeases never touches it and ListPendingDispatches excludes it.
func testClaimReleaseTerminal(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustSeedPending(t, s, runID)

	won, err := s.ClaimDispatch(runID, did, "exec-rel", 10*time.Second)
	if err != nil || !won {
		t.Fatalf("ClaimDispatch: won=%v err=%v", won, err)
	}

	if err := s.ReleaseDispatch(runID, did, "exec-rel", "completed"); err != nil {
		t.Fatalf("ReleaseDispatch: %v", err)
	}

	d, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch: %v", err)
	}
	if d.Status != "completed" {
		t.Errorf("status after release=%q want completed", d.Status)
	}
	if d.ClaimedBy != "" {
		t.Errorf("claimed_by not cleared: %q", d.ClaimedBy)
	}

	// ExpireLeases must not touch it.
	requeued, err := s.ExpireLeases(runID)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	for _, id := range requeued {
		if id == did {
			t.Errorf("ExpireLeases touched terminal dispatch %q", did)
		}
	}

	// ListPendingDispatches must not include it.
	pending, err := s.ListPendingDispatches(runID)
	if err != nil {
		t.Fatalf("ListPendingDispatches: %v", err)
	}
	for _, p := range pending {
		if p.ID == did {
			t.Errorf("ListPendingDispatches includes terminal dispatch %q", did)
		}
	}
}

// testListPendingDispatches: only pending dispatches in alloc order.
func testListPendingDispatches(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)

	// Seed 3 pending dispatches.
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = mustSeedPending(t, s, runID)
	}

	// Claim the middle one.
	won, err := s.ClaimDispatch(runID, ids[1], "exec-x", 10*time.Second)
	if err != nil || !won {
		t.Fatalf("ClaimDispatch ids[1]: won=%v err=%v", won, err)
	}

	pending, err := s.ListPendingDispatches(runID)
	if err != nil {
		t.Fatalf("ListPendingDispatches: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("ListPendingDispatches count=%d want 2", len(pending))
	}
	// Must be in ascending alloc order: ids[0] then ids[2].
	if pending[0].ID != ids[0] {
		t.Errorf("pending[0]=%q want %q", pending[0].ID, ids[0])
	}
	if pending[1].ID != ids[2] {
		t.Errorf("pending[1]=%q want %q", pending[1].ID, ids[2])
	}
	// None should be the claimed one.
	for _, p := range pending {
		if p.ID == ids[1] {
			t.Errorf("ListPendingDispatches includes claimed dispatch %q", ids[1])
		}
		if p.Status != "pending" {
			t.Errorf("ListPendingDispatches entry %q has status=%q want pending", p.ID, p.Status)
		}
	}
}

// testRunningStateExpireWithLease verifies that a "running" dispatch whose
// lease_until is set and expired is reclaimed to "pending" by ExpireLeases
// (F1 pool-crash-recovery fix). Steps:
//
//  1. Claim the dispatch (status → claimed, lease set).
//  2. Manually write status = "running" while keeping the lease intact.
//  3. Wait for the lease to expire.
//  4. ExpireLeases must re-queue it (status → pending, claim cleared).
//  5. Dispatch must be re-claimable by a second executor.
func testRunningStateExpireWithLease(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustSeedPending(t, s, runID)

	const shortLease = 50 * time.Millisecond
	won, err := s.ClaimDispatch(runID, did, "exec-crash", shortLease)
	if err != nil || !won {
		t.Fatalf("ClaimDispatch: won=%v err=%v", won, err)
	}

	// Advance status to "running" while preserving the lease (simulates pool crash
	// after claimed→running write but before the dispatch completes).
	d, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch before running write: %v", err)
	}
	if d.LeaseUntil == nil {
		t.Fatal("lease_until should be set after ClaimDispatch")
	}
	d.Status = "running"
	// LeaseUntil stays as-is (short, about to expire).
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch (running): %v", err)
	}

	// Wait for lease to expire.
	time.Sleep(shortLease + 30*time.Millisecond)

	// ExpireLeases must re-queue the running+expired dispatch.
	requeued, err := s.ExpireLeases(runID)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	found := false
	for _, id := range requeued {
		if id == did {
			found = true
		}
	}
	if !found {
		t.Errorf("ExpireLeases: running dispatch %q not in requeued list %v", did, requeued)
	}

	// Status must be pending, claim cleared.
	d2, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch after expire: %v", err)
	}
	if d2.Status != "pending" {
		t.Errorf("status after expire=%q want pending", d2.Status)
	}
	if d2.ClaimedBy != "" {
		t.Errorf("claimed_by not cleared after expire: %q", d2.ClaimedBy)
	}
	if d2.LeaseUntil != nil {
		t.Error("lease_until not cleared after expire")
	}

	// Must be re-claimable.
	won2, err := s.ClaimDispatch(runID, did, "exec-recovery", 5*time.Second)
	if err != nil {
		t.Fatalf("second ClaimDispatch: %v", err)
	}
	if !won2 {
		t.Error("second ClaimDispatch after running-expiry should succeed")
	}
}

// testRunningStateNoLeaseUntouched verifies that a "running" dispatch with NO
// lease_until (v1-style direct dispatch) is never reclaimed by ExpireLeases.
func testRunningStateNoLeaseUntouched(t *testing.T, s scratch.Store) {
	t.Helper()
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	// Write a running dispatch with no lease (v1 style: tiller run directly).
	now := time.Now().UTC()
	d := &scratch.Dispatch{
		ID:        did,
		Role:      "orchestrator",
		Model:     "fable",
		Status:    "running",
		StartedAt: now,
		// LeaseUntil intentionally nil — v1 dispatch, never pool-managed.
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch (v1 running, no lease): %v", err)
	}

	// ExpireLeases must NOT touch this dispatch.
	requeued, err := s.ExpireLeases(runID)
	if err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	for _, id := range requeued {
		if id == did {
			t.Errorf("ExpireLeases touched running dispatch with no lease: %q", did)
		}
	}

	// Status must still be running.
	d2, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch after ExpireLeases: %v", err)
	}
	if d2.Status != "running" {
		t.Errorf("v1 running dispatch status changed to %q, want running", d2.Status)
	}
}
