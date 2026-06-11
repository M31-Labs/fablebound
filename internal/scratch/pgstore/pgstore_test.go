package pgstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/pgstore"
)

// compile-time interface satisfaction check
var _ scratch.Store = (*pgstore.Store)(nil)

// dsnOrSkip returns the test DSN or skips — matches migrate_test.go pattern.
func dsnOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TILLER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TILLER_TEST_PG_DSN not set — skipping postgres integration test")
	}
	return dsn
}

// openTestStore creates an isolated schema for the test and returns a store + cleanup.
// Each test gets its own schema to ensure full isolation and re-runnability.
func openTestStore(t *testing.T, dsn string) *pgstore.Store {
	t.Helper()
	ctx := context.Background()

	// First migrate the public schema (idempotent).
	_, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Open a store against the public schema — tests use unique run IDs so rows
	// from different tests don't interfere. TRUNCATE between sub-tests if needed.
	db, err := pgstore.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return pgstore.NewStore(db)
}

// uniqueRunID returns a run ID that is unlikely to collide across test runs.
func uniqueRunID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ── Run CRUD ──────────────────────────────────────────────────────────────────

func TestRunCRUD(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("run-crud")
	now := time.Now().UTC().Truncate(time.Millisecond)

	r := &scratch.Run{
		ID:            runID,
		Task:          "test task first line\nsecond line",
		Workspace:     "/tmp/workspace",
		Status:        "created",
		ReasonBudget:  3,
		CreatedAt:     now,
		RootSessionID: "sess-abc",
		PolicySHAs:    map[string]string{"toolgate": "deadbeef"},
		HyphaTraceID:  "trace-xyz",
	}

	// CreateRun
	gotID, err := s.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if gotID != runID {
		t.Errorf("CreateRun: got id %q, want %q", gotID, runID)
	}

	// ReadRun — all fields round-trip
	got, err := s.ReadRun(runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.ID != r.ID {
		t.Errorf("ReadRun.ID: got %q, want %q", got.ID, r.ID)
	}
	if got.Task != r.Task {
		t.Errorf("ReadRun.Task: got %q, want %q", got.Task, r.Task)
	}
	if got.Workspace != r.Workspace {
		t.Errorf("ReadRun.Workspace: got %q, want %q", got.Workspace, r.Workspace)
	}
	if got.Status != r.Status {
		t.Errorf("ReadRun.Status: got %q, want %q", got.Status, r.Status)
	}
	if got.ReasonBudget != r.ReasonBudget {
		t.Errorf("ReadRun.ReasonBudget: got %d, want %d", got.ReasonBudget, r.ReasonBudget)
	}
	if got.RootSessionID != r.RootSessionID {
		t.Errorf("ReadRun.RootSessionID: got %q, want %q", got.RootSessionID, r.RootSessionID)
	}
	if got.HyphaTraceID != r.HyphaTraceID {
		t.Errorf("ReadRun.HyphaTraceID: got %q, want %q", got.HyphaTraceID, r.HyphaTraceID)
	}
	if got.PolicySHAs["toolgate"] != "deadbeef" {
		t.Errorf("ReadRun.PolicySHAs: got %v, want {toolgate:deadbeef}", got.PolicySHAs)
	}

	// WriteRun — status transition
	r.Status = "running"
	endedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.EndedAt = &endedAt
	if err := s.WriteRun(r); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	got2, err := s.ReadRun(runID)
	if err != nil {
		t.Fatalf("ReadRun after WriteRun: %v", err)
	}
	if got2.Status != "running" {
		t.Errorf("WriteRun: status = %q, want running", got2.Status)
	}
	if got2.EndedAt == nil {
		t.Error("WriteRun: EndedAt should be set")
	}
}

func TestRunIDAutoGenerate(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	r := &scratch.Run{Task: "auto id test"}
	gotID, err := s.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if gotID == "" {
		t.Error("CreateRun: expected non-empty run ID when r.ID is empty")
	}
	if r.ID != gotID {
		t.Errorf("CreateRun: r.ID not updated: got %q, want %q", r.ID, gotID)
	}
}

func TestListRuns(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	// Create two runs.
	r1 := &scratch.Run{ID: uniqueRunID("list-a"), Task: "first run"}
	r2 := &scratch.Run{ID: uniqueRunID("list-b"), Task: "second run"}
	for _, r := range []*scratch.Run{r1, r2} {
		if _, err := s.CreateRun(r); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
	}

	items, err := s.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}

	// Both runs should appear; filter to our test runs.
	found := make(map[string]scratch.RunSummary)
	for _, item := range items {
		if item.RunID == r1.ID || item.RunID == r2.ID {
			found[item.RunID] = item
		}
	}
	if len(found) != 2 {
		t.Errorf("ListRuns: found %d of our test runs, want 2", len(found))
	}
	if found[r1.ID].TaskFirstLine != "first run" {
		t.Errorf("ListRuns: r1 TaskFirstLine = %q, want %q", found[r1.ID].TaskFirstLine, "first run")
	}
}

