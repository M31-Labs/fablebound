package pool

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/sandbox"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// ─── stub adapter ─────────────────────────────────────────────────────────────

// stubAdapter is a no-process adapter for tests. It writes a minimal report via
// the Store and returns Result{Status: "completed"} without spawning any subprocess.
type stubAdapter struct {
	// totalRuns counts every call to Run across all dispatches.
	totalRuns atomic.Int64
}

func newStubAdapter() *stubAdapter { return &stubAdapter{} }

func (a *stubAdapter) Name() string        { return "stub" }
func (a *stubAdapter) Enforcement() string { return "full" }

// degradedStubAdapter is a stub adapter that reports enforcement="degraded".
// Used to verify that evalGate re-derives enforcement from the adapter rather
// than trusting the persisted record field.
type degradedStubAdapter struct{}

func (a *degradedStubAdapter) Name() string        { return "degraded-stub" }
func (a *degradedStubAdapter) Enforcement() string { return "degraded" }
func (a *degradedStubAdapter) Prepare(_ context.Context, _ *adapter.DispatchSpec) error {
	return nil
}
func (a *degradedStubAdapter) Run(_ context.Context, _ *adapter.DispatchSpec) (*adapter.Result, error) {
	return &adapter.Result{Status: "completed"}, nil
}

func (a *stubAdapter) Prepare(_ context.Context, _ *adapter.DispatchSpec) error {
	return nil
}

