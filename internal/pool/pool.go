// Package pool implements the tiller executor pool: a host-managed singleton
// that drains pending dispatches through the adapter seam.
//
// # Design
//
// The pool embeds arbiter's workflow.Runner as a library. A SourceLoader polls
// the Store for pending dispatches across all active runs and converts them to
// expert.Facts. The dispatch.arb gate re-evaluates each dispatch with
// Queued=true semantics before execution.
//
// # Queued=true gate decision
//
// The queue-time gate already allowed the dispatch with Queued=true. The
// pool-time gate re-evaluates with Queued=true so that DenyDirectSpawnAtDepth
// (which fires only when Queued=false) does not block pool execution. All
// other budget/depth/role rules run normally using live counters.
//
// # Double-finalization decision
//
// For claude-headless, the detached _supervise process calls WriteDispatch with
// the terminal status (completed/failed/halted) and writes the report. The pool's
// WorkerHandler MUST NOT re-write those fields. After the adapter's Run returns
// (which for claude-headless polls until the record is already terminal), the
// pool calls ReleaseDispatch solely to clear the claim file and lease fields.
// The fsstore's ReleaseDispatch reads the current meta.json, clears ClaimedBy
// and LeaseUntil, sets the EndedAt to now (a minor overwrite acceptable because
// the difference is one poll interval), and removes the claim sentinel. All other
// fields (Status, CostUSD, NumTurns, SessionID) survive unchanged from the
// _supervise write.
//
// # Journal deduplication
//
// workflow.Runner's DeliveryLog (RunnerOptions.DeliveryLog) is a JSONL file
// keyed on arbiter delivery IDs that encode <runID>/<dispatchID>. On restart,
// restorePending() replays the journal: deliveries whose last event was
// "delivered" are dropped; those last seen as "dispatching" become ambiguous
// (not auto-replayed). This guarantees each dispatch executes exactly once
// across pool restarts.
package pool

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/expert"
	"m31labs.dev/arbiter/workflow"
	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
)

const (
	defaultLeaseDuration = 2 * time.Minute
	defaultPollInterval  = 5 * time.Second
	defaultMaxConcurrent = 4

	// pendingSource is the source target for the pool's pending-dispatch loader.
	pendingSource = "tiller-dispatches://pending"
)

// Options configures the executor pool.
type Options struct {
	// Store is the scratch store (required).
	Store scratch.Store

	// RunsBase is the absolute path to the runs/ directory.
	// Required for claudeheadless which needs WorkDir = RunsBase/runID.
	RunsBase string

	// AdapterRegistry holds the registered adapters (required).
	AdapterRegistry *adapter.Registry

	// DispatchPolicy is the loaded dispatch policy. If nil it is loaded from
	// ProjectDir (or cwd if ProjectDir is also empty).
	DispatchPolicy *policy.Loaded

	// ProjectDir is used to load dispatch policy when DispatchPolicy is nil.
	ProjectDir string

	// PollInterval between source loads (default 5s).
	PollInterval time.Duration

	// MaxConcurrent is the maximum number of dispatches running at once (default 4).
	MaxConcurrent int

	// JournalPath is the path to the delivery-log JSONL file used for deduplication.
	// Default: .tiller/pool-journal.jsonl in the project directory.
	JournalPath string

	// LeaseDuration is the initial claim lease duration (default 2m).
	LeaseDuration time.Duration

	// RenewInterval is how often the lease is renewed while a dispatch is running
	// (default: LeaseDuration/2). Must be less than LeaseDuration.
	// Zero means use the default (half of LeaseDuration).
	RenewInterval time.Duration

	// ExecutorID overrides the executor identifier used in claim records.
	// Defaults to poolExecutorID() (host+PID). Used in tests to distinguish
	// multiple in-process Pool instances sharing the same PID.
	ExecutorID string
}

