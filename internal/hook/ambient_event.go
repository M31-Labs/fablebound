package hook

import (
	"encoding/json"
	"strings"

	"m31labs.dev/tiller/internal/scratch"
)

// ambientEvent is the backend-neutral shape used by advisory ambient
// descriptor/result bookkeeping. It keeps logical task identity separate from
// attempt identity so repeated attempts roll up together without disappearing.
type ambientEvent struct {
	Backend            string
	HookEventName      string
	ToolName           string
	NormalizedToolName string
	AgentType          string
	Objective          string
	DescriptorID       string
	DescriptorRef      string
	ObjectiveRef       string
	AttemptID          string
	AttemptRef         string
	BackendAgentID     string
	Model              string
	Effort             string
	TokenUsage         *scratch.TokenUsage
}

type ambientAttemptMode int

const (
	ambientAttemptNext ambientAttemptMode = iota
	ambientAttemptLatest
)

func newAmbientEvent(full HookEventFull, backend string, st scratch.Store, runID string, mode ambientAttemptMode) ambientEvent {
	var input ToolInput
	if len(full.ToolInput) > 0 {
		_ = json.Unmarshal(full.ToolInput, &input)
	}

	toolName := normalizeAmbientToolName(full.ToolName)
	agentType := descriptorAgentType(input)
	if agentType == "" {
		agentType = strings.TrimSpace(full.AgentType)
	}
	objective := descriptorObjective(input)
	if objective == "" {
		objective = full.ToolName
	}
	_, descriptorRef, objectiveRef := ambientTaskDescriptorID(backend, toolName, agentType, objective)
	descriptorID := strings.TrimPrefix(descriptorRef, "descriptor_id:")

	backendAgentID := strings.TrimSpace(full.AgentID)
	if backend == "codex" {
		if id := codexBackendAgentID(full.ToolResponse); id != "" {
			backendAgentID = id
		}
	}
	attemptID := ambientAttemptID(st, runID, descriptorID, mode)

	return ambientEvent{
		Backend:            backend,
		HookEventName:      full.HookEventName,
		ToolName:           full.ToolName,
		NormalizedToolName: toolName,
		AgentType:          agentType,
		Objective:          objective,
		DescriptorID:       descriptorID,
		DescriptorRef:      descriptorRef,
		ObjectiveRef:       objectiveRef,
		AttemptID:          attemptID,
		AttemptRef:         "attempt_id:" + attemptID,
		BackendAgentID:     backendAgentID,
		Model:              strings.TrimSpace(full.Model),
		Effort:             ambientEffort(full),
		TokenUsage:         copyAmbientTokenUsage(full),
	}
}

func ambientEffort(full HookEventFull) string {
	for _, value := range []string{full.Effort, full.ReasoningEffort, full.ModelReasoningEffort} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func copyAmbientTokenUsage(full HookEventFull) *scratch.TokenUsage {
	for _, usage := range []*scratch.TokenUsage{full.TokenUsage, full.Usage} {
		if usage != nil && !usage.Empty() {
			cp := *usage
			return &cp
		}
	}
	return nil
}

func ambientAttemptID(st scratch.Store, runID, descriptorID string, mode ambientAttemptMode) string {
	if st == nil || runID == "" || descriptorID == "" {
		return "attempt-" + hashShort(descriptorID+"\x00unknown")
	}
	events, err := st.ListLedgerEvents(runID)
	if err != nil {
		return "attempt-" + hashShort(descriptorID+"\x00unknown")
	}
	latest := ""
	count := 0
	for _, ev := range events {
		if ev.Kind != ambientTaskDescriptorKind || ambientRefValue(ev.Refs, "descriptor_id") != descriptorID {
			continue
		}
		count++
		if attemptID := ambientRefValue(ev.Refs, "attempt_id"); attemptID != "" {
			latest = attemptID
		}
	}
	if mode == ambientAttemptLatest && latest != "" {
		return latest
	}
	if mode == ambientAttemptLatest && count > 0 {
		return ambientAttemptIDForOrdinal(descriptorID, count)
	}
	return ambientAttemptIDForOrdinal(descriptorID, count+1)
}

func ambientAttemptIDForOrdinal(descriptorID string, ordinal int) string {
	return "attempt-" + hashShort(descriptorID+"\x00"+intFingerprint(ordinal))
}

func intFingerprint(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	n := v
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func ambientEventRefs(ev ambientEvent) []string {
	refs := []string{}
	if ev.ToolName != "" {
		refs = append(refs, "tool:"+ev.NormalizedToolName)
	}
	if ev.AgentType != "" {
		refs = append(refs, "agent_type:"+ev.AgentType)
	}
	if ev.DescriptorRef != "" {
		refs = append(refs, ev.DescriptorRef)
	}
	if ev.ObjectiveRef != "" {
		refs = append(refs, ev.ObjectiveRef)
	}
	if ev.AttemptRef != "" {
		refs = append(refs, ev.AttemptRef)
	}
	if ev.BackendAgentID != "" {
		refs = append(refs, "backend_agent_id:"+ev.BackendAgentID)
	}
	if ev.Model != "" {
		refs = append(refs, "model:"+ev.Model)
	}
	if ev.Effort != "" {
		refs = append(refs, "effort:"+ev.Effort)
	}
	return refs
}

func ambientDescriptorEventID(ev ambientEvent) string {
	return "ambient-task-" + hashShort(ev.Backend+"\x00"+ev.DescriptorID+"\x00"+ev.AttemptID)
}

func ambientRefValue(refs []string, key string) string {
	prefix := key + ":"
	for _, ref := range refs {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ""
}
