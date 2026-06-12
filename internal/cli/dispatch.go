package cli

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/hyphae"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/sandbox"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/spawn"
	"m31labs.dev/tiller/internal/storeutil"
	"m31labs.dev/tiller/internal/tier"
)

// makeDispatchHandler returns a handler for `tiller dispatch` that uses the
// provided adapter registry for spawn+poll. Tier-resolved routing is wired:
// the registry lookup selects the adapter based on tier from the policy decision.
func makeDispatchHandler(reg *adapter.Registry) func([]string) error {
	return func(args []string) error {
		return runDispatchWithRegistry(args, reg)
	}
}

// runDispatchWithRegistry is the handler for `tiller dispatch`.
// Caller identity comes from environment; absent ⇒ role="user", depth=0.
func runDispatchWithRegistry(args []string, reg *adapter.Registry) error {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		role       = fs.String("role", "", "agent role (required)")
		tierFlag   = fs.String("tier", "", "tier override: reason|scrutiny|execute (optional, downgrade only)")
		modelAlias = fs.String("model", "", "deprecated: use --tier; fable→reason, opus→scrutiny, sonnet/haiku→execute")
		briefFlag  = fs.String("brief", "", "brief: '-' for stdin, a file path, or literal text")
		background = fs.Bool("background", false, "return immediately after spawn")
		timeout    = fs.String("timeout", "8m", "wait timeout (e.g. 8m, 30s)")
		wait       = fs.Bool("wait", true, "wait for completion (default true; use --background to disable)")
		queue      = fs.Bool("queue", false, "write dispatch record as status:pending and exit 0 (no spawn)")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *role == "" {
		return fmt.Errorf("--role is required")
	}

	// Handle deprecated --model alias: map vendor model names to tiers.
	if *modelAlias != "" && *tierFlag == "" {
		fmt.Fprintf(os.Stderr, "tiller dispatch: --model is deprecated; use --tier instead\n")
		switch *modelAlias {
		case "fable":
			*tierFlag = "reason"
		case "opus":
			*tierFlag = "scrutiny"
		case "sonnet", "haiku":
			*tierFlag = "execute"
		default:
			// Unknown model: pass through as tier string (best effort).
			*tierFlag = *modelAlias
		}
	}

	// Resolve run directory and open store.
	// storeutil.Resolve reads the manifest store field when TILLER_RUN_DIR is set,
	// opening a tee/pg store so that dispatch writes mirror to pg in tee mode.
	// The hook (internal/hook) never calls storeutil; it uses fsstore directly.
	st, runID, storeCloser, err := storeutil.Resolve(nil)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	if storeCloser != nil {
		defer storeCloser()
	}
	if runID == "" {
		return fmt.Errorf("dispatch: TILLER_RUN_DIR is not set")
	}
	// runDir is needed for spawn functions that take a path string.
	runDir := os.Getenv("TILLER_RUN_DIR")

	// Read caller identity from environment.
	callerRole := os.Getenv("TILLER_ROLE")
	if callerRole == "" {
		callerRole = "user"
	}
	callerDepth := 0
	if d := os.Getenv("TILLER_DEPTH"); d != "" {
		fmt.Sscanf(d, "%d", &callerDepth)
	}
	callerID := os.Getenv("TILLER_DISPATCH_ID")

	// Read brief content.
	briefContent, err := readBrief(*briefFlag)
	if err != nil {
		return fmt.Errorf("dispatch: read brief: %w", err)
	}

	// Load run record for reason_budget.
	runRec, err := st.ReadRun(runID)
	if err != nil {
		return fmt.Errorf("dispatch: read run: %w", err)
	}

	reasonBudget := runRec.ReasonBudget
	if reasonBudget == 0 {
		reasonBudget = 2
	}
	maxDepth := runRec.MaxDepth // MaxDepth is policy data per spec §4.3
	if maxDepth == 0 {
		maxDepth = run.DefaultMaxDepth
	}

	// Get active + reason counts via DispatchFacts (subsumes ActiveCount + ReasonCount).
	facts, err := st.DispatchFacts(runID)
	if err != nil {
		return fmt.Errorf("dispatch: dispatch facts: %w", err)
	}

	// Resolve tier config to determine adapter enforcement for the policy gate.
	// This is done before policy evaluation so DenyDegradedInsight can fire.
	tierCfgForReq, _ := tier.Load(filepath.Dir(filepath.Dir(filepath.Dir(runDir))))
	enforcementForReq := "full"
	if tierCfgForReq != nil {
		reqTier := *tierFlag
		if reqTier == "" {
			// Pre-resolve the tier from the role using the default dispatch routing.
			// For policy gate purposes we use a best-effort mapping identical to
			// DispatchRoute strategy: reason roles → "reason", scrutiny → "scrutiny",
			// execute → "execute". If we can't determine, default to "execute".
			reqTier = roleToDefaultTier(*role)
		}
		if cand, err := tierCfgForReq.Resolve(reqTier, runIDBucket(runID)); err == nil {
			if cand.Adapter == "command" {
				enforcementForReq = "degraded"
			}
		}
	}

	sandboxForReq := sandbox.Plan(roleToDefaultProfile(*role), enforcementForReq)
	enforcementForReq = sandbox.EffectiveEnforcement(enforcementForReq, sandboxForReq)

	// Build dispatch request.
	req := policy.DispatchRequest{
		Role:        *role,
		Tier:        *tierFlag,
		Background:  *background,
		BriefBytes:  len(briefContent),
		Queued:      *queue,
		Enforcement: enforcementForReq,
		SandboxMode: sandboxMode(sandboxForReq),
		SandboxProfile: sandboxProfile(
			sandboxForReq,
			roleToDefaultProfile(*role),
		),
		HorizonManifests: sandboxHorizonManifests(sandboxForReq),
		CallerRole:       callerRole,
		CallerDepth:      callerDepth,
		CallerID:         callerID,
		RunID:            runID,
		ActiveCount:      facts.Active,
		ReasonCount:      facts.ReasonCount,
		ReasonBudget:     reasonBudget,
		MaxDepth:         maxDepth,
	}

	// Load and evaluate dispatch policy.
	projectDir := filepath.Dir(filepath.Dir(filepath.Dir(runDir))) // 3 levels up from runs/<id>
	loaded, err := policy.Load("dispatch", projectDir)
	if err != nil {
		return fmt.Errorf("dispatch: load policy: %w", err)
	}

	result, err := policy.EvalDispatch(loaded, req)
	if err != nil {
		return fmt.Errorf("dispatch: evaluate policy: %w", err)
	}

	// Open audit sink via the Store.
	dispatchSink, dispatchCloser, err := st.AuditSink(runID, "dispatch")
	if err != nil {
		return fmt.Errorf("dispatch: open audit: %w", err)
	}
	defer dispatchCloser.Close()

	// Build matched rules from result for the audit event.
	var stratRes *audit.StrategyDecision
	if result.Verdict == policy.VerdictAllow && result.Route.Tier != "" {
		stratRes = &audit.StrategyDecision{
			Strategy: "DispatchRoute",
			Selected: *role,
			Outcome:  result.Route.Profile,
			Params: map[string]any{
				"tier":            result.Route.Tier,
				"profile":         result.Route.Profile,
				"max_turns":       result.Route.MaxTurns,
				"timeout_minutes": result.Route.TimeoutMinutes,
			},
		}
	}

	auditErr := auditlog.DispatchEvent(
		dispatchSink,
		runID+"/"+*role,
		loaded.SHA256,
		req,
		result.Matched,
		stratRes,
		result.Arbitrace,
	)
	if auditErr != nil {
		fmt.Fprintf(os.Stderr, "tiller dispatch: audit write error: %v\n", auditErr)
	}

	// Handle denial.
	if result.Verdict != policy.VerdictAllow {
		return &DenialError{Rule: result.Rule, Reason: result.Reason}
	}

	// Check for Reject route (empty tier).
	if result.Route.Tier == "" {
		return fmt.Errorf("dispatch: policy returned empty tier for role %q (Reject route)", *role)
	}

	// Allocate dispatch id atomically.
	dispatchID, err := st.AllocDispatch(runID)
	if err != nil {
		return fmt.Errorf("dispatch: alloc dispatch: %w", err)
	}

	// Write brief.md via the Store.
	if err := st.WriteBrief(runID, dispatchID, []byte(briefContent)); err != nil {
		return fmt.Errorf("dispatch: write brief.md: %w", err)
	}

	// Resolve adapter, provider, and model via tier.Config.Resolve.
	// bucket = stable FNV-32a hash of runID (preserves v1 §6.3 canary bucketing).
	tierCfg, err := tier.Load(projectDir)
	if err != nil {
		return fmt.Errorf("dispatch: load tier config: %w", err)
	}
	bucket := runIDBucket(runID)
	candidate, err := tierCfg.Resolve(result.Route.Tier, bucket)
	if err != nil {
		return fmt.Errorf("dispatch: resolve tier %q: %w", result.Route.Tier, err)
	}

	adp, err := reg.Get(candidate.Adapter)
	if err != nil {
		return fmt.Errorf("dispatch: resolve adapter %q: %w", candidate.Adapter, err)
	}
	sandboxRec := sandbox.Plan(result.Route.Profile, adp.Enforcement())
	effectiveEnforcement := sandbox.EffectiveEnforcement(adp.Enforcement(), sandboxRec)

	// Build the DispatchSpec for the adapter.
	childDepth := callerDepth + 1
	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Role:       *role,
		Tier:       result.Route.Tier,
		Provider:   candidate.Provider,
		Model:      candidate.Model,
		Profile:    result.Route.Profile,
		WorkDir:    runDir,
		Depth:      childDepth,
		MaxTurns:   result.Route.MaxTurns,
		Timeout:    time.Duration(result.Route.TimeoutMinutes) * time.Minute,
		Sandbox:    sandboxRec,
	}

	// --queue: write dispatch record as status:pending, skip adapter Prepare/Run.
	// Brief and route are fully persisted so an executor can claim and spawn later.
	// --queue --wait polls ReadDispatch until terminal without spawning.
	if *queue {
		now := time.Now()
		d := &scratch.Dispatch{
			ID:             dispatchID,
			Parent:         callerID,
			Role:           *role,
			Model:          candidate.Model,
			Profile:        result.Route.Profile,
			Status:         "pending",
			Depth:          childDepth,
			MaxTurns:       result.Route.MaxTurns,
			TimeoutMinutes: result.Route.TimeoutMinutes,
			StartedAt:      now,
			Tier:           result.Route.Tier,
			Provider:       candidate.Provider,
			Adapter:        candidate.Adapter,
			Enforcement:    effectiveEnforcement,
			Sandbox:        sandboxRec,
		}
		if err := st.WriteDispatch(runID, d); err != nil {
			return fmt.Errorf("dispatch: write dispatch record: %w", err)
		}

		fmt.Fprintf(os.Stderr, "queued %s as %s (role=%s, tier=%s, status=pending)\n",
			dispatchID, *role, *role, result.Route.Tier)
		// --queue exits 0 immediately: print the dispatch id and return.
		// Polling a queued dispatch is done via `tiller await <id>` (P4.3+).
		fmt.Printf("%s\n", dispatchID)
		return nil
	}

	// Prepare: writes settings.json via the Store (present-brief verb is already
	// fulfilled by WriteBrief above; Prepare writes the adapter config).
	if err := adp.Prepare(context.Background(), spec); err != nil {
		return fmt.Errorf("dispatch: prepare adapter: %w", err)
	}

	// Write dispatch record (status: running).
	// Tier/Provider/Model/Adapter are set from tier.Resolve; Enforcement is the
	// adapter value adjusted by active constraining sandbox metadata.
	now := time.Now()
	d := &scratch.Dispatch{
		ID:             dispatchID,
		Parent:         callerID,
		Role:           *role,
		Model:          spec.Model,
		Profile:        result.Route.Profile,
		Status:         "running",
		Depth:          childDepth,
		MaxTurns:       result.Route.MaxTurns,
		TimeoutMinutes: result.Route.TimeoutMinutes,
		StartedAt:      now,
		Tier:           spec.Tier,
		Enforcement:    effectiveEnforcement,
		Provider:       spec.Provider,
		Adapter:        candidate.Adapter,
		Sandbox:        sandboxRec,
	}
	if err := st.WriteDispatch(runID, d); err != nil {
		return fmt.Errorf("dispatch: write dispatch record: %w", err)
	}

	// Append kind:"dispatch" to the CALLER's context_trace.jsonl via the Store.
	if callerID != "" {
		ev := scratch.TraceEvent{
			Ts:         now.UTC().Format(time.RFC3339Nano),
			Kind:       "dispatch",
			RunID:      runID,
			DispatchID: callerID,
			Depth:      callerDepth,
			ChildID:    dispatchID,
			Role:       *role,
			Model:      spec.Model,
			Profile:    result.Route.Profile,
		}
		if appendErr := st.AppendTraceEvent(runID, callerID, ev); appendErr != nil {
			fmt.Fprintf(os.Stderr, "tiller dispatch: context_trace append error: %v\n", appendErr)
		}
	}

	// Hypha trace tick: "<did> <role>(<tier>) dispatched by <parent>" (soft-fail).
	{
		hyp := hyphae.New(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "tiller dispatch [hypha]: "+format+"\n", args...)
		})
		if hyp.Available() {
			if runRec.HyphaTraceID != "" {
				parent := callerID
				if parent == "" {
					parent = "user"
				}
				tick := fmt.Sprintf("%s %s(%s) dispatched by %s", dispatchID, *role, result.Route.Tier, parent)
				hyp.TraceTick(runRec.HyphaTraceID, tick)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "dispatched %s as %s (role=%s, tier=%s)\n",
		dispatchID, *role, *role, result.Route.Tier)

	// --background or --no-wait: spawn only (process mechanic) and return.
	// adp.Run is not called in background/no-wait modes; the caller is
	// responsible for observing the dispatch via tiller poll/await.
	if *background || !*wait {
		binary, err := os.Executable()
		if err != nil {
			return fmt.Errorf("dispatch: find executable: %w", err)
		}
		if err := spawn.SpawnDetached(binary, runDir, dispatchID); err != nil {
			return fmt.Errorf("dispatch: spawn supervisor: %w", err)
		}
		fmt.Printf("%s %s\n", dispatchID, runDir)
		return nil
	}

	// --wait (default): delegate spawn + poll to the adapter.
	// adp.Run spawns _supervise and polls until terminal. A timeout context is
	// derived from spec.Timeout (0 = no adapter-level timeout; the --timeout flag
	// below provides the user-facing deadline independently).
	//
	// We use the CLI-level timeout as the context deadline for Run so that an
	// adapter that overruns can be cancelled cleanly.
	dur, parseErr := parseDuration(*timeout)
	if parseErr != nil {
		dur = 8 * time.Minute
	}
	runCtx, runCancel := context.WithTimeout(context.Background(), dur)
	defer runCancel()

	runResult, runErr := adp.Run(runCtx, spec)
	if runErr != nil {
		if runCtx.Err() != nil {
			// Context timed out: print running status per spec §2.3 (exit 0).
			reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
			fmt.Printf("%s running %s\n", dispatchID, reportPath)
			return nil
		}
		return fmt.Errorf("dispatch: run adapter: %w", runErr)
	}

	reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
	status := "completed"
	if runResult != nil {
		status = runResult.Status
	}
	fmt.Printf("%s %s %s\n", dispatchID, status, reportPath)
	return nil
}

