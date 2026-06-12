package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// appendCodexLifecycleRecord appends best-effort Codex lifecycle facts to the
// run ledger. It intentionally uses only TILLER_RUN_DIR + fsstore.Open so the
// ambient hook hot path never resolves/dials a configured store.
func appendCodexLifecycleRecord(full HookEventFull) {
	runDir := os.Getenv("TILLER_RUN_DIR")
	if runDir == "" {
		return
	}
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	now := time.Now().UTC()

	agentType := codexAgentType(full)
	agentRunID := ""
	if full.HookEventName == "SubagentStart" && full.AgentID != "" {
		agentRunID = codexAgentRunID(full.AgentID)
		ar := &scratch.AgentRun{
			ID:             agentRunID,
			RunID:          runID,
			Backend:        "codex",
			BackendAgentID: full.AgentID,
			Role:           codexAgentRole(agentType),
			Tier:           codexAgentTier(agentType),
			Model:          full.Model,
			SpawnedAt:      now,
			Status:         scratch.AgentRunStatusRunning,
		}
		_ = st.WriteAgentRun(runID, ar)
	}

	ev := scratch.LedgerEvent{
		ID:         codexLedgerEventID(full, now),
		AgentRunID: agentRunID,
		Backend:    "codex",
		Kind:       codexLedgerKind(full),
		Status:     codexLifecycleStatus(full),
		At:         now,
		Summary:    codexLifecycleSummary(full),
		Refs:       codexLifecycleRefs(full),
	}
	_ = st.AppendLedgerEvent(runID, ev)
}

func isCodexMultiAgentLifecycleTool(toolName string) bool {
	normalized := normalizeAmbientToolName(toolName)
	for _, name := range []string{"spawn_agent", "wait_agent", "resume_agent", "send_input", "close_agent"} {
		if normalized == name || strings.HasSuffix(toolName, name) {
			return true
		}
	}
	return false
}

func codexLedgerKind(full HookEventFull) string {
	switch full.HookEventName {
	case "SessionStart":
		return "codex.session_start"
	case "SubagentStart":
		return "codex.subagent_start"
	case "PreToolUse":
		return "codex.lifecycle_tool"
	default:
		return "codex.lifecycle"
	}
}

func codexLifecycleStatus(full HookEventFull) string {
	if full.HookEventName != "PreToolUse" {
		return "observed"
	}
	switch normalizeAmbientToolName(full.ToolName) {
	case "spawn_agent":
		return scratch.AgentRunStatusRequested
	case "close_agent":
		return scratch.AgentRunStatusClosed
	case "resume_agent", "send_input", "wait_agent":
		return scratch.AgentRunStatusRunning
	}
	for _, name := range []string{"spawn_agent", "close_agent", "resume_agent", "send_input", "wait_agent"} {
		if strings.HasSuffix(full.ToolName, name) {
			switch name {
			case "spawn_agent":
				return scratch.AgentRunStatusRequested
			case "close_agent":
				return scratch.AgentRunStatusClosed
			default:
				return scratch.AgentRunStatusRunning
			}
		}
	}
	return "observed"
}

func codexLifecycleSummary(full HookEventFull) string {
	switch full.HookEventName {
	case "SessionStart":
		return "Codex ambient session started"
	case "SubagentStart":
		agentType := codexAgentType(full)
		if agentType == "" {
			agentType = "unspecified"
		}
		return fmt.Sprintf("Codex subagent started: %s", agentType)
	case "PreToolUse":
		agentType := codexAgentType(full)
		if agentType != "" {
			return fmt.Sprintf("Codex root lifecycle tool: %s agent_type=%s", full.ToolName, agentType)
		}
		return fmt.Sprintf("Codex root lifecycle tool: %s", full.ToolName)
	default:
		return "Codex lifecycle event observed"
	}
}

func codexLifecycleRefs(full HookEventFull) []string {
	var refs []string
	if full.ToolName != "" {
		refs = append(refs, "tool:"+full.ToolName)
	}
	if full.AgentID != "" {
		refs = append(refs, "backend_agent_id:"+full.AgentID)
	}
	if agentType := codexAgentType(full); agentType != "" {
		refs = append(refs, "agent_type:"+agentType)
	}
	return refs
}

func codexLedgerEventID(full HookEventFull, at time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", full.HookEventName, full.ToolName, full.AgentID, string(full.ToolInput), at.UnixNano())))
	return "codex-ledger-" + hex.EncodeToString(sum[:8])
}

func codexAgentRunID(backendAgentID string) string {
	sum := sha256.Sum256([]byte("codex-agent\x00" + backendAgentID))
	return "codex-agent-" + hex.EncodeToString(sum[:8])
}

func codexAgentRole(agentType string) string {
	return strings.TrimPrefix(agentType, "tiller-")
}

func codexAgentTier(agentType string) string {
	switch agentType {
	case "tiller-scout", "tiller-investigator", "tiller-reviewer":
		return "scrutiny"
	case "tiller-worker", "tiller-debugger":
		return "execute"
	case "tiller-architect", "tiller-deep-report":
		return "reason"
	default:
		return ""
	}
}