// Pool is the executor pool. One instance per host — daemon rule honored.
type Pool struct {
	store           scratch.Store
	runsBase        string
	adapterRegistry *adapter.Registry
	dispatchPolicy  *policy.Loaded
	pollInterval    time.Duration
	maxConcurrent   int
	journalPath     string
	leaseDuration   time.Duration
	renewInterval   time.Duration // resolved to LeaseDuration/2 when zero
	executorID      string        // "" → poolExecutorID() at claim time

	// runIDs tracks the run IDs seen by the source loader (for sweeper).
	runsMu sync.RWMutex
	runIDs map[string]struct{}
}

// New creates a new Pool.
func New(opts Options) (*Pool, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("pool.New: Store is required")
	}
	if opts.AdapterRegistry == nil {
		return nil, fmt.Errorf("pool.New: AdapterRegistry is required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = defaultLeaseDuration
	}
	if opts.RenewInterval <= 0 {
		opts.RenewInterval = opts.LeaseDuration / 2
	}

	pol := opts.DispatchPolicy
	if pol == nil {
		dir := opts.ProjectDir
		if dir == "" {
			dir, _ = os.Getwd()
		}
		var err error
		pol, err = policy.Load("dispatch", dir)
		if err != nil {
			return nil, fmt.Errorf("pool.New: load dispatch policy: %w", err)
		}
	}

	return &Pool{
		store:           opts.Store,
		runsBase:        opts.RunsBase,
		adapterRegistry: opts.AdapterRegistry,
		dispatchPolicy:  pol,
		pollInterval:    opts.PollInterval,
		maxConcurrent:   opts.MaxConcurrent,
		journalPath:     opts.JournalPath,
		leaseDuration:   opts.LeaseDuration,
		renewInterval:   opts.RenewInterval,
		executorID:      opts.ExecutorID,
		runIDs:          make(map[string]struct{}),
	}, nil
}

// Run starts the pool's polling loop and blocks until ctx is cancelled.
// On cancellation it drains in-flight work before returning.
func (p *Pool) Run(ctx context.Context) error {
	wf, err := compilePoolWorkflow()
	if err != nil {
		return fmt.Errorf("pool.Run: compile workflow: %w", err)
	}

	runner, err := workflow.NewRunner(wf, workflow.RunnerOptions{
		Loader: p.sourceLoader(),
		WorkerHandlers: map[arbiter.ArbiterHandlerKind]workflow.WorkerHandler{
			arbiter.ArbiterHandlerExec: workflow.WorkerHandlerFunc(p.executeDispatch),
		},
		MaxConcurrentDeliveries: p.maxConcurrent,
		DeliveryLog:             p.journalPath,
	})
	if err != nil {
		return fmt.Errorf("pool.Run: new runner: %w", err)
	}
	defer runner.Close() //nolint:errcheck

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		if _, tickErr := runner.Tick(ctx); tickErr != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("pool: tick error: %v", tickErr)
		}

		// Sweeper: expire stale leases on all known runs.
		p.runsMu.RLock()
		runIDs := make([]string, 0, len(p.runIDs))
		for id := range p.runIDs {
			runIDs = append(runIDs, id)
		}
		p.runsMu.RUnlock()

		for _, runID := range runIDs {
			expired, expErr := p.store.ExpireLeases(runID)
			if expErr != nil {
				log.Printf("pool: expire leases for run %s: %v", runID, expErr)
				continue
			}
			for _, did := range expired {
				log.Printf("pool: expire lease %s/%s → pending", runID, did)
			}
		}

		select {
		case <-ctx.Done():
			goto drain
		case <-ticker.C:
		}
	}

drain:
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	drained, _, drainErr := runner.Drain(drainCtx)
	if drained > 0 {
		log.Printf("pool: drained %d deliveries on shutdown", drained)
	}
	return drainErr
}

// RunWithSignals starts the pool and handles SIGINT/SIGTERM for graceful shutdown.
func (p *Pool) RunWithSignals() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh
		log.Printf("pool: signal received, draining in-flight work...")
		cancel()
	}()

	return p.Run(ctx)
}

