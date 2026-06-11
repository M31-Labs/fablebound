package spawn

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/hyphae"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/storeutil"
)

// maxTranscriptParseBytes is the maximum transcript size we will read into
// memory for JSON parsing. Transcripts larger than this cap are persisted to
// disk (transcript.json is still written in full) but are not parsed; the
// dispatch is marked halted with an explanatory report.md.
//
// The real claude ≥2.1.172 streaming-event array can be very large for long
// sessions. 64 MiB is a generous ceiling that keeps heap usage bounded while
// accommodating nearly all real workloads. Set to a smaller value in tests via
// testTranscriptParseBytes.
//
// TODO: implement true incremental streaming JSON parse to eliminate this cap.
var maxTranscriptParseBytes int64 = 64 << 20 // 64 MiB

// ClaudeResult is the parsed --output-format json output from claude.
// It normalises two historical shapes into one struct:
//
//   - Legacy / stub shape (pre-2.1.172): single JSON object with "cost_usd".
//     {"type":"result","result":"...","cost_usd":0.001,"num_turns":1,"session_id":"...","is_error":false}
//
//   - Real claude 2.1.172 shape: JSON array of event objects; the result
//     record is the element with "type":"result" and uses "total_cost_usd".
//     [{"type":"system",...},{"type":"assistant",...},...,
//     {"type":"result","subtype":"success","total_cost_usd":1.16,"num_turns":3,...}]
//
// Use parseClaudeResult to decode either shape.
type ClaudeResult struct {
	Type      string  `json:"type"`
	Result    string  `json:"result"`
	CostUSD   float64 // normalised from cost_usd (legacy) or total_cost_usd (real)
	NumTurns  int     `json:"num_turns"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
}

// parseClaudeResult decodes the raw bytes captured from claude's stdout and
// returns a normalised ClaudeResult.  It handles two known output shapes:
//
//  1. Legacy / stub — single JSON object (pre-2.1.172 or test stubs):
//     {"type":"result","cost_usd":0.001,...}
//
//  2. Real claude ≥2.1.172 — JSON array of streaming events; the last element
//     with "type":"result" carries "total_cost_usd":
//     [{"type":"system",...},...,{"type":"result","total_cost_usd":1.16,...}]
//
// Shape detection is explicit: if the first non-whitespace byte is '[' the
// array path is taken; otherwise the legacy single-object path is used.
func parseClaudeResult(data []byte) (ClaudeResult, error) {
	// Detect array shape.
	trimmed := data
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}

	if len(trimmed) > 0 && trimmed[0] == '[' {
		// Real claude ≥2.1.172: JSON array of event objects.
		// rawArrayEvent is a minimal struct for one event in the array.
		type rawArrayEvent struct {
			Type         string  `json:"type"`
			Result       string  `json:"result"`
			TotalCostUSD float64 `json:"total_cost_usd"`
			NumTurns     int     `json:"num_turns"`
			SessionID    string  `json:"session_id"`
			IsError      bool    `json:"is_error"`
			Subtype      string  `json:"subtype"`
		}
		var events []rawArrayEvent
		if err := json.Unmarshal(trimmed, &events); err != nil {
			return ClaudeResult{}, fmt.Errorf("parse claude array output: %w", err)
		}
		// Find the result event (last element with type:"result").
		for _, e := range slices.Backward(events) {

			if e.Type == "result" {
				return ClaudeResult{
					Type:      e.Type,
					Result:    e.Result,
					CostUSD:   e.TotalCostUSD,
					NumTurns:  e.NumTurns,
					SessionID: e.SessionID,
					IsError:   e.IsError,
				}, nil
			}
		}
		return ClaudeResult{}, fmt.Errorf("parse claude array output: no result event found in %d events", len(events))
	}

	// Legacy / stub shape: single JSON object, possibly preceded by log noise.
	// trimOutput finds the first line whose first non-whitespace byte is '{'.
	type rawLegacyResult struct {
		Type      string  `json:"type"`
		Result    string  `json:"result"`
		CostUSD   float64 `json:"cost_usd"`
		NumTurns  int     `json:"num_turns"`
		SessionID string  `json:"session_id"`
		IsError   bool    `json:"is_error"`
	}
	var raw rawLegacyResult
	if err := json.Unmarshal(trimOutput(data), &raw); err != nil {
		return ClaudeResult{}, fmt.Errorf("parse claude single-object output: %w", err)
	}
	return ClaudeResult{
		Type:      raw.Type,
		Result:    raw.Result,
		CostUSD:   raw.CostUSD,
		NumTurns:  raw.NumTurns,
		SessionID: raw.SessionID,
		IsError:   raw.IsError,
	}, nil
}

// SuperviseArgs holds everything needed to run the supervisor.
type SuperviseArgs struct {
	RunDir     string
	DispatchID string
	// TimeoutMinutes: 0 means no timeout.
	TimeoutMinutes int
}

// Supervise runs claude for the given dispatch, captures output, and
// finalizes the dispatch record. It is meant to run as the _supervise
// subprocess (detached, setsid).
//
// Flow:
//  1. Build ClaudeArgs from the dispatch directory (reads dispatch record + brief.md + settings.json).
//  2. Exec claude; pipe stdout to supervise.log; enforce timeout_minutes.
//  3. Parse --output-format json result → write report.md + transcript.json.
//  4. Append kind:"report" to the dispatch's context_trace.jsonl.
//  5. Finalize dispatch record (status/cost/turns/session/ended_at/exit).
func Supervise(a SuperviseArgs) error {
	// Ensure TILLER_RUN_DIR is set so that storeutil.Resolve can read the manifest
	// and open the correct store (tee/pg if the parent run used one).
	// This is a no-op when the env var is already set by the spawn parent.
	if os.Getenv("TILLER_RUN_DIR") == "" {
		_ = os.Setenv("TILLER_RUN_DIR", a.RunDir)
	}

	// Resolve the store from the run directory.
	// storeutil.Resolve reads the manifest store field and opens a tee/pg store
	// when the parent `tiller run` used one — so supervise finalizations mirror to pg.
	// Soft-fail: on any dial error, falls back to fsstore (fs is authoritative).
	runID := filepath.Base(a.RunDir)
	var st scratch.Store
	var storeCloser func() error
	{
		resolvedSt, _, resolvedCloser, resolveErr := storeutil.Resolve(nil)
		if resolveErr != nil {
			// Fallback: use fsstore directly.
			runsBase := filepath.Dir(a.RunDir)
			resolvedSt = fsstore.Open(runsBase)
			resolvedCloser = nil
		}
		st = resolvedSt
		storeCloser = resolvedCloser
	}
	if storeCloser != nil {
		defer storeCloser() //nolint:errcheck
	}

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

	// Load dispatch record to get role, model, depth.
	d, err := st.ReadDispatch(runID, a.DispatchID)
	if err != nil {
		return fmt.Errorf("supervise: read dispatch: %w", err)
	}

	// Paths inside dispatch dir.
	briefPath := filepath.Join(dispatchDir, "brief.md")
	settingsPath := filepath.Join(dispatchDir, "settings.json")
	reportPath := filepath.Join(dispatchDir, "report.md")
	transcriptPath := filepath.Join(dispatchDir, "transcript.json")

	rolePromptPath := RolePromptPath(a.RunDir, d.Role)

	// Use TimeoutMinutes from dispatch record, falling back to SuperviseArgs override.
	timeoutMins := d.TimeoutMinutes
	if a.TimeoutMinutes > 0 {
		timeoutMins = a.TimeoutMinutes
	}

	// Derive tier from dispatch record: prefer Tier field (v2), fall back to
	// deriving from model string for v1 compatibility.
	tier := d.Tier
	if tier == "" {
		tier = modelToTier(d.Model)
	}
	metaRoute := policy.Route{
		Tier:           tier,
		Profile:        d.Profile,
		MaxTurns:       d.MaxTurns,
		TimeoutMinutes: timeoutMins,
	}

	cArgs := ClaudeArgs{
		RunDir:         a.RunDir,
		DispatchID:     a.DispatchID,
		Role:           d.Role,
		CallerDepth:    d.Depth - 1, // d.Depth is child depth; caller = child-1
		Route:          metaRoute,
		Model:          d.Model, // resolved by tier.Resolve at dispatch time (P2.6+)
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

	// Stream stdout directly to transcript.json on disk — no unbounded heap
	// accumulation regardless of how large the streaming-event array grows.
	transcriptFile, transcriptOpenErr := os.OpenFile(transcriptPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if transcriptOpenErr != nil {
		logf("warning: open transcript.json for streaming: %v", transcriptOpenErr)
		// Drain stdout so the child does not block on a full pipe.
		_, _ = io.Copy(io.Discard, stdoutPipe)
	} else {
		_, _ = io.Copy(transcriptFile, stdoutPipe)
		_ = transcriptFile.Close()
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

	// Determine transcript size and decide whether to parse.
	var claudeRes ClaudeResult
	finalStatus := "completed"

	transcriptSize := int64(0)
	if fi, statErr := os.Stat(transcriptPath); statErr == nil {
		transcriptSize = fi.Size()
	}

	if transcriptOpenErr != nil {
		// Could not stream to disk — no transcript to parse.
		logf("warning: transcript.json was not written; parse skipped")
		_ = os.WriteFile(reportPath, []byte("supervisor error: transcript.json could not be opened for streaming\n"), 0o644)
		if timeoutKilled {
			finalStatus = "halted"
		} else if exitCode != 0 {
			finalStatus = "failed"
		}
	} else if transcriptSize > maxTranscriptParseBytes {
		// Transcript exceeds parse cap — do NOT load into memory.
		// TODO: implement incremental streaming JSON parse to handle oversized transcripts.
		logf("warning: transcript.json size %d exceeds parse cap %d; parse skipped", transcriptSize, maxTranscriptParseBytes)
		reportMsg := fmt.Sprintf(
			"supervisor: transcript.json (%d bytes) exceeds parse cap (%d bytes).\n"+
				"The full transcript is preserved on disk but could not be parsed in-memory.\n"+
				"Dispatch marked halted. See supervise.log for details.\n",
			transcriptSize, maxTranscriptParseBytes,
		)
		_ = os.WriteFile(reportPath, []byte(reportMsg), 0o644)
		finalStatus = "halted"
	} else {
		// Read transcript back into memory (bounded by the cap check above).
		buf, readErr := os.ReadFile(transcriptPath)
		if readErr != nil {
			logf("warning: read transcript.json for parsing: %v", readErr)
			_ = os.WriteFile(reportPath, []byte(fmt.Sprintf("supervisor error: could not read transcript.json: %v\n", readErr)), 0o644)
			if timeoutKilled {
				finalStatus = "halted"
			} else if exitCode != 0 {
				finalStatus = "failed"
			}
		} else if parsedRes, parseErr := parseClaudeResult(buf); parseErr != nil {
			// Parse JSON result — handles both legacy single-object and real array shapes.
			logf("warning: failed to parse claude output as JSON: %v", parseErr)
			// Write raw stdout as report.
			_ = os.WriteFile(reportPath, buf, 0o644)
			if timeoutKilled {
				finalStatus = "halted"
			} else if exitCode != 0 {
				finalStatus = "failed"
			}
		} else {
			claudeRes = parsedRes
			// Write report.md via the Store.
			if err := st.WriteReport(runID, a.DispatchID, []byte(claudeRes.Result)); err != nil {
				logf("warning: write report.md: %v", err)
			}

			// transcript.json is already on disk from the streaming copy above.

			if claudeRes.IsError {
				finalStatus = "failed"
			} else if timeoutKilled {
				finalStatus = "halted"
			}

			logf("report written (%d bytes), status=%s", len(claudeRes.Result), finalStatus)
		}
	}

	// Append kind:"report" to dispatch's context_trace.jsonl via the Store.
	reportEvent := scratch.TraceEvent{
		Ts:         endedAt.UTC().Format(time.RFC3339Nano),
		Kind:       "report",
		RunID:      runID,
		DispatchID: a.DispatchID,
		Role:       d.Role,
		Depth:      d.Depth,
		Status:     finalStatus,
		CostUSD:    claudeRes.CostUSD,
		NumTurns:   claudeRes.NumTurns,
	}
	if appendErr := st.AppendTraceEvent(runID, a.DispatchID, reportEvent); appendErr != nil {
		logf("warning: append context_trace.jsonl: %v", appendErr)
	}

	// Finalize dispatch record.
	d.Status = finalStatus
	d.EndedAt = &endedAt
	d.Exit = exitCode
	d.CostUSD = claudeRes.CostUSD
	d.NumTurns = claudeRes.NumTurns
	d.SessionID = claudeRes.SessionID
	_ = startedAt // already recorded in initial dispatch write

	if err := st.WriteDispatch(runID, d); err != nil {
		logf("error: finalize dispatch: %v", err)
		return fmt.Errorf("supervise: finalize dispatch: %w", err)
	}

	logf("dispatch %s finalized as %s", a.DispatchID, finalStatus)

	// Hypha trace tick: "<did> <status> $<cost>" (soft-fail; log to supervise.log).
	{
		hyp := hyphae.New(func(format string, args ...any) {
			logf("[hypha] "+format, args...)
		})
		if hyp.Available() {
			if runRec, err := st.ReadRun(runID); err == nil && runRec.HyphaTraceID != "" {
				tick := fmt.Sprintf("%s %s $%.4f", a.DispatchID, finalStatus, claudeRes.CostUSD)
				hyp.TraceTick(runRec.HyphaTraceID, tick)
			}
		}
	}

	return nil
}

// SpawnDetached starts a detached tiller _supervise process.
// The child gets its own session (setsid) and its stdio goes to
// <dispatchDir>/supervise.log.
//
// Returns immediately after the child is spawned — the caller does NOT wait
// for the supervisor. The supervisor PID is written into the dispatch record so
// that orphan detection can verify whether the process is still alive.
func SpawnDetached(binary, runDir, dispatchID string) error {
	dispatchDir := filepath.Join(runDir, "dispatches", dispatchID)
	logPath := filepath.Join(dispatchDir, "supervise.log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("spawn detached: open log %s: %w", logPath, err)
	}
	// We intentionally don't defer close here — the file handle will be
	// duplicated into the child; the parent closes after Start().

	cmd := exec.Command(binary, "_supervise", runDir, dispatchID)
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

	// Write the supervisor PID into the dispatch record for orphan detection.
	// Best-effort: failure here does not block the dispatch.
	runsBase := filepath.Dir(runDir)
	runID := filepath.Base(runDir)
	st := fsstore.Open(runsBase)
	if d, readErr := st.ReadDispatch(runID, dispatchID); readErr == nil {
		d.SupervisorPID = cmd.Process.Pid
		if writeErr := st.WriteDispatch(runID, d); writeErr != nil {
			// Non-fatal: orphan detection will simply not have a PID to check.
			_ = writeErr
		}
	}

	// Release the process — we are not waiting.
	go func() { _ = cmd.Wait() }()

	return nil
}

// trimOutput returns the first line whose first non-whitespace byte is '{'.
// This avoids false positives from lines that contain '{' mid-line (e.g. log
// lines like "processing {thing}"). Falls back to the full buffer if no such
// line is found.
func trimOutput(data []byte) []byte {
	start := 0
	for start < len(data) {
		// Find end of current line.
		lineEnd := start
		for lineEnd < len(data) && data[lineEnd] != '\n' {
			lineEnd++
		}
		line := data[start:lineEnd]
		// Trim leading whitespace to find the first non-space byte.
		trimmed := line
		for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\r') {
			trimmed = trimmed[1:]
		}
		if len(trimmed) > 0 && trimmed[0] == '{' {
			// Return up to and including the newline.
			end := lineEnd
			if end < len(data) {
				end++ // include the '\n'
			}
			return data[start:end]
		}
		// Advance past the newline.
		start = lineEnd + 1
	}
	return data
}

// modelToTier derives a tier name from a v1 model string.
// Used for backward compatibility when reading v1 dispatch records that have
// a model field but no tier field.
func modelToTier(model string) string {
	switch model {
	case "fable":
		return "reason"
	case "opus":
		return "scrutiny"
	default:
		return "execute"
	}
}