func (a *stubAdapter) Run(_ context.Context, s *adapter.DispatchSpec) (*adapter.Result, error) {
	a.totalRuns.Add(1)
	report := []byte(fmt.Sprintf("stub report for %s/%s run=%d", s.RunID, s.DispatchID, a.totalRuns.Load()))
	if err := s.Store.WriteReport(s.RunID, s.DispatchID, report); err != nil {
		return nil, fmt.Errorf("stub: write report: %w", err)
	}
	return &adapter.Result{Status: "completed", CostUSD: 0.001}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// openStore opens an fsstore in a fresh temp directory.
func openStore(t *testing.T) (scratch.Store, string) {
	t.Helper()
	runsBase := t.TempDir()
	return fsstore.Open(runsBase), runsBase
}

// buildPool creates a Pool for testing using the given stub adapter.
// MaxConcurrent=1 serialises execution so we never exceed DenyConcurrencyCap(4)
// in the dispatch gate. DispatchPolicy is loaded from the embedded defaults.
func buildPool(t *testing.T, st scratch.Store, runsBase string, stub *stubAdapter, journalPath string, pollInterval time.Duration) *Pool {
	t.Helper()
	reg := adapter.NewRegistry()
	reg.Register(stub)
	p, err := New(Options{
		Store:           st,
		RunsBase:        runsBase,
		AdapterRegistry: reg,
		PollInterval:    pollInterval,
		MaxConcurrent:   1, // serialise to stay under DenyConcurrencyCap (cap=4)
		JournalPath:     journalPath,
		LeaseDuration:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	return p
}

// seedRun creates a run with n pending dispatches routed to the "stub" adapter.
func seedRun(t *testing.T, st scratch.Store, n int) (runID string, dispatchIDs []string) {
	t.Helper()
	var err error
	runID, err = st.CreateRun(&scratch.Run{
		Task:         "pool test task",
		Workspace:    t.TempDir(),
		Status:       "running",
		ReasonBudget: 10, // generous reason budget
		MaxDepth:     8,  // generous depth
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for i := range n {
		did, err := st.AllocDispatch(runID)
		if err != nil {
			t.Fatalf("AllocDispatch[%d]: %v", i, err)
		}
		d := &scratch.Dispatch{
			ID:      did,
			Role:    "worker",
			Model:   "stub-model",
			Profile: "execution",
			Status:  "pending",
			Depth:   1,
			Tier:    "execute",
			Adapter: "stub",
		}
		if err := st.WriteDispatch(runID, d); err != nil {
			t.Fatalf("WriteDispatch[%d]: %v", i, err)
		}
		if err := st.WriteBrief(runID, did, []byte("test brief")); err != nil {
			t.Fatalf("WriteBrief[%d]: %v", i, err)
		}
		dispatchIDs = append(dispatchIDs, did)
	}
	return runID, dispatchIDs
}

// countTerminal counts dispatches that have reached a terminal state.
func countTerminal(t *testing.T, st scratch.Store, runID string, dispatchIDs []string) int {
	t.Helper()
	n := 0
	for _, did := range dispatchIDs {
		d, err := st.ReadDispatch(runID, did)
		if err == nil && d.IsTerminal() {
			n++
		}
	}
	return n
}

// waitAllTerminal polls until all dispatchIDs are terminal or timeout.
func waitAllTerminal(t *testing.T, st scratch.Store, runID string, dispatchIDs []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countTerminal(t, st, runID, dispatchIDs) == len(dispatchIDs) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout after %v: only %d/%d dispatches terminal",
		timeout, countTerminal(t, st, runID, dispatchIDs), len(dispatchIDs))
}

// waitAtLeastTerminal polls until at least n dispatchIDs are terminal or timeout.
func waitAtLeastTerminal(t *testing.T, st scratch.Store, runID string, dispatchIDs []string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countTerminal(t, st, runID, dispatchIDs) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout after %v: only %d/%d dispatches terminal (want >= %d)",
		timeout, countTerminal(t, st, runID, dispatchIDs), len(dispatchIDs), n)
}

// journalDeliveredCount returns the number of "delivered" entries in the journal.
func journalDeliveredCount(t *testing.T, journalPath string) int {
	t.Helper()
	entries, err := readJSONLFile(journalPath)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e["event"] == "delivered" {
			n++
		}
	}
	return n
}

// journalDeliveredIDs returns the set of delivery IDs that have a "delivered" event.
func journalDeliveredIDs(t *testing.T, journalPath string) map[string]int {
	t.Helper()
	entries, err := readJSONLFile(journalPath)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}
	ids := make(map[string]int)
	for _, e := range entries {
		if e["event"] != "delivered" {
			continue
		}
		delivery, _ := e["delivery"].(map[string]any)
		if delivery == nil {
			continue
		}
		id, _ := delivery["id"].(string)
		if id != "" {
			ids[id]++
		}
	}
	return ids
}

// ─── acceptance tests ─────────────────────────────────────────────────────────

// TestPoolDrainsQueue: 5 pending dispatches → 5 completed, journal has 5 entries.
func TestPoolDrainsQueue(t *testing.T) {
	st, runsBase := openStore(t)
	journalPath := runsBase + "/pool-journal.jsonl"

	stub := newStubAdapter()
	runID, dispatchIDs := seedRun(t, st, 5)

	p := buildPool(t, st, runsBase, stub, journalPath, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	waitAllTerminal(t, st, runID, dispatchIDs, 15*time.Second)
	cancel()

	if err := <-done; err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("Pool.Run error: %v", err)
	}

	// Assert all 5 dispatches completed.
	for _, did := range dispatchIDs {
		d, err := st.ReadDispatch(runID, did)
		if err != nil {
			t.Fatalf("ReadDispatch %s: %v", did, err)
		}
		if d.Status != "completed" {
			t.Errorf("dispatch %s status=%q, want completed", did, d.Status)
		}
	}

	// Assert journal has 5 "delivered" entries.
	n := journalDeliveredCount(t, journalPath)
	if n != 5 {
		t.Errorf("journal delivered=%d, want 5", n)
	}
}

// TestPoolRestartNoDoubleExec: drain 2, stop, restart same journal, drain rest.
// Each dispatch is executed exactly once (no double execution).
func TestPoolRestartNoDoubleExec(t *testing.T) {
	st, runsBase := openStore(t)
	journalPath := runsBase + "/pool-journal.jsonl"

	// Two separate stub adapters track executions per run.
	stub1 := newStubAdapter()
	stub2 := newStubAdapter()

	runID, dispatchIDs := seedRun(t, st, 5)

	// Run 1: drain at least 2 dispatches, then stop.
	p1 := buildPool(t, st, runsBase, stub1, journalPath, 50*time.Millisecond)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel1()

	done1 := make(chan error, 1)
	go func() { done1 <- p1.Run(ctx1) }()

	waitAtLeastTerminal(t, st, runID, dispatchIDs, 2, 15*time.Second)
	cancel1()
	<-done1

	runsAfterFirst := stub1.totalRuns.Load()
	t.Logf("after first pool run: %d dispatches executed, %d terminal",
		runsAfterFirst, countTerminal(t, st, runID, dispatchIDs))

	// Run 2: restart with same journal, drain all remaining.
	p2 := buildPool(t, st, runsBase, stub2, journalPath, 50*time.Millisecond)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()

	done2 := make(chan error, 1)
	go func() { done2 <- p2.Run(ctx2) }()

	waitAllTerminal(t, st, runID, dispatchIDs, 15*time.Second)
	cancel2()
	<-done2

	// All 5 dispatches must be completed.
	for _, did := range dispatchIDs {
		d, err := st.ReadDispatch(runID, did)
		if err != nil {
			t.Fatalf("ReadDispatch %s: %v", did, err)
		}
		if d.Status != "completed" {
			t.Errorf("dispatch %s status=%q, want completed", did, d.Status)
		}
	}

	// Total stub runs = 5 (each dispatch executed exactly once).
	totalRuns := stub1.totalRuns.Load() + stub2.totalRuns.Load()
	if totalRuns != 5 {
		t.Errorf("total stub runs=%d, want 5 (no double execution)", totalRuns)
	}

	// Journal must have exactly 5 "delivered" entries with unique delivery IDs.
	ids := journalDeliveredIDs(t, journalPath)
	if len(ids) != 5 {
		t.Errorf("journal unique delivered IDs=%d, want 5", len(ids))
	}
	for id, count := range ids {
		if count > 1 {
			t.Errorf("delivery %s appears %d times in journal (want 1)", id, count)
		}
	}
}

// TestEvalGateEnforcementRederivation proves that evalGate re-derives
// enforcement from the adapter registry rather than trusting the persisted
// record field. A dispatch record that claims enforcement="full" but routes to
// the "degraded-stub" adapter (Enforcement()="degraded") must be denied with
// DenyDegradedInsight when it requests a scrutiny-tier role.
func TestEvalGateEnforcementRederivation(t *testing.T) {
	st, runsBase := openStore(t)

	// Create a run with generous budgets.
	runID, err := st.CreateRun(&scratch.Run{
		Task:         "spoofed enforcement test",
		Workspace:    t.TempDir(),
		Status:       "running",
		ReasonBudget: 10,
		MaxDepth:     8,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Allocate and write a dispatch that claims enforcement="full" but routes to
	// the degraded adapter. Tier is "scrutiny" so DenyDegradedInsight must fire
	// once enforcement is correctly re-derived to "degraded".
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	d := &scratch.Dispatch{
		ID:          dispatchID,
		Role:        "investigator",
		Model:       "stub-model",
		Profile:     "readonly",
		Status:      "pending",
		Depth:       1,
		Tier:        "scrutiny",
		Adapter:     "degraded-stub", // real adapter has Enforcement()="degraded"
		Enforcement: "full",          // spoofed record value — gate must ignore this
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}
	if err := st.WriteBrief(runID, dispatchID, []byte("scrutiny brief")); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}

	// Register only the degraded-stub adapter.
	reg := adapter.NewRegistry()
	reg.Register(&degradedStubAdapter{})

	journalPath := runsBase + "/enforce-journal.jsonl"
	p, err := New(Options{
		Store:           st,
		RunsBase:        runsBase,
		AdapterRegistry: reg,
		PollInterval:    20 * time.Millisecond,
		MaxConcurrent:   1,
		JournalPath:     journalPath,
		LeaseDuration:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Wait for the dispatch to reach a terminal state (expect "denied").
	waitAllTerminal(t, st, runID, []string{dispatchID}, 8*time.Second)
	cancel()
	<-done

	// The dispatch must be denied, not completed.
	finalD, readErr := st.ReadDispatch(runID, dispatchID)
	if readErr != nil {
		t.Fatalf("ReadDispatch: %v", readErr)
	}
	if finalD.Status != "denied" {
		t.Errorf("dispatch status=%q, want denied (DenyDegradedInsight should have fired)", finalD.Status)
	}
	if finalD.DenyReason == "" {
		t.Error("dispatch DenyReason is empty; expected DenyDegradedInsight reason")
	}
	t.Logf("dispatch %s: status=%s deny_reason=%q", dispatchID, finalD.Status, finalD.DenyReason)
}

func TestEvalGateSandboxFactsDoNotPromoteRequestedProcessSandbox(t *testing.T) {
	st, runsBase := openStore(t)
	runID, err := st.CreateRun(&scratch.Run{
		Task:         "requested sandbox facts test",
		Workspace:    t.TempDir(),
		Status:       "running",
		ReasonBudget: 10,
		MaxDepth:     8,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	d := &scratch.Dispatch{
		ID:      dispatchID,
		Role:    "investigator",
		Model:   "stub-model",
		Profile: "readonly",
		Status:  "pending",
		Depth:   1,
		Tier:    "scrutiny",
		Adapter: "degraded-stub",
		Sandbox: &sandbox.Record{
			Mode:    sandbox.ModeProcess,
			Profile: "readonly",
			Status:  sandbox.StatusRequested,
			Runner:  "process",
			Horizon: []sandbox.HorizonManifest{{Path: "observe.json"}},
		},
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	reg := adapter.NewRegistry()
	reg.Register(&degradedStubAdapter{})
	p, err := New(Options{
		Store:           st,
		RunsBase:        runsBase,
		AdapterRegistry: reg,
		LeaseDuration:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}

	allowed, gr, err := p.evalGate(context.Background(), runID, dispatchID)
	if err != nil {
		t.Fatalf("evalGate: %v", err)
	}
	if allowed {
		t.Fatal("evalGate allowed scrutiny degraded adapter with requested process sandbox")
	}
	if gr.req.Enforcement != "degraded" {
		t.Errorf("DispatchRequest.Enforcement=%q, want degraded", gr.req.Enforcement)
	}
	if gr.req.SandboxMode != "process" {
		t.Errorf("DispatchRequest.SandboxMode=%q, want process", gr.req.SandboxMode)
	}
	if gr.req.SandboxProfile != "readonly" {
		t.Errorf("DispatchRequest.SandboxProfile=%q, want readonly", gr.req.SandboxProfile)
	}
	if gr.req.HorizonManifests != 1 {
		t.Errorf("DispatchRequest.HorizonManifests=%d, want 1", gr.req.HorizonManifests)
	}
}

func TestEvalGatePromotesOnlyConstrainingActiveSandbox(t *testing.T) {
	st, runsBase := openStore(t)
	runID, err := st.CreateRun(&scratch.Run{
		Task:         "active sandbox facts test",
		Workspace:    t.TempDir(),
		Status:       "running",
		ReasonBudget: 10,
		MaxDepth:     8,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	d := &scratch.Dispatch{
		ID:      dispatchID,
		Role:    "investigator",
		Model:   "stub-model",
		Profile: "readonly",
		Status:  "pending",
		Depth:   1,
		Tier:    "scrutiny",
		Adapter: "degraded-stub",
		Sandbox: &sandbox.Record{
			Mode:    sandbox.ModeContainer,
			Profile: "readonly",
			Status:  sandbox.StatusActive,
			Runner:  "bubblewrap",
		},
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	reg := adapter.NewRegistry()
	reg.Register(&degradedStubAdapter{})
	p, err := New(Options{
		Store:           st,
		RunsBase:        runsBase,
		AdapterRegistry: reg,
		LeaseDuration:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}

	allowed, gr, err := p.evalGate(context.Background(), runID, dispatchID)
	if err != nil {
		t.Fatalf("evalGate: %v", err)
	}
	if !allowed {
		t.Fatalf("evalGate denied active constraining sandbox: %s", gr.reason)
	}
	if gr.req.Enforcement != "sandboxed" {
		t.Errorf("DispatchRequest.Enforcement=%q, want sandboxed", gr.req.Enforcement)
	}
	if gr.req.SandboxMode != "container" {
		t.Errorf("DispatchRequest.SandboxMode=%q, want container", gr.req.SandboxMode)
	}
}