// sourceLoader returns a workflow.SourceLoader for tiller-dispatches://pending.
// It lists all active runs and collects all pending dispatches.
func (p *Pool) sourceLoader() workflow.SourceLoader {
	return func(ctx context.Context, target string) ([]expert.Fact, error) {
		if target != pendingSource {
			return nil, fmt.Errorf("pool: unexpected source target %q", target)
		}

		runs, err := p.store.ListRuns()
		if err != nil {
			return nil, fmt.Errorf("pool: list runs: %w", err)
		}

		// Track seen run IDs for the sweeper.
		p.runsMu.Lock()
		for _, r := range runs {
			p.runIDs[r.RunID] = struct{}{}
		}
		p.runsMu.Unlock()

		var facts []expert.Fact
		for _, r := range runs {
			dispatches, err := p.store.ListPendingDispatches(r.RunID)
			if err != nil {
				log.Printf("pool: list pending dispatches for %s: %v", r.RunID, err)
				continue
			}
			for _, d := range dispatches {
				facts = append(facts, dispatchToFact(r.RunID, d))
			}
		}
		return facts, nil
	}
}

// executeDispatch is the WorkerHandler.Execute for workerKindDispatch.
// It claims, gates, prepares, runs, and releases the dispatch.
func (p *Pool) executeDispatch(ctx context.Context, inv workflow.WorkerInvocation) (workflow.WorkerExecution, error) {
	params := inv.Delivery.Outcome.Params
	runID, _ := params["run_id"].(string)
	dispatchID, _ := params["dispatch_id"].(string)
	adapterName, _ := params["adapter"].(string)

	if runID == "" || dispatchID == "" {
		return workflow.WorkerExecution{}, fmt.Errorf("pool: worker missing run_id/dispatch_id in delivery params")
	}

	executor := p.executorID
	if executor == "" {
		executor = poolExecutorID()
	}

	// ── CAS claim ─────────────────────────────────────────────────────────────
	claimed, err := p.store.ClaimDispatch(runID, dispatchID, executor, p.leaseDuration)
	if err != nil {
		return workflow.WorkerExecution{}, fmt.Errorf("pool: claim %s/%s: %w", runID, dispatchID, err)
	}
	if !claimed {
		// Another worker claimed it; skip silently.
		log.Printf("pool: lost race for %s/%s (already claimed)", runID, dispatchID)
		return workflow.WorkerExecution{
			Outcomes: []expert.Outcome{resultOutcome(runID, dispatchID, "skipped", 0)},
		}, nil
	}
	log.Printf("pool: claim %s/%s", runID, dispatchID)

	// ── Gate evaluation (Queued=true) ──────────────────────────────────────────
	allowed, gateResult, gateErr := p.evalGate(ctx, runID, dispatchID)
	if gateErr != nil {
		p.releaseDispatch(runID, dispatchID, executor, "failed")
		return workflow.WorkerExecution{}, fmt.Errorf("pool: gate %s/%s: %w", runID, dispatchID, gateErr)
	}
	if !allowed {
		log.Printf("pool: gate denied %s/%s: %s", runID, dispatchID, gateResult.reason)
		// Write the deny reason to the dispatch record before releasing.
		if deniedDispatch, readErr := p.store.ReadDispatch(runID, dispatchID); readErr == nil {
			deniedDispatch.DenyReason = gateResult.reason
			_ = p.store.WriteDispatch(runID, deniedDispatch)
		}
		// Write audit line mirroring queue-time deny in cli/dispatch.go.
		p.writeGateDenyAudit(runID, dispatchID, gateResult)
		p.releaseDispatch(runID, dispatchID, executor, "denied")
		return workflow.WorkerExecution{
			Outcomes: []expert.Outcome{resultOutcome(runID, dispatchID, "denied", 0)},
		}, nil
	}

	// ── Read dispatch record for adapter config ────────────────────────────────
	d, err := p.store.ReadDispatch(runID, dispatchID)
	if err != nil {
		p.releaseDispatch(runID, dispatchID, executor, "failed")
		return workflow.WorkerExecution{}, fmt.Errorf("pool: read dispatch %s/%s: %w", runID, dispatchID, err)
	}

	// Transition status to "running".
	d.Status = "running"
	if writeErr := p.store.WriteDispatch(runID, d); writeErr != nil {
		log.Printf("pool: write running %s/%s: %v", runID, dispatchID, writeErr)
	}

	// ── Resolve adapter and compute workDir ──────────────────────────────────
	if adapterName == "" {
		adapterName = d.Adapter
	}
	adpt, adptErr := p.adapterRegistry.Get(adapterName)
	if adptErr != nil {
		p.releaseDispatch(runID, dispatchID, executor, "failed")
		return workflow.WorkerExecution{}, fmt.Errorf("pool: adapter %q not found: %w", adapterName, adptErr)
	}

	spec := buildDispatchSpec(p.store, p.runsBase, runID, d)

	// ── Materialize ───────────────────────────────────────────────────────────
	// Pass spec.WorkDir so pgstore can write brief.md / settings.json on disk.
	// fsstore treats Materialize as a no-op regardless of dir.
	if err := p.store.Materialize(runID, dispatchID, spec.WorkDir); err != nil {
		p.releaseDispatch(runID, dispatchID, executor, "failed")
		return workflow.WorkerExecution{}, fmt.Errorf("pool: materialize %s/%s: %w", runID, dispatchID, err)
	}

	// ── Lease renewer ─────────────────────────────────────────────────────────
	renewCtx, cancelRenew := context.WithCancel(ctx)
	var renewWg sync.WaitGroup
	renewWg.Add(1)
	go func() {
		defer renewWg.Done()
		t := time.NewTicker(p.renewInterval)
		defer t.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-t.C:
				if renewErr := p.store.RenewLease(runID, dispatchID, executor, p.leaseDuration); renewErr != nil {
					log.Printf("pool: renew lease %s/%s: %v", runID, dispatchID, renewErr)
				}
			}
		}
	}()

	// ── Prepare ───────────────────────────────────────────────────────────────
	if prepErr := adpt.Prepare(ctx, spec); prepErr != nil {
		cancelRenew()
		renewWg.Wait()
		p.releaseDispatch(runID, dispatchID, executor, "failed")
		return workflow.WorkerExecution{}, fmt.Errorf("pool: prepare %s/%s: %w", runID, dispatchID, prepErr)
	}

	// ── Run adapter ───────────────────────────────────────────────────────────
	result, runErr := adpt.Run(ctx, spec)
	cancelRenew()
	renewWg.Wait()

	terminalStatus := "failed"
	var costUSD float64
	if result != nil {
		if result.Status != "" {
			terminalStatus = result.Status
		}
		costUSD = result.CostUSD
	}

	// ── Release claim (clear claim file + lease fields) ───────────────────────
	// For claude-headless: _supervise already wrote the terminal status, so
	// ReleaseDispatch only clears ClaimedBy/LeaseUntil/claim-file. EndedAt is
	// overwritten with now() — a minor imprecision (one poll interval).
	p.releaseDispatch(runID, dispatchID, executor, terminalStatus)

	if runErr != nil {
		return workflow.WorkerExecution{}, fmt.Errorf("pool: run %s/%s: %w", runID, dispatchID, runErr)
	}

	log.Printf("pool: complete %s/%s status=%s cost=$%.4f", runID, dispatchID, terminalStatus, costUSD)

	return workflow.WorkerExecution{
		Outcomes: []expert.Outcome{resultOutcome(runID, dispatchID, terminalStatus, costUSD)},
	}, nil
}

