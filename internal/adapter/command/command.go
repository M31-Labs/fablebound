// Package command implements the generic command adapter for tiller.
//
// The command adapter executes an arbitrary subprocess as an agent. It is the
// multi-backend proof (spec §5.1): any program that reads a brief and produces
// output can serve as a tiller agent without implementing hooks.
//
// Because command-backed agents cannot intercept tool calls, the adapter's
// enforcement level is "degraded" (spec §5.1). The policy layer uses this to
// restrict command adapters to execute-tier roles only
// (DenyDegradedInsight rule in dispatch.arb).
//
// Configuration comes from the [adapter.<name>] section in models.toml, where
// <name> matches the provider field of the tier candidate:
//
//	[adapter.echo-agent]
//	argv    = ["/usr/local/bin/echo-agent", "--brief", "{brief}", "--out", "{report}"]
//	report  = "stdout"   # "stdout" or a file path written by the subprocess
//	timeout = "5m"       # Go duration string; 0 = no timeout
//
// Placeholder substitution in argv:
//
//	{brief}  — absolute path to the materialised brief.md in the spool dir
//	{report} — absolute path where the adapter expects the report output
//
// If report = "stdout", {report} is still substituted (with a temp file path)
// but the adapter captures stdout instead of reading the file.
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/tier"
)

// Adapter implements the command adapter: it execs an arbitrary subprocess as
// an agent, capturing its output as the dispatch report.
//
// Construct via New; do not use the zero value directly.
type Adapter struct {
	// tierCfg is used to look up per-provider [adapter.<name>] config.
	tierCfg *tier.Config
}

// New returns a new command Adapter backed by the given tier.Config.
// tierCfg must not be nil; it is used to look up [adapter.<name>] sections
// keyed by the Candidate.Provider field (e.g. "echo-agent" for
// "command:echo-agent/-").
func New(tierCfg *tier.Config) *Adapter {
	return &Adapter{tierCfg: tierCfg}
}

// Name returns "command".
func (a *Adapter) Name() string { return "command" }

// Enforcement returns "degraded": command-backed agents cannot intercept tool
// calls, so tool-call gating is unavailable (spec §5.1).
func (a *Adapter) Enforcement() string { return "degraded" }

// Prepare materialises the brief path and adapter config for the dispatch.
// For the command adapter, Prepare is a no-op beyond what dispatch.go already
// does (WriteBrief). The adapter reads the brief from the spool dir at Run time.
// Prepare is idempotent.
func (a *Adapter) Prepare(_ context.Context, _ *adapter.DispatchSpec) error {
	// The brief has already been written by dispatch.go via Store.WriteBrief.
	// No settings.json (hook config) is needed — command adapters have no hooks.
	return nil
}

