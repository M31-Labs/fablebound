// Package auditlog provides per-file JSONL audit sinks for tiller.
// Each sink wraps arbiter's audit.JSONLSink with an exclusive flock on each write,
// assembling DecisionEvents per spec §8.
package auditlog

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"m31labs.dev/arbiter/audit"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
	"m31labs.dev/tiller/internal/policy"
)

// RunSinks holds the two per-run audit sinks (dispatch + toolgate).
type RunSinks struct {
	Dispatch *Sink
	Toolgate *Sink
}

// OpenRunSinks opens (or creates) the two audit JSONL files under
// <runDir>/audit/ and returns a RunSinks. The caller must call Close.
func OpenRunSinks(runDir string) (*RunSinks, error) {
	auditDir := filepath.Join(runDir, "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return nil, fmt.Errorf("auditlog: mkdir %s: %w", auditDir, err)
	}
	dispatch, err := Open(filepath.Join(auditDir, "dispatch.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("auditlog: open dispatch sink: %w", err)
	}
	toolgate, err := Open(filepath.Join(auditDir, "toolgate.jsonl"))
	if err != nil {
		dispatch.Close()
		return nil, fmt.Errorf("auditlog: open toolgate sink: %w", err)
	}
	return &RunSinks{Dispatch: dispatch, Toolgate: toolgate}, nil
}

// Close closes both sinks.
func (rs *RunSinks) Close() {
	if rs.Dispatch != nil {
		rs.Dispatch.Close()
	}
	if rs.Toolgate != nil {
		rs.Toolgate.Close()
	}
}

// Sink is an append-only JSONL audit sink with per-write flock.
// It serialises concurrent writes from multiple goroutines; cross-process
// safety is provided by the advisory flock in appendLocked.
type Sink struct {
	path string
}

// Open creates (or opens) a JSONL audit file at path.
// The file is not opened until the first write, so this is cheap.
func Open(path string) (*Sink, error) {
	// Eagerly create the file so tests can stat it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("auditlog.Open %s: %w", path, err)
	}
	f.Close()
	return &Sink{path: path}, nil
}

// Close is a no-op (file is opened/closed per write). Present for symmetry.
func (s *Sink) Close() {}

// Path returns the file path of this sink.
func (s *Sink) Path() string { return s.path }

// WriteDecision appends one JSON event to the sink. It acquires an exclusive
// advisory flock for the duration of the write, then closes the file.
func (s *Sink) WriteDecision(_ context.Context, event audit.DecisionEvent) error {
	return s.appendLocked(event)
}

func (s *Sink) appendLocked(event audit.DecisionEvent) error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("auditlog: open %s: %w", s.path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("auditlog: flock %s: %w", s.path, err)
	}
	// Lock released on f.Close().

	return json.NewEncoder(f).Encode(event)
}

// ToolCallEvent assembles a DecisionEvent for a toolgate PreToolUse decision
// and writes it to the sink.
//
// Parameters:
//
//	sink       — the toolgate sink (run's audit/toolgate.jsonl)
//	requestID  — unique identifier for this hook invocation
//	bundleID   — hex sha256 of the toolgate policy source
//	req        — the ToolCallRequest that was evaluated
//	matched    — matched rules from EvalGoverned
//	trace      — arbitrace from EvalGoverned
//	stratRes   — nil for toolgate (rules-only); non-nil for dispatch strategy
func ToolCallEvent(
	sink *Sink,
	requestID string,
	bundleID string,
	req policy.ToolCallRequest,
	matched []vm.MatchedRule,
	trace *govern.Arbitrace,
) error {
	event := assembleEvent("rules", requestID, bundleID, policy.ContextMap(req), matched, nil, trace)
	return sink.WriteDecision(context.Background(), event)
}

// DispatchEvent assembles a DecisionEvent for a dispatch decision
// and writes it to the sink.
func DispatchEvent(
	sink *Sink,
	requestID string,
	bundleID string,
	req policy.DispatchRequest,
	matched []vm.MatchedRule,
	stratRes *audit.StrategyDecision,
	trace *govern.Arbitrace,
) error {
	event := assembleEvent("rules", requestID, bundleID, policy.ContextMap(req), matched, stratRes, trace)
	return sink.WriteDecision(context.Background(), event)
}

// assembleEvent builds a DecisionEvent per spec §8.
// Kind is always "rules" so that arbiter replay processes the events.
func assembleEvent(
	kind string,
	requestID string,
	bundleID string,
	ctx map[string]any,
	matched []vm.MatchedRule,
	stratRes *audit.StrategyDecision,
	trace *govern.Arbitrace,
) audit.DecisionEvent {
	rules := make([]audit.RuleMatch, 0, len(matched))
	for _, mr := range matched {
		rm := audit.RuleMatch{
			Name:     mr.Name,
			Priority: mr.Priority,
			Action:   mr.Action,
			Fallback: mr.Fallback,
		}
		if len(mr.Params) > 0 {
			rm.Params = make(map[string]any, len(mr.Params))
			maps.Copy(rm.Params, mr.Params)
		}
		rules = append(rules, rm)
	}

	var steps []govern.ArbitraceStep
	if trace != nil {
		steps = trace.Steps
	}

	return audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		RequestID: requestID,
		BundleID:  bundleID,
		Kind:      kind,
		Context:   ctx,
		Rules:     rules,
		Strategy:  stratRes,
		Arbitrace: steps,
	}
}