// readBrief reads brief content from: "-" (stdin), a file path, or literal text.
func readBrief(briefFlag string) (string, error) {
	if briefFlag == "" {
		return "", nil
	}
	if briefFlag == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	}
	// Try as file path.
	if data, err := os.ReadFile(briefFlag); err == nil {
		return string(data), nil
	}
	// Treat as literal text.
	return briefFlag, nil
}

// parseDuration parses a duration string like "8m", "30s", "1h".
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Try Go standard format first.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Try minutes (plain integer).
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	return 0, fmt.Errorf("invalid duration: %q", s)
}

// runIDBucket returns a stable non-negative bucket index for canary bucketing
// (preserves v1 §6.3 stable-hash-of-run-id semantics).
// Uses FNV-32a so the mapping is deterministic across processes and platforms.
func runIDBucket(runID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return int(h.Sum32())
}

// roleToDefaultTier returns the default tier for a role, mirroring the
// DispatchRoute strategy in dispatch.arb. Used to pre-compute enforcement
// before the full policy evaluation when no --tier flag is set.
func roleToDefaultTier(role string) string {
	switch role {
	case "orchestrator", "chief-architect", "deep-report":
		return "reason"
	case "investigator", "reviewer":
		return "scrutiny"
	default:
		return "execute"
	}
}

func roleToDefaultProfile(role string) string {
	switch role {
	case "chief-architect", "deep-report":
		return "insight"
	case "investigator", "reviewer":
		return "readonly"
	case "worker", "debugger":
		return "execution"
	default:
		return ""
	}
}

func sandboxMode(rec *sandbox.Record) string {
	if rec == nil {
		return ""
	}
	return string(rec.Mode)
}

func sandboxProfile(rec *sandbox.Record, fallback string) string {
	if rec != nil && rec.Profile != "" {
		return rec.Profile
	}
	return fallback
}

func sandboxHorizonManifests(rec *sandbox.Record) int {
	if rec == nil {
		return 0
	}
	return len(rec.Horizon)
}
