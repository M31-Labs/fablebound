package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/hyphae"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/spawn"
)

// runDispatch is the handler for `tiller dispatch`.
// Caller identity comes from environment; absent ⇒ role="user", depth=0.
func runDispatch(args []string) error {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		role       = fs.String("role", "", "agent role (required)")
		model      = fs.String("model", "", "model override (optional, downgrade only)")
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

	// Load run record for fable_budget.
	runRec, err := st.ReadRun(runID)
	if err != nil {
		return fmt.Errorf("dispatch: read run: %w", err)
	}

	fableBudget := runRec.FableBudget
	if fableBudget == 0 {
		fableBudget = 2
	}

	// Get active + fable counts via DispatchFacts (subsumes ActiveCount + FableCount).
	facts, err := st.DispatchFacts(runID)
	if err != nil {
		return fmt.Errorf("dispatch: dispatch facts: %w", err)
	}

	// Build dispatch request.
	req := policy.DispatchRequest{
		Role:        *role,
		Model:       *model,
		Background:  *background,
		BriefBytes:  len(briefContent),
		CallerRole:  callerRole,
		CallerDepth: callerDepth,
		CallerID:    callerID,
		RunID:       runID,
		ActiveCount: facts.Active,
		FableCount:  facts.ReasonCount,
		FableBudget: fableBudget,
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
	if result.Verdict == policy.VerdictAllow && result.Route.Model != "" {
		stratRes = &audit.StrategyDecision{
			Strategy: "DispatchRoute",
			Selected: *role,
			Outcome:  result.Route.Profile,
			Params: map[string]any{
				"model":           result.Route.Model,
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

	// Check for Reject route (empty model).
	if result.Route.Model == "" {
		return fmt.Errorf("dispatch: policy returned empty model for role %q (Reject route)", *role)
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

	// Write settings.json via the Store.
	settingsJSON, err := spawn.Settings(result.Route.Profile, callerDepth+1)
	if err != nil {
		return fmt.Errorf("dispatch: generate settings: %w", err)
	}
	if err := st.WriteAdapterConfig(runID, dispatchID, settingsJSON); err != nil {
		return fmt.Errorf("dispatch: write settings.json: %w", err)
	}

	// Write dispatch record (status: running).
	now := time.Now()
	d := &scratch.Dispatch{
		ID:             dispatchID,
		Parent:         callerID,
		Role:           *role,
		Model:          result.Route.Model,
		Profile:        result.Route.Profile,
		Status:         "running",
		Depth:          callerDepth + 1,
		MaxTurns:       result.Route.MaxTurns,
		TimeoutMinutes: result.Route.TimeoutMinutes,
		StartedAt:      now,
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
			Model:      result.Route.Model,
			Profile:    result.Route.Profile,
		}
		if appendErr := st.AppendTraceEvent(runID, callerID, ev); appendErr != nil {
			fmt.Fprintf(os.Stderr, "tiller dispatch: context_trace append error: %v\n", appendErr)
		}
	}

	// Find tiller binary for spawning.
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("dispatch: find executable: %w", err)
	}

	// Spawn detached supervisor.
	if err := spawn.SpawnDetached(binary, runDir, dispatchID); err != nil {
		return fmt.Errorf("dispatch: spawn supervisor: %w", err)
	}

	// Hypha trace tick: "<did> <role>(<model>) dispatched by <parent>" (soft-fail).
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
				tick := fmt.Sprintf("%s %s(%s) dispatched by %s", dispatchID, *role, result.Route.Model, parent)
				hyp.TraceTick(runRec.HyphaTraceID, tick)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "dispatched %s as %s (role=%s, model=%s)\n",
		dispatchID, *role, *role, result.Route.Model)

	// --background: return immediately.
	if *background {
		fmt.Printf("%s %s\n", dispatchID, runDir)
		return nil
	}

	// --wait (default): poll until terminal or timeout.
	if !*wait {
		fmt.Printf("%s %s\n", dispatchID, runDir)
		return nil
	}

	return waitForDispatch(st, runID, runDir, dispatchID, *timeout)
}

// waitForDispatch polls dispatch record until terminal status or timeout.
// On timeout, exits 0 printing status "running" per spec §2.3.
func waitForDispatch(st scratch.Store, runID, runDir, dispatchID, timeoutStr string) error {
	dur, err := parseDuration(timeoutStr)
	if err != nil {
		dur = 8 * time.Minute
	}

	deadline := time.Now().Add(dur)
	pollInterval := 200 * time.Millisecond

	for {
		d, err := st.ReadDispatch(runID, dispatchID)
		if err == nil {
			if d.IsTerminal() {
				reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
				fmt.Printf("%s %s %s\n", dispatchID, d.Status, reportPath)
				return nil
			}
		}

		if time.Now().After(deadline) {
			// Timeout: exit 0, print running.
			reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
			fmt.Printf("%s running %s\n", dispatchID, reportPath)
			return nil
		}

		time.Sleep(pollInterval)
	}
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