// Run executes the configured subprocess, captures its output, writes the
// report via the Store, and appends a kind:"report" TraceEvent.
//
// On subprocess failure (non-zero exit) the result status is "failed" and the
// captured output (or error text) is written as the report. The adapter itself
// returns (nil, err) only when it cannot proceed at all (config not found,
// I/O failure before exec).
func (a *Adapter) Run(ctx context.Context, s *adapter.DispatchSpec) (*adapter.Result, error) {
	// Resolve adapter config from the tier config using Provider as the key.
	ac := a.tierCfg.AdapterConfig(s.Provider)
	if ac == nil {
		return nil, fmt.Errorf("command adapter: no [adapter.%s] section found in tier config", s.Provider)
	}

	if len(ac.Argv) == 0 {
		return nil, fmt.Errorf("command adapter: [adapter.%s] has empty argv", s.Provider)
	}

	// Determine the spool dir for this dispatch.
	// The fsstore layout is: runsBase/runID/dispatches/dispatchID/
	// WorkDir is set to runsBase/runID by dispatch.go.
	spoolDir := filepath.Join(s.WorkDir, "dispatches", s.DispatchID)

	// Materialise brief path (it was written by dispatch.go via WriteBrief).
	briefPath := filepath.Join(spoolDir, "brief.md")

	// Determine report path: always a file in the spool dir.
	reportPath := filepath.Join(spoolDir, "cmd-report.md")

	// Substitute placeholders in argv.
	argv := substitutePlaceholders(ac.Argv, briefPath, reportPath)
	if len(argv) == 0 {
		return nil, fmt.Errorf("command adapter: argv is empty after substitution")
	}

	// Build context with optional timeout.
	runCtx := ctx
	var cancelTimeout context.CancelFunc
	if ac.Timeout != "" {
		dur, err := time.ParseDuration(ac.Timeout)
		if err != nil {
			return nil, fmt.Errorf("command adapter: invalid timeout %q: %w", ac.Timeout, err)
		}
		if dur > 0 {
			runCtx, cancelTimeout = context.WithTimeout(ctx, dur)
			defer cancelTimeout()
		}
	}

	// Exec the subprocess.
	// Use a new process group so that context cancellation (timeout) can kill
	// the entire subprocess tree, not just the direct child shell.
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...) //nolint:gosec
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Override the default context-cancel behaviour (SIGKILL to pid only) so
	// we can kill the entire process group on timeout.
	cmd.WaitDelay = 0 // no grace period needed for command adapters
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout // combine stderr into stdout for diagnostics

	// Start the process, then wait manually so we can kill the group on cancel.
	startErr := cmd.Start()
	if startErr != nil {
		return nil, fmt.Errorf("command adapter: start %s: %w", argv[0], startErr)
	}

	// Wait for completion or context cancellation.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var execErr error
	select {
	case execErr = <-done:
		// Process exited normally (or with non-zero).
	case <-runCtx.Done():
		// Timeout or external cancel: kill the whole process group.
		if cmd.Process != nil {
			// Negative PID kills the process group.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		execErr = runCtx.Err()
		// Drain the done channel to avoid goroutine leak.
		<-done
	}

	// Collect report content.
	var reportBytes []byte
	reportSource := ac.Report
	if reportSource == "" {
		reportSource = "stdout"
	}

	if reportSource == "stdout" {
		reportBytes = stdout.Bytes()
	} else {
		// Read the file the subprocess was supposed to write.
		var readErr error
		reportBytes, readErr = os.ReadFile(reportPath)
		if readErr != nil {
			// Fall back to stdout content + error message.
			reportBytes = append(stdout.Bytes(),
				[]byte(fmt.Sprintf("\n[command adapter: read report file %s: %v]", reportPath, readErr))...)
		}
	}

	// Determine terminal status.
	status := "completed"
	if execErr != nil {
		status = "failed"
		ctxErr := runCtx.Err()
		if ctxErr == context.DeadlineExceeded {
			reportBytes = append(reportBytes, []byte("\n[command adapter: timeout exceeded]")...)
		} else if ctxErr == context.Canceled {
			reportBytes = append(reportBytes, []byte("\n[command adapter: cancelled]")...)
		}
	}

	// Write report via the Store.
	if writeErr := s.Store.WriteReport(s.RunID, s.DispatchID, reportBytes); writeErr != nil {
		return nil, fmt.Errorf("command adapter: write report: %w", writeErr)
	}

	// Append kind:"report" TraceEvent.
	ev := scratch.TraceEvent{
		Ts:         time.Now().UTC().Format(time.RFC3339Nano),
		Kind:       "report",
		RunID:      s.RunID,
		DispatchID: s.DispatchID,
		Role:       s.Role,
		Depth:      s.Depth,
		Status:     status,
	}
	if appendErr := s.Store.AppendTraceEvent(s.RunID, s.DispatchID, ev); appendErr != nil {
		// Best-effort: log but don't fail the dispatch.
		fmt.Fprintf(os.Stderr, "command adapter: append trace event: %v\n", appendErr)
	}

	return &adapter.Result{
		Status:  status,
		CostUSD: 0,
	}, nil
}

// substitutePlaceholders replaces {brief} and {report} tokens in argv elements.
func substitutePlaceholders(argv []string, briefPath, reportPath string) []string {
	result := make([]string, len(argv))
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, "{brief}", briefPath)
		arg = strings.ReplaceAll(arg, "{report}", reportPath)
		result[i] = arg
	}
	return result
}