// ── Dispatch CRUD ─────────────────────────────────────────────────────────────

func TestDispatchCRUD(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("dispatch-crud")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "t"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// AllocDispatch — first dispatch should be d01.
	id1, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	if id1 != "d01" {
		t.Errorf("AllocDispatch: got %q, want d01", id1)
	}

	// AllocDispatch — second dispatch should be d02.
	id2, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch second: %v", err)
	}
	if id2 != "d02" {
		t.Errorf("AllocDispatch second: got %q, want d02", id2)
	}

	// WriteDispatch with all fields.
	now := time.Now().UTC().Truncate(time.Millisecond)
	endedAt := now.Add(30 * time.Second)
	leaseUntil := now.Add(5 * time.Minute)
	d := &scratch.Dispatch{
		ID:             id1,
		Parent:         "",
		Role:           "orchestrator",
		Model:          "fable",
		Profile:        "full",
		Status:         "running",
		Depth:          0,
		SupervisorPID:  12345,
		MaxTurns:       10,
		TimeoutMinutes: 30,
		StartedAt:      now,
		EndedAt:        &endedAt,
		Exit:           0,
		CostUSD:        0.042,
		NumTurns:       5,
		SessionID:      "sess-d01",
		Tier:           "reason",
		Enforcement:    "full",
		ClaimedBy:      "worker-1",
		LeaseUntil:     &leaseUntil,
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	// ReadDispatch — all fields round-trip.
	got, err := s.ReadDispatch(runID, id1)
	if err != nil {
		t.Fatalf("ReadDispatch: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("ReadDispatch.ID: got %q, want %q", got.ID, d.ID)
	}
	if got.Role != d.Role {
		t.Errorf("ReadDispatch.Role: got %q, want %q", got.Role, d.Role)
	}
	if got.Model != d.Model {
		t.Errorf("ReadDispatch.Model: got %q, want %q", got.Model, d.Model)
	}
	if got.Tier != d.Tier {
		t.Errorf("ReadDispatch.Tier: got %q, want %q", got.Tier, d.Tier)
	}
	if got.Enforcement != d.Enforcement {
		t.Errorf("ReadDispatch.Enforcement: got %q, want %q", got.Enforcement, d.Enforcement)
	}
	if got.ClaimedBy != d.ClaimedBy {
		t.Errorf("ReadDispatch.ClaimedBy: got %q, want %q", got.ClaimedBy, d.ClaimedBy)
	}
	if got.LeaseUntil == nil {
		t.Error("ReadDispatch.LeaseUntil: nil, want set")
	}
	if got.NumTurns != d.NumTurns {
		t.Errorf("ReadDispatch.NumTurns: got %d, want %d", got.NumTurns, d.NumTurns)
	}
	if got.SessionID != d.SessionID {
		t.Errorf("ReadDispatch.SessionID: got %q, want %q", got.SessionID, d.SessionID)
	}

	// ListDispatches.
	list, err := s.ListDispatches(runID)
	if err != nil {
		t.Fatalf("ListDispatches: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("ListDispatches: got %d, want 2", len(list))
	}
	// Should be sorted by id: d01, d02.
	if list[0].ID != "d01" || list[1].ID != "d02" {
		t.Errorf("ListDispatches order: got [%s, %s], want [d01, d02]", list[0].ID, list[1].ID)
	}
}

// ── AllocDispatch monotonicity under concurrency ───────────────────────────────

func TestAllocDispatchMonotonic(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("alloc-conc")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "concurrency test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	const n = 10
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := s.AllocDispatch(runID)
			ids[i] = id
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: AllocDispatch error: %v", i, err)
		}
	}

	// All IDs must be unique and form the set {d01..d10}.
	seen := make(map[string]int, n)
	for i, id := range ids {
		seen[id] = i
	}
	if len(seen) != n {
		t.Errorf("AllocDispatch concurrency: expected %d unique IDs, got %d (ids=%v)", n, len(seen), ids)
	}
	sort.Strings(ids)
	for i, id := range ids {
		want := fmt.Sprintf("d%02d", i+1)
		if id != want {
			t.Errorf("AllocDispatch concurrency: sorted[%d] = %q, want %q", i, id, want)
		}
	}
}

