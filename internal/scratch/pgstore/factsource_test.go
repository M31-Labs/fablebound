package pgstore_test

// TestFactsourceGovernsDispatch proves that arbiter can govern dispatch from
// live PostgreSQL rows via the arbiter factsource postgres loader and the
// dispatch_facts view added in schema version 3.
//
// Flow:
//   1. Fresh schema migrate (idempotent).
//   2. Seed a run with reason_budget=2, then insert 2 reason-tier dispatches.
//   3. Load facts for that run via the arbiter postgres factsource loader
//      (queries the dispatch_facts view).
//   4. Inject loader-returned counts into a DispatchRequest.
//   5. Evaluate the embedded dispatch.arb.
//   6. Assert Deny with rule == DenyReasonBudget.
//
// Skipped unless TILLER_TEST_PG_DSN is set.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	// Importing factsource registers the postgres:// loader scheme via init().
	"m31labs.dev/arbiter/expert/factsource"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/pgstore"
)

func TestFactsourceGovernsDispatch(t *testing.T) {
	dsn := os.Getenv("TILLER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TILLER_TEST_PG_DSN not set — skipping postgres integration test")
	}

	ctx := context.Background()

	// 1. Apply schema (idempotent; adds dispatch_facts view at version 3).
	v, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v < pgstore.SchemaVersion {
		t.Fatalf("schema version: got %d, want >= %d", v, pgstore.SchemaVersion)
	}

	// 2. Seed: one run with reason_budget=2 and exactly 2 reason dispatches.
	db, err := pgstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := pgstore.NewStore(db)

	runID := fmt.Sprintf("factsource-test-%d", time.Now().UnixNano())
	_, err = s.CreateRun(&scratch.Run{
		ID:           runID,
		Task:         "factsource governance proof",
		Status:       "running",
		ReasonBudget: 2, // reason_budget = 2
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Insert 2 reason-tier dispatches (tier='reason', status='running' → active + reason).
	for i := range 2 {
		id, err := s.AllocDispatch(runID)
		if err != nil {
			t.Fatalf("AllocDispatch %d: %v", i, err)
		}
		if err := s.WriteDispatch(runID, &scratch.Dispatch{
			ID:          id,
			Role:        "orchestrator",
			Model:       "fable",
			Tier:        "reason",
			Status:      "running",
			StartedAt:   time.Now().UTC(),
			Enforcement: "full",
		}); err != nil {
			t.Fatalf("WriteDispatch %d: %v", i, err)
		}
	}

	// 3. Load facts via the arbiter postgres factsource loader.
	//    The loader queries: SELECT type, key, COALESCE(fields,'{}'), version
	//                        FROM public.dispatch_facts ORDER BY type, key
	//    We filter to our run_id after loading.
	factsURI := dsn + "?table=dispatch_facts&schema=public&mode=merge"
	facts, err := factsource.Load(factsURI)
	if err != nil {
		t.Fatalf("factsource.Load: %v", err)
	}

	// Find the fact row for our run.
	var runFact *factsource.Fact
	for i := range facts {
		if facts[i].Key == runID {
			runFact = &facts[i]
			break
		}
	}
	if runFact == nil {
		t.Fatalf("no dispatch_facts row found for run_id %q (got %d rows)", runID, len(facts))
	}

	// 4. Extract counts from the fact fields and build a DispatchRequest.
	//    JSON numbers unmarshal as float64.
	activeDispatches := int(toFloat64(runFact.Fields["active_dispatches"]))
	reasonDispatches := int(toFloat64(runFact.Fields["reason_dispatches"]))
	reasonBudget := int(toFloat64(runFact.Fields["reason_budget"]))

	t.Logf("dispatch_facts for %s: active=%d reason=%d budget=%d",
		runID, activeDispatches, reasonDispatches, reasonBudget)

	if reasonDispatches < reasonBudget {
		t.Fatalf("pre-condition: reason_dispatches=%d must be >= reason_budget=%d",
			reasonDispatches, reasonBudget)
	}

	// Construct a DispatchRequest that will trigger DenyReasonBudget.
	// The request asks for a reason-tier orchestrator dispatch from a root
	// orchestrator at depth 0 — only DenyReasonBudget (priority 2) should fire.
	req := policy.DispatchRequest{
		Role:         "orchestrator",
		Tier:         "reason",
		CallerRole:   "orchestrator",
		CallerDepth:  0,
		RunID:        runID,
		ActiveCount:  activeDispatches,
		ReasonCount:  reasonDispatches,
		ReasonBudget: reasonBudget,
		MaxDepth:     4, // keep DenyDepthBeyondPolicy quiet; this test targets the budget rule
	}

	// 5. Evaluate the embedded dispatch.arb policy.
	loaded, err := policy.Load("dispatch", "")
	if err != nil {
		t.Fatalf("policy.Load: %v", err)
	}
	result, err := policy.EvalDispatch(loaded, req)
	if err != nil {
		t.Fatalf("EvalDispatch: %v", err)
	}

	t.Logf("EvalDispatch verdict=%s rule=%q reason=%q", result.Verdict, result.Rule, result.Reason)

	// 6. Assert Deny with rule DenyReasonBudget.
	if result.Verdict != policy.VerdictDeny {
		t.Errorf("verdict: got %s, want Deny", result.Verdict)
	}
	if result.Rule != "DenyReasonBudget" {
		t.Errorf("rule: got %q, want DenyReasonBudget", result.Rule)
	}
}

// toFloat64 coerces a JSON-decoded number to float64.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}
