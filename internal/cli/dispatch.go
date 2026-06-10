package cli

import (
	"context"
	"flag"
	"fmt"
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
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/spawn"
)

// makeDispatchHandler returns a handler for `tiller dispatch` that uses the
// provided adapter registry for spawn+poll. P2.6 will replace the hard-coded
// "claude-headless" adapter name with tier-resolved routing; the registry
// lookup point is already in place here so P2.6 only needs to change one line.
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
		tier       = fs.String("tier", "", "tier override: reason|scrutiny|execute (optional, downgrade only)")
		modelAlias = fs.String("model", "", "deprecated: use --tier; fable→reason, opus→scrutiny, sonnet/haiku→execute")
		briefFlag  = fs.String("brief", "", "brief: '-' for stdin, a file path, or literal text")
		background = fs.Bool("background", false, "return immediately after spawn")
		timeout    = fs.String("timeout", "8m", "wait timeout (e.g. 8m, 30s)")
		wait       = fs.Bool("wait", true, "wait for completion (default true; use --background to disable)")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *role == "" {
		return fmt.Errorf("--role is required")
	}

	// Handle deprecated --model alias: map vendor model names to tiers.
	if *modelAlias != "" && *tier == "" {
		fmt.Fprintf(os.Stderr, "tiller dispatch: --model is deprecated; use --tier instead\n")
		switch *modelAlias {
		case "fable":
			*tier = "reason"
		case "opus":
			*tier = "scrutiny"
		case "sonnet", "haiku":
			*tier = "execute"
		default:
			// Unknown model: pass through as tier string (best effort).
			*tier = *modelAlias
		}
	}

	// Resolve run directory and open store.
	st, runID, err := fsstore.Resolve()
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
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

	reasonBudget := runRec.FableBudget // FableBudget field stores reason_budget (renamed in v2)
	if reasonBudget == 0 {
		reasonBudget = 2
	}

	// Get active + reason counts via DispatchFacts (subsumes ActiveCount + ReasonCount).
	facts, err := st.DispatchFacts(runID)
	if err != nil {
		return fmt.Errorf("dispatch: dispatch facts: %w", err)
	}

	// Build dispatch request.
	req := policy.DispatchRequest{
		Role:         *role,
		Tier:         *tier,
		Background:   *background,
		BriefBytes:   len(briefContent),
		CallerRole:   callerRole,
		CallerDepth:  callerDepth,
		CallerID:     callerID,
		RunID:        runID,
		ActiveCount:  facts.Active,
		ReasonCount:  facts.ReasonCount,
		ReasonBudget: reasonBudget,
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

	// Resolve adapter for this dispatch.
	// P2.6 will replace the constant "claude-headless" with tier.Resolve output;
	// the registry lookup is already here so P2.6 only changes the name string.
	const adapterName = "claude-headless"
	adp, err := reg.Get(adapterName)
	if err != nil {
		return fmt.Errorf("dispatch: resolve adapter %q: %w", adapterName, err)
	}

	// Build the DispatchSpec for the adapter.
	// Tier is set from policy route; Model is derived from Tier for the adapter
	// until P2.6 wires full tier-to-model resolution via models.toml.
	childDepth := callerDepth + 1
	spec := &adapter.DispatchSpec{
		Store:      st,
		RunID:      runID,
		DispatchID: dispatchID,
		Role:       *role,
		Tier:       result.Route.Tier,
		Provider:   "anthropic",
		Model:      tierToDefaultModel(result.Route.Tier), // P2.6 replaces with tier.Resolve
		Profile:    result.Route.Profile,
		WorkDir:    runDir,
		Depth:      childDepth,
		MaxTurns:   result.Route.MaxTurns,
		Timeout:    time.Duration(result.Route.TimeoutMinutes) * time.Minute,
	}

	// Prepare: writes settings.json via the Store (present-brief verb is already
	// fulfilled by WriteBrief above; Prepare writes the adapter config).
	if err := adp.Prepare(context.Background(), spec); err != nil {
		return fmt.Errorf("dispatch: prepare adapter: %w", err)
	}

	// Write dispatch record (status: running).
	// Tier and Enforcement are set from the adapter contract (spec §2.1).
	now := time.Now()
	d := &scratch.Dispatch{
		ID:             dispatchID,
		Parent:         callerID,
		Role:           *role,
		Model:          spec.Model, // derived from tier until P2.6
		Profile:        result.Route.Profile,
		Status:         "running",
		Depth:          childDepth,
		MaxTurns:       result.Route.MaxTurns,
		TimeoutMinutes: result.Route.TimeoutMinutes,
		StartedAt:      now,
		Tier:           spec.Tier,        // set by Prepare (deriveTier)
		Enforcement:    adp.Enforcement(), // "full" for claude-headless
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

// tierToDefaultModel maps a tier name to a default model identifier.
// This is a temporary bridge until P2.6 wires tier.Resolve from models.toml.
// The adapter (claudeheadless) also has deriveTier(model) which is the inverse.
func tierToDefaultModel(tier string) string {
	switch tier {
	case "reason":
		return "fable"
	case "scrutiny":
		return "opus"
	case "execute":
		return "sonnet"
	default:
		return "sonnet" // safe default
	}
}
