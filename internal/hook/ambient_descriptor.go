package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

const ambientTaskDescriptorKind = "ambient.task_descriptor"

func appendAmbientTaskDescriptor(full HookEventFull, backend string) {
	if !isAmbientTaskDescriptorTool(full.ToolName, backend) {
		return
	}
	runDir := os.Getenv("TILLER_RUN_DIR")
	if runDir == "" {
		return
	}

	var input ToolInput
	if len(full.ToolInput) > 0 {
		_ = json.Unmarshal(full.ToolInput, &input)
	}
	agentType := descriptorAgentType(input)
	objective := descriptorObjective(input)
	if objective == "" {
		objective = full.ToolName
	}

	descriptorID, descriptorRef, objectiveRef := ambientTaskDescriptorID(backend, normalizeAmbientToolName(full.ToolName), agentType, objective)
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	if ledgerEventExists(st, runID, descriptorID) {
		return
	}

	now := time.Now().UTC()
	ev := scratch.LedgerEvent{
		ID:      descriptorID,
		Backend: backend,
		Kind:    ambientTaskDescriptorKind,
		Status:  scratch.AgentRunStatusRequested,
		At:      now,
		Summary: descriptorSummary(agentType, objective),
		Refs:    descriptorRefs(full.ToolName, agentType, descriptorRef, objectiveRef),
	}
	_ = st.AppendLedgerEvent(runID, ev)
	refreshAmbientStatusSnapshot(runDir, now)
}

func isAmbientTaskDescriptorTool(toolName, backend string) bool {
	normalized := normalizeAmbientToolName(toolName)
	switch backend {
	case "claude-code":
		return normalized == "Task" || normalized == "Agent"
	case "codex":
		return normalized == "spawn_agent" || strings.HasSuffix(toolName, "spawn_agent")
	default:
		return false
	}
}

func descriptorAgentType(input ToolInput) string {
	if input.AgentType != "" {
		return input.AgentType
	}
	return input.SubagentType
}

func descriptorObjective(input ToolInput) string {
	for _, value := range []string{input.Description, input.Prompt, input.Message} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, item := range input.Items {
		if strings.TrimSpace(item.Text) != "" {
			return strings.TrimSpace(item.Text)
		}
	}
	return ""
}

func descriptorSummary(agentType, objective string) string {
	if agentType == "" {
		agentType = "unspecified"
	}
	first := firstDescriptorLine(objective)
	if first == "" {
		first = "task descriptor requested"
	}
	return fmt.Sprintf("%s: %s", agentType, truncateDescriptorText(first, 140))
}

func descriptorRefs(toolName, agentType, descriptorRef, objectiveRef string) []string {
	refs := []string{}
	if toolName != "" {
		refs = append(refs, "tool:"+normalizeAmbientToolName(toolName))
	}
	if agentType != "" {
		refs = append(refs, "agent_type:"+agentType)
	}
	refs = append(refs, descriptorRef, objectiveRef)
	return refs
}

func ambientTaskDescriptorID(backend, toolName, agentType, objective string) (eventID, descriptorRef, objectiveRef string) {
	objectiveSum := sha256.Sum256([]byte(strings.TrimSpace(objective)))
	objectiveHash := hex.EncodeToString(objectiveSum[:8])
	sum := sha256.Sum256([]byte(strings.Join([]string{
		backend,
		toolName,
		agentType,
		objectiveHash,
	}, "\x00")))
	descriptorHash := hex.EncodeToString(sum[:8])
	return "ambient-task-" + descriptorHash, "descriptor_id:" + descriptorHash, "objective_hash:" + objectiveHash
}

func firstDescriptorLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateDescriptorText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return strings.TrimSpace(value[:max-3]) + "..."
}