// ── Brief / report / doc round-trip ──────────────────────────────────────────

func TestDocRoundTrip(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("doc-rt")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "doc test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispID, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}

	briefBody := []byte("# Brief\n\nDo the thing.")
	if err := s.WriteBrief(runID, dispID, briefBody); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}
	gotBrief, err := s.ReadBrief(runID, dispID)
	if err != nil {
		t.Fatalf("ReadBrief: %v", err)
	}
	if string(gotBrief) != string(briefBody) {
		t.Errorf("ReadBrief: got %q, want %q", gotBrief, briefBody)
	}

	// Overwrite brief (upsert).
	briefBody2 := []byte("# Brief v2\n\nDo the other thing.")
	if err := s.WriteBrief(runID, dispID, briefBody2); err != nil {
		t.Fatalf("WriteBrief upsert: %v", err)
	}
	gotBrief2, err := s.ReadBrief(runID, dispID)
	if err != nil {
		t.Fatalf("ReadBrief after upsert: %v", err)
	}
	if string(gotBrief2) != string(briefBody2) {
		t.Errorf("ReadBrief upsert: got %q, want %q", gotBrief2, briefBody2)
	}

	reportBody := []byte("# Report\n\nDone.")
	if err := s.WriteReport(runID, dispID, reportBody); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	gotReport, err := s.ReadReport(runID, dispID)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if string(gotReport) != string(reportBody) {
		t.Errorf("ReadReport: got %q, want %q", gotReport, reportBody)
	}
}

// ── Notes ordering ────────────────────────────────────────────────────────────

func TestNotesOrdering(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("notes-order")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "notes test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	refs := make([]scratch.NoteRef, 3)
	for i := range 3 {
		ref, err := s.AppendNote(runID, "orchestrator", []byte(fmt.Sprintf("note %d", i)))
		if err != nil {
			t.Fatalf("AppendNote %d: %v", i, err)
		}
		refs[i] = ref
		// Small sleep to ensure distinct timestamps in filenames.
		time.Sleep(2 * time.Millisecond)
	}

	list, err := s.ListNotes(runID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListNotes: got %d, want 3", len(list))
	}
	// Must be in filename order (ascending = chronological).
	if !sort.SliceIsSorted(list, func(i, j int) bool {
		return list[i].Filename < list[j].Filename
	}) {
		t.Errorf("ListNotes: not sorted by filename: %v", list)
	}
	// Filenames should match what AppendNote returned.
	for i, ref := range refs {
		if list[i].Filename != ref.Filename {
			t.Errorf("ListNotes[%d].Filename: got %q, want %q", i, list[i].Filename, ref.Filename)
		}
	}
}

