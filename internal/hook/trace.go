package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ToolTraceEvent is one entry in dispatches/<id>/tool_trace.jsonl.
type ToolTraceEvent struct {
	Ts           string `json:"ts"`
	Kind         string `json:"kind"`
	RunID        string `json:"run_id"`
	DispatchID   string `json:"dispatch_id"`
	Role         string `json:"role"`
	Depth        int    `json:"depth"`
	Tool         string `json:"tool"`
	InputSummary string `json:"input_summary"`
	Status       string `json:"status"`
}

// ContextReadEvent is a kind:"read" entry in context_trace.jsonl.
type ContextReadEvent struct {
	Ts           string `json:"ts"`
	Kind         string `json:"kind"`
	RunID        string `json:"run_id"`
	DispatchID   string `json:"dispatch_id"`
	Role         string `json:"role"`
	Depth        int    `json:"depth"`
	Tool         string `json:"tool"`
	InputSummary string `json:"input_summary"`
}

// HandlePostToolUse processes a PostToolUse event.
// Appends a kind:"tool" event to tool_trace.jsonl, and for Read/Glob/Grep
// also appends a kind:"read" event to context_trace.jsonl.
// Always returns nil — trace failures must never block the agent.
func HandlePostToolUse(id Identity, event HookEvent) error {
	if id.RunDir == "" || id.DispatchID == "" {
		return nil
	}

	var input ToolInput
	if len(event.ToolInput) > 0 {
		_ = json.Unmarshal(event.ToolInput, &input)
	}

	var resp ToolResponse
	if len(event.ToolResponse) > 0 {
		_ = json.Unmarshal(event.ToolResponse, &resp)
	}

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	summary := inputSummary(event.ToolName, input)
	status := toolStatus(resp)

	dispatchDir := filepath.Join(id.RunDir, "dispatches", id.DispatchID)
	if err := os.MkdirAll(dispatchDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dispatch dir: %w", err)
	}

	// Append to tool_trace.jsonl.
	toolEvent := ToolTraceEvent{
		Ts:           ts,
		Kind:         "tool",
		RunID:        id.RunID,
		DispatchID:   id.DispatchID,
		Role:         id.Role,
		Depth:        id.Depth,
		Tool:         event.ToolName,
		InputSummary: summary,
		Status:       status,
	}
	toolTracePath := filepath.Join(dispatchDir, "tool_trace.jsonl")
	if err := AppendJSONL(toolTracePath, toolEvent); err != nil {
		return fmt.Errorf("tool_trace.jsonl: %w", err)
	}

	// For Read/Glob/Grep, also append a kind:"read" event to context_trace.jsonl.
	if isReadTool(event.ToolName) {
		readEvent := ContextReadEvent{
			Ts:           ts,
			Kind:         "read",
			RunID:        id.RunID,
			DispatchID:   id.DispatchID,
			Role:         id.Role,
			Depth:        id.Depth,
			Tool:         event.ToolName,
			InputSummary: summary,
		}
		ctxTracePath := filepath.Join(dispatchDir, "context_trace.jsonl")
		if err := AppendJSONL(ctxTracePath, readEvent); err != nil {
			return fmt.Errorf("context_trace.jsonl: %w", err)
		}
	}

	return nil
}

// AppendJSONL opens path for appending with an exclusive flock, marshals v as
// a single JSON line, and closes (releasing the lock).
// Exported so that internal/spawn/supervise.go can append context_trace events.
func AppendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", path, err)
	}
	// Lock released on f.Close().

	return json.NewEncoder(f).Encode(v)
}
