package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"m31labs.dev/fablebound/internal/hook"
	"m31labs.dev/fablebound/internal/policy"
	"m31labs.dev/fablebound/internal/run"
)

// ClaudeResult is the parsed --output-format json output from claude.
type ClaudeResult struct {
	Type      string  `json:"type"`
	Result    string  `json:"result"`
	CostUSD   float64 `json:"cost_usd"`
	NumTurns  int     `json:"num_turns"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
}

// SuperviseArgs holds everything needed to run the supervisor.
type SuperviseArgs struct {
	RunDir     string
	DispatchID string
	// TimeoutMinutes: 0 means no timeout.
	TimeoutMinutes int
}

// Supervise runs claude for the given dispatch, captures output, and
// finalizes the dispatch meta.json. It is meant to run as the _supervise
// subprocess (detached, setsid).
//
// Flow:
//  1. Build ClaudeArgs from the dispatch directory (reads meta.json + brief.md + settings.json).
//  2. Exec claude; pipe stdout to supervise.log; enforce timeout_minutes.
//  3. Parse --output-format json result → write report.md + transcript.json.
//  4. Append kind:"report" to the dispatch's context_trace.jsonl.
//  5. Finalize meta.json (status/cost/turns/session/ended_at/exit).
func Supervise(a SuperviseArgs) error {
	dispatchDir := filepath.Join(a.RunDir, "dispatches", a.DispatchID)
	logPath := filepath.Join(dispatchDir, "supervise.log")

	// Open supervise.log for all stdio from the child.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("supervise: open log %s: %w", logPath, err)
	}
	defer logFile.Close()

	logf := func(format string, args ...any) {
		ts := time.Now().UTC().Format(time.RFC3339)
		fmt.Fprintf(logFile, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
	}

	logf("supervisor started for dispatch %s", a.DispatchID)

	// Load meta to get role, model, depth, callerDepth.
	meta, err := run.ReadMeta(a.RunDir, a.DispatchID)
	if err != nil {
		return fmt.Errorf("supervise: read meta: %w", err)
	}

	// Paths inside dispatch dir.
	briefPath := filepath.Join(dispatchDir, "brief.md")
	settingsPath := filepath.Join(dispatchDir, "settings.json")
	reportPath := filepath.Join(dispatchDir, "report.md")
	transcriptPath := filepath.Join(dispatchDir, "transcript.json")

	rolePromptPath := RolePromptPath(a.RunDir, meta.Role)

	// Use TimeoutMinutes from meta (set at dispatch time), falling back to
	// the SuperviseArgs override (for testing).
	timeoutMins := meta.TimeoutMinutes
	if a.TimeoutMinutes > 0 {
		timeoutMins = a.TimeoutMinutes
	}

	metaRoute := policy.Route{
		Model:          meta.Model,
		Profile:        meta.Profile,
		MaxTurns:       meta.MaxTurns,
		TimeoutMinutes: timeoutMins,
	}

	cArgs := ClaudeArgs{
		RunDir:         a.RunDir,
		DispatchID:     a.DispatchID,
		Role:           meta.Role,
		CallerDepth:    meta.Depth - 1, // meta.Depth is child depth; caller = child-1
		Route:          metaRoute,
		BriefPath:      briefPath,
		SettingsPath:   settingsPath,
		RolePromptPath: rolePromptPath,
	}

	args, err := BuildArgs(cArgs)
	if err != nil {
		return fmt.Errorf("supervise: build args: %w", err)
	}

	env := BuildEnv(cArgs)

	bin := args[0]
	argv := args[1:]

	logf("exec: %s %v", bin, argv)

	cmd := exec.Command(bin, argv...)
	cmd.Env = env
	cmd.Stderr = logFile

	// Capture stdout for JSON parsing.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("supervise: stdout pipe: %w", err)
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervise: start claude: %w", err)
	}

	logf("claude PID=%d started", cmd.Process.Pid)

	// Apply timeout.
	var timeoutKilled bool
	var killTimer *time.Timer
	if timeoutMins > 0 {
		killTimer = time.AfterFunc(time.Duration(timeoutMins)*time.Minute, func() {
			timeoutKilled = true
			logf("timeout (%dm) reached, killing process", timeoutMins)
			_ = cmd.Process.Kill()
		})
	}

	// Read stdout into memory for JSON parsing.
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 4096)
	for {
		n, readErr := stdoutPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		if readErr != nil {
			break
		}
	}

	exitErr := cmd.Wait()
	endedAt := time.Now()

	if killTimer != nil {
		killTimer.Stop()
	}

	exitCode := 0
	if exitErr != nil {
		if ee, ok := exitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}

	logf("claude exited code=%d", exitCode)

	// Parse JSON result.
	var claudeRes ClaudeResult
	finalStatus := "completed"
	if err := json.Unmarshal(trimOutput(buf), &claudeRes); err != nil {
		logf("warning: failed to parse claude output as JSON: %v", err)
		// Write raw stdout as report.
		_ = os.WriteFile(reportPath, buf, 0o644)
		if timeoutKilled {
			finalStatus = "halted"
		} else if exitCode != 0 {
			finalStatus = "failed"
		}
	} else {
		// Write report.md from result field.
		if err := os.WriteFile(reportPath, []byte(claudeRes.Result), 0o644); err != nil {
			logf("warning: write report.md: %v", err)
		}

		// Write transcript.json from raw output.
		if err := os.WriteFile(transcriptPath, buf, 0o644); err != nil {
			logf("warning: write transcript.json: %v", err)
		}

		if claudeRes.IsError {
			finalStatus = "failed"
		} else if timeoutKilled {
			finalStatus = "halted"
		}

		logf("report written (%d bytes), status=%s", len(claudeRes.Result), finalStatus)
	}

	// Append kind:"report" to dispatch's context_trace.jsonl.
	ctxTracePath := filepath.Join(dispatchDir, "context_trace.jsonl")
	reportEvent := map[string]any{
		"ts":          endedAt.UTC().Format(time.RFC3339Nano),
		"kind":        "report",
		"run_id":      filepath.Base(a.RunDir),
		"dispatch_id": a.DispatchID,
		"role":        meta.Role,
		"depth":       meta.Depth,
		"status":      finalStatus,
		"cost_usd":    claudeRes.CostUSD,
		"num_turns":   claudeRes.NumTurns,
	}
	if appendErr := hook.AppendJSONL(ctxTracePath, reportEvent); appendErr != nil {
		logf("warning: append context_trace.jsonl: %v", appendErr)
	}

	// Finalize meta.json.
	meta.Status = finalStatus
	meta.EndedAt = &endedAt
	meta.Exit = exitCode
	meta.CostUSD = claudeRes.CostUSD
	meta.NumTurns = claudeRes.NumTurns
	meta.SessionID = claudeRes.SessionID
	_ = startedAt // already recorded in initial meta write

	if err := run.WriteMeta(a.RunDir, meta); err != nil {
		logf("error: finalize meta: %v", err)
		return fmt.Errorf("supervise: finalize meta: %w", err)
	}

	logf("dispatch %s finalized as %s", a.DispatchID, finalStatus)
	return nil
}

// SpawnDetached starts a detached fablebound _supervise process.
// The child gets its own session (setsid) and its stdio goes to
// <dispatchDir>/supervise.log.
//
// Returns immediately after the child is spawned — the caller does NOT wait
// for the supervisor.
func SpawnDetached(fablebound, runDir, dispatchID string) error {
	dispatchDir := filepath.Join(runDir, "dispatches", dispatchID)
	logPath := filepath.Join(dispatchDir, "supervise.log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("spawn detached: open log %s: %w", logPath, err)
	}
	// We intentionally don't defer close here — the file handle will be
	// duplicated into the child; the parent closes after Start().

	cmd := exec.Command(fablebound, "_supervise", runDir, dispatchID)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // detach from the caller's session
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("spawn detached: start _supervise: %w", err)
	}

	logFile.Close()

	// Release the process — we are not waiting.
	go func() { _ = cmd.Wait() }()

	return nil
}

// trimOutput trims leading/trailing whitespace and returns the first
// JSON object line from stdout (claude may emit other lines before the JSON).
func trimOutput(data []byte) []byte {
	// Find the first line that starts with '{'
	start := 0
	for i, b := range data {
		if b == '{' {
			start = i
			break
		}
	}
	// Find end of that JSON object (find the closing newline after it)
	end := len(data)
	for i := start; i < len(data); i++ {
		if data[i] == '\n' {
			end = i + 1
			break
		}
	}
	if start >= end {
		return data
	}
	return data[start:end]
}