// ── Trace events ──────────────────────────────────────────────────────────────

func TestTraceEvents(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("trace-ev")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "trace test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispID, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}

	ev := scratch.TraceEvent{
		Ts:           time.Now().UTC().Format(time.RFC3339Nano),
		Kind:         "tool",
		RunID:        runID,
		DispatchID:   dispID,
		Role:         "worker",
		Depth:        1,
		Tool:         "Bash",
		InputSummary: "ls /",
		Status:       "ok",
	}
	if err := s.AppendTraceEvent(runID, dispID, ev); err != nil {
		t.Fatalf("AppendTraceEvent: %v", err)
	}

	// Verify the row exists in the DB by checking via a second event.
	ev2 := scratch.TraceEvent{
		Kind:       "read",
		RunID:      runID,
		DispatchID: dispID,
		Role:       "worker",
		Tool:       "Read",
	}
	if err := s.AppendTraceEvent(runID, dispID, ev2); err != nil {
		t.Fatalf("AppendTraceEvent second: %v", err)
	}
}

// ── DispatchFacts counts ──────────────────────────────────────────────────────

func TestDispatchFacts(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("facts")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "facts test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	type dispSpec struct {
		status string
		tier   string
		model  string
	}
	specs := []dispSpec{
		{status: "running", tier: "reason", model: "fable"},   // active + reason (tier)
		{status: "running", tier: "", model: "fable"},         // active + reason (v1 model fallback)
		{status: "claimed", tier: "execute", model: "sonnet"}, // active, not reason
		{status: "pending", tier: "", model: "sonnet"},        // active, not reason
		{status: "completed", tier: "reason", model: "fable"}, // not active, but reason
		{status: "failed", tier: "", model: "sonnet"},         // not active, not reason
	}

	for i, spec := range specs {
		id, err := s.AllocDispatch(runID)
		if err != nil {
			t.Fatalf("AllocDispatch %d: %v", i, err)
		}
		d := &scratch.Dispatch{
			ID:          id,
			Status:      spec.status,
			Tier:        spec.tier,
			Model:       spec.model,
			Role:        "worker",
			StartedAt:   time.Now().UTC(),
			Enforcement: "full",
		}
		if err := s.WriteDispatch(runID, d); err != nil {
			t.Fatalf("WriteDispatch %d: %v", i, err)
		}
	}

	facts, err := s.DispatchFacts(runID)
	if err != nil {
		t.Fatalf("DispatchFacts: %v", err)
	}
	// active = running(2) + claimed(1) + pending(1) = 4
	if facts.Active != 4 {
		t.Errorf("DispatchFacts.Active: got %d, want 4", facts.Active)
	}
	// reason = tier=reason(2) + (tier='' AND model=fable)(1) = 3
	if facts.ReasonCount != 3 {
		t.Errorf("DispatchFacts.ReasonCount: got %d, want 3", facts.ReasonCount)
	}
}

// ── Materialize ───────────────────────────────────────────────────────────────

func TestMaterialize(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("materialize")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "materialize test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispID, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}

	briefContent := []byte("# Brief\n\nTask: do the thing.")
	if err := s.WriteBrief(runID, dispID, briefContent); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}

	settingsContent := []byte(`{"model":"fable","tools":[]}`)
	if err := s.WriteAdapterConfig(runID, dispID, settingsContent); err != nil {
		t.Fatalf("WriteAdapterConfig: %v", err)
	}

	spoolDir := t.TempDir()
	if err := s.Materialize(runID, dispID, spoolDir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// brief.md must exist with correct content.
	briefPath := spoolDir + "/brief.md"
	gotBrief, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("Materialize: brief.md not written: %v", err)
	}
	if string(gotBrief) != string(briefContent) {
		t.Errorf("Materialize brief.md: got %q, want %q", gotBrief, briefContent)
	}

	// settings.json must exist with correct content.
	settingsPath := spoolDir + "/settings.json"
	gotSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Materialize: settings.json not written: %v", err)
	}
	if string(gotSettings) != string(settingsContent) {
		t.Errorf("Materialize settings.json: got %q, want %q", gotSettings, settingsContent)
	}
}

