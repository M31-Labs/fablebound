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
	"m31labs.dev/fablebound/internal/auditlog"
	"m31labs.dev/fablebound/internal/hook"
	"m31labs.dev/fablebound/internal/policy"
	"m31labs.dev/fablebound/internal/run"
	"m31labs.dev/fablebound/internal/spawn"
)

// runDispatch is the handler for `fablebound dispatch`.
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

	// Resolve run directory.
	runDir, err := run.CurrentRunDir()
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	runID := filepath.Base(runDir)

	// Read caller identity from environment.
	callerRole := os.Getenv("FABLEBOUND_ROLE")
	if callerRole == "" {
		callerRole = "user"
	}
	callerDepth := 0
	if d := os.Getenv("FABLEBOUND_DEPTH"); d != "" {
		fmt.Sscanf(d, "%d", &callerDepth)
	}
	callerID := os.Getenv("FABLEBOUND_DISPATCH_ID")

	// Read brief content.
	briefContent, err := readBrief(*briefFlag)
	if err != nil {
		return fmt.Errorf("dispatch: read brief: %w", err)
	}

	// Load manifest for fable_budget.
	manifest, err := run.ReadManifest(runDir)
	if err != nil {
		return fmt.Errorf("dispatch: read manifest: %w", err)
	}

	fableBudget := manifest.FableBudget
	if fableBudget == 0 {
		fableBudget = 2
	}

	// Count active + fable dispatches.
	activeCount, err := run.ActiveCount(runDir)
	if err != nil {
		return fmt.Errorf("dispatch: count active: %w", err)
	}
	fableCount, err := run.FableCount(runDir)
	if err != nil {
		return fmt.Errorf("dispatch: count fable: %w", err)
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
		ActiveCount: activeCount,
		FableCount:  fableCount,
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

	// Open audit sink.
	sinks, err := auditlog.OpenRunSinks(runDir)
	if err != nil {
		return fmt.Errorf("dispatch: open audit: %w", err)
	}
	defer sinks.Close()

	// Build matched rules from result for the audit event.
	// We need to re-evaluate to get the matched rules. Use the internal data.
	// For audit, we record the result with strategy if Allow.
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

	// Re-derive matched rules via the stored arbitrace for auditing.
	// We use the rule and reason from the result for a simple audit entry.
	// The full trace is in result.Arbitrace.
	auditErr := auditlog.DispatchEvent(
		sinks.Dispatch,
		runID+"/"+*role,
		loaded.SHA256,
		req,
		nil, // matched rules — nil is acceptable for audit format
		stratRes,
		result.Arbitrace,
	)
	if auditErr != nil {
		fmt.Fprintf(os.Stderr, "fablebound dispatch: audit write error: %v\n", auditErr)
	}

	// Handle denial.
	if result.Verdict != policy.VerdictAllow {
		return &DenialError{Rule: result.Rule, Reason: result.Reason}
	}

	// Check for Reject route (empty model).
	if result.Route.Model == "" {
		return fmt.Errorf("dispatch: policy returned empty model for role %q (Reject route)", *role)
	}

	// Allocate dispatch id (skip non-numeric ids like "root").
	metas, err := run.ScanMetas(runDir)
	if err != nil {
		return fmt.Errorf("dispatch: scan metas: %w", err)
	}
	dispatchID := run.NextDispatchID(metas)

	// Create dispatch directory.
	// runDir = <workspace>/.fablebound/runs/<run-id>; store base = parent of runDir.
	dispatchDir, err := run.NewStore(filepath.Dir(runDir)).CreateDispatch(runID, dispatchID)
	if err != nil {
		return fmt.Errorf("dispatch: create dispatch dir: %w", err)
	}

	// Write brief.md.
	briefPath := filepath.Join(dispatchDir, "brief.md")
	if err := os.WriteFile(briefPath, []byte(briefContent), 0o644); err != nil {
		return fmt.Errorf("dispatch: write brief.md: %w", err)
	}

	// Write settings.json.
	settingsJSON, err := spawn.Settings(result.Route.Profile, callerDepth+1)
	if err != nil {
		return fmt.Errorf("dispatch: generate settings: %w", err)
	}
	settingsPath := filepath.Join(dispatchDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
		return fmt.Errorf("dispatch: write settings.json: %w", err)
	}

	// Write meta.json (status: running).
	now := time.Now()
	meta := &run.Meta{
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
	if err := run.WriteMeta(runDir, meta); err != nil {
		return fmt.Errorf("dispatch: write meta.json: %w", err)
	}

	// Append kind:"dispatch" to the CALLER's context_trace.jsonl.
	if callerID != "" {
		callerTracePath := filepath.Join(runDir, "dispatches", callerID, "context_trace.jsonl")
		dispatchEvent := map[string]any{
			"ts":          now.UTC().Format(time.RFC3339Nano),
			"kind":        "dispatch",
			"run_id":      runID,
			"dispatch_id": callerID,
			"child_id":    dispatchID,
			"role":        *role,
			"model":       result.Route.Model,
			"profile":     result.Route.Profile,
		}
		if appendErr := hook.AppendJSONL(callerTracePath, dispatchEvent); appendErr != nil {
			fmt.Fprintf(os.Stderr, "fablebound dispatch: context_trace append error: %v\n", appendErr)
		}
	}

	// Find fablebound binary for spawning.
	fablebound, err := os.Executable()
	if err != nil {
		return fmt.Errorf("dispatch: find executable: %w", err)
	}

	// Spawn detached supervisor.
	if err := spawn.SpawnDetached(fablebound, runDir, dispatchID); err != nil {
		return fmt.Errorf("dispatch: spawn supervisor: %w", err)
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

	return waitForDispatch(runDir, dispatchID, *timeout)
}

// waitForDispatch polls meta.json until terminal status or timeout.
// On timeout, exits 0 printing status "running" per spec §2.3.
func waitForDispatch(runDir, dispatchID, timeoutStr string) error {
	dur, err := parseDuration(timeoutStr)
	if err != nil {
		dur = 8 * time.Minute
	}

	deadline := time.Now().Add(dur)
	pollInterval := 200 * time.Millisecond

	for {
		m, err := run.ReadMeta(runDir, dispatchID)
		if err == nil {
			if m.IsTerminal() {
				reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
				fmt.Printf("%s %s %s\n", dispatchID, m.Status, reportPath)
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