// releaseDispatch calls ReleaseDispatch with logging on error.
func (p *Pool) releaseDispatch(runID, dispatchID, executor, status string) {
	if err := p.store.ReleaseDispatch(runID, dispatchID, executor, status); err != nil {
		log.Printf("pool: release dispatch %s/%s: %v", runID, dispatchID, err)
	}
}

// gateResult holds the outcome of evalGate for use in audit + deny recording.
type gateResult struct {
	reason     string // deny reason (empty on allow)
	rule       string // rule name that fired
	req        policy.DispatchRequest
	matchedRes policy.DispatchResult
}

// evalGate evaluates the dispatch policy with Queued=true semantics.
//
// ActiveCount is computed from ListDispatches filtered to "running" only —
// "pending" and "claimed" dispatches are not yet consuming agent resources,
// so they should not count against the DenyConcurrencyCap check. The original
// DispatchFacts.Active includes pending+claimed+running (correct for the
// queue-time gate where the caller is about to spawn), but the pool-time gate
// must not count pending dispatches it is about to execute.
func (p *Pool) evalGate(_ context.Context, runID, dispatchID string) (bool, gateResult, error) {
	d, err := p.store.ReadDispatch(runID, dispatchID)
	if err != nil {
		return false, gateResult{}, fmt.Errorf("read dispatch: %w", err)
	}
	runRec, err := p.store.ReadRun(runID)
	if err != nil {
		return false, gateResult{}, fmt.Errorf("read run: %w", err)
	}
	facts, err := p.store.DispatchFacts(runID)
	if err != nil {
		return false, gateResult{}, fmt.Errorf("dispatch facts: %w", err)
	}

	reasonBudget := runRec.ReasonBudget
	if reasonBudget == 0 {
		reasonBudget = 2
	}
	maxDepth := runRec.MaxDepth
	if maxDepth == 0 {
		maxDepth = 4
	}
	callerDepth := d.Depth - 1
	if callerDepth < 0 {
		callerDepth = 0
	}

	// Compute active count as only "running" dispatches.
	// DispatchFacts.Active includes pending+claimed+running; for the pool gate
	// we use only running dispatches (actual concurrent agent executions).
	activeRunning, err := p.countRunningDispatches(runID)
	if err != nil {
		return false, gateResult{}, fmt.Errorf("count running dispatches: %w", err)
	}

	// Resolve the adapter NOW — before the gate — so that the gate uses the
	// adapter's authoritative Enforcement() value rather than the persisted
	// record field (defense-in-depth against record spoofing).
	// Fall back to the record value only when the adapter name is unknown;
	// that path will fail later in executeDispatch with a clear error, so the
	// gate behaviour is otherwise identical to today.
	adapterNameForGate := d.Adapter
	enforcement := d.Enforcement
	if adpt, adptErr := p.adapterRegistry.Get(adapterNameForGate); adptErr == nil {
		enforcement = adpt.Enforcement()
	} else if enforcement == "" {
		// Unknown adapter AND no record value: default to "full" so pre-v2
		// records continue to work; executeDispatch will surface the real error.
		enforcement = "full"
	}

	req := policy.DispatchRequest{
		Role:         d.Role,
		Tier:         d.Tier,
		Background:   false,
		BriefBytes:   0,
		Queued:       true, // pool always uses queued semantics — DenyDirectSpawnAtDepth must not fire
		Enforcement:  enforcement,
		CallerRole:   "orchestrator",
		CallerDepth:  callerDepth,
		CallerID:     d.Parent,
		RunID:        runID,
		ActiveCount:  activeRunning,
		ReasonCount:  facts.ReasonCount,
		ReasonBudget: reasonBudget,
		MaxDepth:     maxDepth,
	}

	res, err := policy.EvalDispatch(p.dispatchPolicy, req)
	if err != nil {
		return false, gateResult{}, err
	}
	gr := gateResult{req: req, matchedRes: res}
	if res.Verdict != policy.VerdictAllow {
		gr.reason = res.Reason
		gr.rule = res.Rule
		return false, gr, nil
	}
	return true, gr, nil
}

