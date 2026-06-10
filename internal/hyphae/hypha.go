// Package hyphae provides a soft-dependency wrapper around the `hypha` CLI.
// Every call is best-effort: if hypha is absent from PATH or returns an error,
// the failure is logged and execution continues. NEVER invoke `hypha mcp serve`
// or `hypha hub serve` — persistent daemons are explicitly forbidden.
package hyphae

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const (
	// HyphaSpace is the default hyphae space for tiller traces/spores.
	HyphaSpace = "hypha://m31labs/tiller"

	// HyphaAgent is the agent URI used for trace start.
	HyphaAgent = "hypha://agents/tiller"
)

// Logger is a function that logs a formatted message; used so callers can
// redirect to supervise.log or stderr.
type Logger func(format string, args ...any)

// Hypha is a wrapper around the hypha CLI binary. All methods are no-ops if
// the binary is not found on PATH.
type Hypha struct {
	path string // resolved path; "" if not found
	log  Logger
}

// New returns a Hypha instance. If hypha is not on PATH, the instance is
// valid but all calls will be no-ops (logged as skips).
// log may be nil (falls back to a discard logger).
func New(log Logger) *Hypha {
	if log == nil {
		log = func(string, ...any) {}
	}
	p, err := exec.LookPath("hypha")
	if err != nil {
		log("hypha not found on PATH: traces will be skipped")
		return &Hypha{log: log}
	}
	return &Hypha{path: p, log: log}
}

// Available reports whether hypha was found on PATH.
func (h *Hypha) Available() bool {
	return h.path != ""
}

// run executes the hypha binary with the given arguments.
// Returns the combined stdout output.
// Any error is logged and swallowed (soft-fail).
func (h *Hypha) run(args ...string) (string, error) {
	if !h.Available() {
		return "", nil
	}
	cmd := exec.Command(h.path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.log("hypha %s: %v (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// TraceStart opens a new hypha trace for a tiller run.
// Returns the trace id or "" on failure.
// Args: --agent <agent> --task <runID> --phase <phase> --space <space>
//
// hypha ≥ v0.1.9 emits a JSON envelope on stdout; TraceStart parses d.ID from
// it. If stdout is not valid JSON (older hypha emits plain text), the first
// whitespace-delimited word is used as the id.
func (h *Hypha) TraceStart(runID, phase, space string) string {
	if !h.Available() {
		return ""
	}
	if space == "" {
		space = HyphaSpace
	}
	out, err := h.run(
		"trace", "start",
		"--agent", HyphaAgent,
		"--task", runID,
		"--phase", phase,
		"--space", space,
	)
	if err != nil {
		return ""
	}
	id := parseEnvelopeID(out)
	if id != "" {
		h.log("hypha trace start: trace id=%s", id)
	}
	return id
}

// TraceTick appends a tick event to an existing trace.
// id is the trace id returned by TraceStart; message is the tick text;
// space is the hyphae space URI (required on multi-space installs).
// No-op if id is empty.
func (h *Hypha) TraceTick(id, message string) {
	if !h.Available() || id == "" {
		return
	}
	h.run("trace", "tick", id, message, "--space", HyphaSpace) //nolint:errcheck
}

// TraceDone finalises a trace with the given status.
// status is in tiller vocabulary (completed|failed|halted|stale|…) and is
// mapped to the hypha v0.1.9 vocabulary (succeeded|failed|killed) at this
// boundary. Callers stay in tiller terms; the mapping lives here.
//
// Mapping:
//   - completed → succeeded
//   - failed    → failed
//   - anything else (halted, stale, …) → killed
//
// --space is required on multi-space installs (observed in v0.1.9 live probe).
// No-op if id is empty.
func (h *Hypha) TraceDone(id, status string) {
	if !h.Available() || id == "" {
		return
	}
	h.run("trace", "done", id, "--status", tillerStatusToHypha(status), "--space", HyphaSpace) //nolint:errcheck
}

// tillerStatusToHypha maps tiller run-terminal status vocabulary to the hypha
// v0.1.9 trace-done vocabulary (succeeded|failed|killed).
func tillerStatusToHypha(tillerStatus string) string {
	switch tillerStatus {
	case "completed":
		return "succeeded"
	case "failed":
		return "failed"
	default:
		// halted, stale, and any future terminal states map to killed.
		return "killed"
	}
}

// SporeSubmit runs `hypha spore submit <path> --sign [--as a]`.
// Returns a display string: if stdout is a JSON envelope containing d.FilePath,
// that path is returned; otherwise raw stdout is returned. Soft-fails on error.
//
// Note: `hypha spore submit` does not accept a --space flag (v0.1.9).
func (h *Hypha) SporeSubmit(sporePath, _ /*space*/, as string) (string, error) {
	if !h.Available() {
		return "", fmt.Errorf("hypha not available")
	}
	args := []string{"spore", "submit", sporePath, "--sign"}
	if as != "" {
		args = append(args, "--as", as)
	}
	out, err := h.run(args...)
	if err != nil {
		return out, err
	}
	// Prefer the receipt file path from the JSON envelope when present.
	if fp := parseEnvelopeFilePath(out); fp != "" {
		return fp, nil
	}
	return out, nil
}

// parseEnvelopeID extracts the trace id from a hypha v0.1.9 JSON envelope
// (field path: .d.ID). Falls back to firstWord for legacy plain-text output.
func parseEnvelopeID(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if strings.HasPrefix(out, "{") {
		var env struct {
			D struct {
				ID string `json:"ID"`
			} `json:"d"`
		}
		if err := json.Unmarshal([]byte(out), &env); err == nil && env.D.ID != "" {
			return env.D.ID
		}
	}
	// Legacy plain-text or malformed JSON: use first word.
	return firstWord(out)
}

// parseEnvelopeFilePath extracts the file path from a hypha v0.1.9 JSON
// envelope (field path: .d.FilePath). Returns "" if not present or not JSON.
func parseEnvelopeFilePath(out string) string {
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") {
		return ""
	}
	var env struct {
		D struct {
			FilePath string `json:"FilePath"`
		} `json:"d"`
	}
	if err := json.Unmarshal([]byte(out), &env); err == nil {
		return env.D.FilePath
	}
	return ""
}

// firstWord returns the first whitespace-delimited token from s.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	idx := strings.IndexAny(s, " \t\n\r")
	if idx < 0 {
		return s
	}
	return s[:idx]
}
