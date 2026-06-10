// Package hyphae provides a soft-dependency wrapper around the `hypha` CLI.
// Every call is best-effort: if hypha is absent from PATH or returns an error,
// the failure is logged and execution continues. NEVER invoke `hypha mcp serve`
// or `hypha hub serve` — persistent daemons are explicitly forbidden.
package hyphae

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const (
	// HyphaSpace is the default hyphae space for fablebound traces/spores.
	HyphaSpace = "hypha://m31labs/fablebound"

	// HyphaAgent is the agent URI used for trace start.
	HyphaAgent = "hypha://agents/fablebound"
)

// Logger is a function that logs a formatted message; used so callers can
// redirect to supervise.log or stderr.
type Logger func(format string, args ...any)

// Hypha is a wrapper around the hypha CLI binary. All methods are no-ops if
// the binary is not found on PATH.
type Hypha struct {
	path   string // resolved path; "" if not found
	log    Logger
	stdout io.Writer // capture for tests; nil → discard
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
	if h.stdout != nil {
		cmd.Stdout = h.stdout
	}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.log("hypha %s: %v (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// TraceStart opens a new hypha trace for a fablebound run.
// Returns the trace id (stdout of hypha trace start) or "" on failure.
// Args: --agent <agent> --task <runID> --phase <phase> --space <space>
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
	// The trace id is the first word of stdout.
	id := firstWord(out)
	if id != "" {
		h.log("hypha trace start: trace id=%s", id)
	}
	return id
}

// TraceTick appends a tick event to an existing trace.
// id is the trace id returned by TraceStart; message is the tick text.
// No-op if id is empty.
func (h *Hypha) TraceTick(id, message string) {
	if !h.Available() || id == "" {
		return
	}
	h.run("trace", "tick", id, message) //nolint:errcheck
}

// TraceDone finalises a trace with the given status (e.g. "completed", "failed").
// No-op if id is empty.
func (h *Hypha) TraceDone(id, status string) {
	if !h.Available() || id == "" {
		return
	}
	h.run("trace", "done", id, "--status", status) //nolint:errcheck
}

// SporeSubmit runs `hypha spore submit <path> --sign [--space s] [--as a]`.
// Returns the raw stdout. Soft-fails on error.
func (h *Hypha) SporeSubmit(sporePath, space, as string) (string, error) {
	if !h.Available() {
		return "", fmt.Errorf("hypha not available")
	}
	args := []string{"spore", "submit", sporePath, "--sign"}
	if space != "" {
		args = append(args, "--space", space)
	}
	if as != "" {
		args = append(args, "--as", as)
	}
	return h.run(args...)
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