// writeGateDenyAudit writes a DecisionEvent to the run's dispatch audit sink,
// mirroring the queue-time deny path in cli/dispatch.go.
func (p *Pool) writeGateDenyAudit(runID, dispatchID string, gr gateResult) {
	sink, closer, err := p.store.AuditSink(runID, "dispatch")
	if err != nil {
		log.Printf("pool: gate deny audit open %s/%s: %v", runID, dispatchID, err)
		return
	}
	if closer != nil {
		defer closer.Close()
	}
	auditErr := auditlog.DispatchEvent(
		sink,
		runID+"/"+dispatchID,
		p.dispatchPolicy.SHA256,
		gr.req,
		gr.matchedRes.Matched,
		nil, // no strategy decision on deny
		gr.matchedRes.Arbitrace,
	)
	if auditErr != nil {
		log.Printf("pool: gate deny audit write %s/%s: %v", runID, dispatchID, auditErr)
	}
}

// countRunningDispatches returns the number of dispatches with status "running" for runID.
func (p *Pool) countRunningDispatches(runID string) (int, error) {
	dispatches, err := p.store.ListDispatches(runID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range dispatches {
		if d.Status == "running" {
			n++
		}
	}
	return n, nil
}

// poolExecutorID returns a host+PID stable executor identifier.
func poolExecutorID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("pool/%s/%d", host, os.Getpid())
}