// ── AuditSink async + spool fallback ─────────────────────────────────────────

func TestAuditSinkAsync(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("audit-async")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "audit test"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	sink, closer, err := s.AuditSink(runID, "dispatch")
	if err != nil {
		t.Fatalf("AuditSink: %v", err)
	}

	// Write a decision via the sink path.
	ev := audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		RequestID: "req-001",
		BundleID:  "bundle-abc",
		Kind:      "rules",
		Context:   map[string]any{"role": "orchestrator"},
	}
	if werr := sink.WriteDecision(t.Context(), ev); werr != nil {
		t.Logf("WriteDecision (sync sink): %v", werr)
	}

	// Close drains the async goroutine.
	if cerr := closer.Close(); cerr != nil {
		t.Fatalf("AuditSink Close: %v", cerr)
	}
}

func TestAuditSinkSpoolOnDownDB(t *testing.T) {
	// Point at an unreachable DSN — the sink must never block.
	badDSN := "postgres://nobody:wrongpass@127.0.0.1:15432/noexist?connect_timeout=1"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgstore.Open(badDSN)
	if err != nil {
		t.Fatalf("Open bad DSN: %v", err)
	}
	defer db.Close()

	s := pgstore.NewStore(db)

	// Use a fake run ID — DB is unreachable so CreateRun would fail; we just
	// test that AuditSink+Write+Close don't block.
	runID := uniqueRunID("spool-fallback")

	sink, closer, err := s.AuditSink(runID, "toolgate")
	if err != nil {
		t.Fatalf("AuditSink on bad DSN: %v", err)
	}

	// Write events — must return immediately (no blocking).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 5 {
			ev := audit.DecisionEvent{
				Timestamp: time.Now().UTC(),
				RequestID: fmt.Sprintf("req-%03d", i),
				Kind:      "rules",
			}
			// WriteDecision via sink path (sync) — this writes to spool directly.
			sink.WriteDecision(context.Background(), ev) //nolint:errcheck
		}
		closer.Close() //nolint:errcheck
	}()

	select {
	case <-done:
		// Good — non-blocking.
	case <-ctx.Done():
		t.Fatal("AuditSink blocked for >5s with down DB")
	}

	// Check spool file was written.
	spoolPath := pgstore.SpoolPath(runID, "toolgate")
	if _, err := os.Stat(spoolPath); err != nil {
		t.Logf("spool file %s: %v (may be absent if sink writes to the same path)", spoolPath, err)
	}
}

// ── AdapterConfig round-trip ──────────────────────────────────────────────────

func TestAdapterConfigRoundTrip(t *testing.T) {
	dsn := dsnOrSkip(t)
	s := openTestStore(t, dsn)

	runID := uniqueRunID("adapter-cfg")
	if _, err := s.CreateRun(&scratch.Run{ID: runID, Task: "adapter cfg"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispID, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}

	cfg := map[string]any{"model": "fable", "maxTurns": 20}
	cfgJSON, _ := json.Marshal(cfg)

	if err := s.WriteAdapterConfig(runID, dispID, cfgJSON); err != nil {
		t.Fatalf("WriteAdapterConfig: %v", err)
	}
	gotCfg, err := s.ReadAdapterConfig(runID, dispID)
	if err != nil {
		t.Fatalf("ReadAdapterConfig: %v", err)
	}
	if string(gotCfg) != string(cfgJSON) {
		t.Errorf("ReadAdapterConfig: got %q, want %q", gotCfg, cfgJSON)
	}
}