// buildDispatchSpec assembles a DispatchSpec from the dispatch record.
func buildDispatchSpec(store scratch.Store, runsBase, runID string, d *scratch.Dispatch) *adapter.DispatchSpec {
	workDir := ""
	if runsBase != "" {
		workDir = filepath.Join(runsBase, runID)
	}
	return &adapter.DispatchSpec{
		Store:      store,
		RunID:      runID,
		DispatchID: d.ID,
		Role:       d.Role,
		Tier:       d.Tier,
		Provider:   d.Provider,
		Model:      d.Model,
		Profile:    d.Profile,
		Depth:      d.Depth,
		MaxTurns:   d.MaxTurns,
		WorkDir:    workDir,
	}
}

// dispatchToFact converts a pending Dispatch to a PendingDispatch expert.Fact.
func dispatchToFact(runID string, d *scratch.Dispatch) expert.Fact {
	return expert.Fact{
		Type: "PendingDispatch",
		Key:  runID + "/" + d.ID,
		Fields: map[string]any{
			"run_id":      runID,
			"dispatch_id": d.ID,
			"role":        d.Role,
			"tier":        d.Tier,
			"adapter":     d.Adapter,
			"depth":       float64(d.Depth),
			"status":      d.Status,
		},
	}
}

// resultOutcome produces a DispatchResult expert.Outcome for WorkerExecution.Outcomes.
// DispatchResult is declared as an outcome in the pool workflow, so WorkerExecution
// must use the Outcomes field (not Facts). Using Facts for an outcome-typed worker
// output causes applyWorkerExecution to fail with "expected outcome output, got facts".
func resultOutcome(runID, dispatchID, status string, costUSD float64) expert.Outcome {
	return expert.Outcome{
		Name: "DispatchResult",
		Params: map[string]any{
			"run_id":      runID,
			"dispatch_id": dispatchID,
			"status":      status,
			"cost_usd":    costUSD,
		},
	}
}

// ─── pool workflow ────────────────────────────────────────────────────────────

// poolWorkflowSrc is the embedded arbiter workflow source for the pool.
//
// Design: one external source (tiller-dispatches://pending) feeds PendingDispatch
// facts. The dispatcher arbiter emits a PendingDispatch outcome for each pending
// dispatch, which the runner delivers via the execute_dispatch worker. The worker
// result feeds back as worker://execute_dispatch facts.
const poolWorkflowSrc = `
outcome PendingDispatch {
	run_id: string
	dispatch_id: string
	role: string
	tier: string
	adapter: string
	depth: number
	status: string
}

outcome DispatchResult {
	run_id: string
	dispatch_id: string
	status: string
	cost_usd: number
}

worker execute_dispatch {
	input PendingDispatch
	output DispatchResult
	exec "pool"
}

arbiter dispatcher {
	poll 5s
	source tiller-dispatches://pending
	source worker://execute_dispatch
	on PendingDispatch worker execute_dispatch
}

expert rule DispatchPending priority 10 per_fact {
	when {
		any d in facts.PendingDispatch { d.status == "pending" }
	}
	then emit PendingDispatch {
		run_id: d.run_id,
		dispatch_id: d.dispatch_id,
		role: d.role,
		tier: d.tier,
		adapter: d.adapter,
		depth: d.depth,
		status: d.status,
	}
}
`

// compilePoolWorkflow compiles the embedded pool workflow.
func compilePoolWorkflow() (*workflow.Workflow, error) {
	return workflow.Compile([]byte(poolWorkflowSrc), workflow.Options{})
}

// DefaultJournalPath returns the default journal path under projectDir.
func DefaultJournalPath(projectDir string) string {
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	return filepath.Join(projectDir, ".tiller", "pool-journal.jsonl")
}
