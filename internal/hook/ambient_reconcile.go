package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/adapter/claudecode"
	"m31labs.dev/tiller/internal/adapter/codex"
	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

const ambientTaskResultKind = "ambient.task_result"

func reconcileAmbientPostToolUse(full HookEventFull, workspaceDir, backend string) {
	if full.AgentID != "" || ambientgate.IsDisabled(workspaceDir) {
		return
	}
	runDir := os.Getenv("TILLER_RUN_DIR")
	if runDir == "" {
		return
	}
	ambient := loadAmbientConfig(workspaceDir, backend)
	if ambient == nil {
		return
	}
	var governed bool
	switch ambient.Detector {
	case "claude-jsonl-transcript":
		_, governed = claudecode.DetectTierWithConfig(full.TranscriptPath, ambient)
	case "codex-jsonl-transcript":
		_, governed = codex.DetectTierWithEvidenceConfig(codexModelEvidence(full), full.TranscriptPath, ambient)
	default:
		return
	}
	if !governed || !isAmbientPostToolUseResultTool(full.ToolName, backend) {
		return
	}
	reconcileAmbientTaskResult(full, backend, runDir)
}

func isAmbientPostToolUseResultTool(toolName, backend string) bool {
	normalized := normalizeAmbientToolName(toolName)
	switch backend {
	case "claude-code":
		return normalized == "Task" || normalized == "Agent"
	case "codex":
		return isCodexMultiAgentLifecycleTool(toolName)
	default:
		return false
	}
}

func reconcileAmbientTaskResult(full HookEventFull, backend, runDir string) {
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))

	var input ToolInput
	if len(full.ToolInput) > 0 {
		_ = json.Unmarshal(full.ToolInput, &input)
	}
	agentType := descriptorAgentType(input)
	objective := descriptorObjective(input)
	if objective == "" {
		objective = full.ToolName
	}
	_, descriptorRef, objectiveRef := ambientTaskDescriptorID(backend, normalizeAmbientToolName(full.ToolName), agentType, objective)
	descriptorID := strings.TrimPrefix(descriptorRef, "descriptor_id:")
	refs := descriptorRefs(full.ToolName, agentType, descriptorRef, objectiveRef)

	resp := parseAmbientToolResponse(full.ToolResponse)
	report := parseAmbientReport(resp.Text)
	status := ambientResultStatus(full.ToolName, backend, resp.IsError)
	now := time.Now().UTC()
	agentRunID := ""
	if backend == "claude-code" {
		agentRunID = syntheticAmbientAgentRunID(backend, descriptorID)
		upsertAmbientAgentRun(st, runID, agentRunID, backend, "", agentType, status, now, report, refs)
	} else if backend == "codex" {
		if backendAgentID := codexBackendAgentID(full.ToolResponse); backendAgentID != "" {
			agentRunID = codexAgentRunID(backendAgentID)
			upsertAmbientAgentRun(st, runID, agentRunID, backend, backendAgentID, agentType, status, now, report, refs)
		}
	}

	eventID := ambientResultEventID(backend, full.ToolName, descriptorID, status, resp.Text)
	if !ledgerEventExists(st, runID, eventID) {
		_ = st.AppendLedgerEvent(runID, scratch.LedgerEvent{
			ID:         eventID,
			AgentRunID: agentRunID,
			Backend:    backend,
			Kind:       ambientTaskResultKind,
			Status:     status,
			At:         now,
			Summary:    ambientResultSummary(report, full.ToolName, status),
			Refs:       refs,
		})
	}
	if report.CheckpointCandidate {
		appendAmbientCheckpointCandidate(st, runID, agentRunID, backend, agentType, descriptorID, now, report, refs)
	}
	refreshAmbientStatusSnapshot(runDir, now)
}

type ambientResponse struct {
	IsError bool
	Text    string
}

func parseAmbientToolResponse(raw json.RawMessage) ambientResponse {
	var resp ToolResponse
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	text := strings.TrimSpace(resp.Output)
	if text == "" {
		text = strings.TrimSpace(jsonTextValue(raw))
	}
	return ambientResponse{IsError: resp.IsError, Text: text}
}

func jsonTextValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	values := collectTextValues(v, nil)
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func collectTextValues(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	case []any:
		for _, item := range x {
			out = collectTextValues(item, out)
		}
	case map[string]any:
		for _, key := range []string{"output", "result", "content", "text", "summary", "message"} {
			if val, ok := x[key]; ok {
				out = collectTextValues(val, out)
			}
		}
	}
	return out
}

type ambientReport struct {
	Summary             string
	ChangedFiles        []string
	Verification        []string
	Caveats             []string
	CheckpointCandidate bool
	RecommendedNext     string
}

func parseAmbientReport(text string) ambientReport {
	var report ambientReport
	section := ""
	lines := strings.Split(text, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if heading, rest, ok := ambientReportHeading(line); ok {
			section = heading
			if rest != "" {
				applyAmbientReportLine(&report, section, rest)
			}
			continue
		}
		applyAmbientReportLine(&report, section, line)
	}
	report.ChangedFiles = uniqueSortedReportValues(report.ChangedFiles)
	report.Verification = uniqueReportValues(report.Verification)
	report.Caveats = uniqueReportValues(report.Caveats)
	return report
}

func ambientReportHeading(line string) (section, rest string, ok bool) {
	trimmed := strings.TrimSpace(strings.TrimLeft(line, "#"))
	if trimmed == "" || trimmed == line && !strings.Contains(line, ":") {
		return "", "", false
	}
	head := trimmed
	rest = ""
	if idx := strings.Index(trimmed, ":"); idx >= 0 {
		head = trimmed[:idx]
		rest = strings.TrimSpace(trimmed[idx+1:])
	}
	key := strings.ToLower(strings.TrimSpace(head))
	key = strings.TrimSuffix(key, ".")
	key = strings.ReplaceAll(key, "/", " ")
	key = strings.Join(strings.Fields(key), " ")
	switch key {
	case "outcome", "summary":
		return "summary", rest, true
	case "files changed", "changed files", "files changed or inspected":
		return "changed_files", rest, true
	case "verification", "verification commands and results":
		return "verification", rest, true
	case "caveats", "caveats or residual risk", "residual risk":
		return "caveats", rest, true
	case "checkpoint candidate", "checkpoint candidate yes no":
		return "checkpoint", rest, true
	case "recommended next action":
		return "recommended_next", rest, true
	default:
		return "", "", false
	}
}

func applyAmbientReportLine(report *ambientReport, section, line string) {
	value := cleanReportListValue(line)
	if value == "" || strings.EqualFold(value, "none") {
		return
	}
	switch section {
	case "summary":
		if report.Summary == "" {
			report.Summary = value
		}
	case "changed_files":
		report.ChangedFiles = append(report.ChangedFiles, splitReportValues(value)...)
	case "verification":
		report.Verification = append(report.Verification, value)
	case "caveats":
		report.Caveats = append(report.Caveats, value)
	case "checkpoint":
		lower := strings.ToLower(value)
		if strings.Contains(lower, "yes") || strings.Contains(lower, "true") {
			report.CheckpointCandidate = true
		}
	case "recommended_next":
		if report.RecommendedNext == "" {
			report.RecommendedNext = value
		}
	}
}

func cleanReportListValue(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "-*")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "[x]")
	line = strings.TrimPrefix(line, "[ ]")
	return strings.TrimSpace(line)
}

func splitReportValues(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(strings.Trim(field, "`"))
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func uniqueSortedReportValues(values []string) []string {
	out := uniqueReportValues(values)
	sort.Strings(out)
	return out
}

func uniqueReportValues(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func ambientResultStatus(toolName, backend string, isError bool) string {
	if isError {
		return scratch.AgentRunStatusFailed
	}
	if backend == "codex" {
		switch normalizeAmbientToolName(toolName) {
		case "spawn_agent":
			return scratch.AgentRunStatusRequested
		case "close_agent":
			return scratch.AgentRunStatusClosed
		case "wait_agent", "resume_agent", "send_input":
			return scratch.AgentRunStatusRunning
		}
	}
	return scratch.AgentRunStatusCompleted
}

func syntheticAmbientAgentRunID(backend, descriptorID string) string {
	return "ambient-agent-" + hashShort(backend+"\x00"+descriptorID)
}

func ambientResultEventID(backend, toolName, descriptorID, status, text string) string {
	return "ambient-result-" + hashShort(strings.Join([]string{backend, normalizeAmbientToolName(toolName), descriptorID, status, hashShort(text)}, "\x00"))
}

func appendAmbientCheckpointCandidate(st *fsstore.FS, runID, agentRunID, backend, agentType, descriptorID string, at time.Time, report ambientReport, refs []string) {
	id := "ambient-checkpoint-" + hashShort(backend+"\x00"+descriptorID)
	existing, err := st.ListCheckpointCandidates(runID)
	if err == nil {
		for _, c := range existing {
			if c.ID == id {
				return
			}
		}
	}
	_ = st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:           id,
		AgentRunID:   agentRunID,
		Backend:      backend,
		Role:         codexAgentRole(agentType),
		Tier:         codexAgentTier(agentType),
		ReportedAt:   at,
		Status:       scratch.CheckpointStatusProposed,
		ChangedFiles: report.ChangedFiles,
		Verification: report.Verification,
		Caveats:      report.Caveats,
		Summary:      report.Summary,
		Refs:         refs,
	})
}

func upsertAmbientAgentRun(st *fsstore.FS, runID, agentRunID, backend, backendAgentID, agentType, status string, at time.Time, report ambientReport, refs []string) {
	ar := findAgentRun(st, runID, agentRunID)
	if ar == nil {
		ar = &scratch.AgentRun{
			ID:             agentRunID,
			RunID:          runID,
			Backend:        backend,
			BackendAgentID: backendAgentID,
			Role:           codexAgentRole(agentType),
			Tier:           codexAgentTier(agentType),
			SpawnedAt:      at,
			Status:         status,
			Refs:           refs,
		}
	} else {
		if ar.SpawnedAt.IsZero() {
			ar.SpawnedAt = at
		}
		if ar.BackendAgentID == "" {
			ar.BackendAgentID = backendAgentID
		}
		if ar.Role == "" {
			ar.Role = codexAgentRole(agentType)
		}
		if ar.Tier == "" {
			ar.Tier = codexAgentTier(agentType)
		}
		ar.Status = status
		ar.Refs = uniqueReportValues(append(ar.Refs, refs...))
	}
	if status == scratch.AgentRunStatusCompleted || status == scratch.AgentRunStatusFailed || status == scratch.AgentRunStatusClosed {
		completedAt := at
		ar.CompletedAt = &completedAt
	}
	reportedAt := at
	ar.ReportedAt = &reportedAt
	if report.Summary != "" {
		ar.Summary = report.Summary
	}
	if len(report.ChangedFiles) > 0 {
		ar.ChangedFiles = report.ChangedFiles
	}
	if len(report.Verification) > 0 {
		ar.Verification = report.Verification
	}
	if len(report.Caveats) > 0 {
		ar.Caveats = report.Caveats
	}
	_ = st.WriteAgentRun(runID, ar)
}

func findAgentRun(st *fsstore.FS, runID, agentRunID string) *scratch.AgentRun {
	agents, err := st.ListAgentRuns(runID)
	if err != nil {
		return nil
	}
	for _, ar := range agents {
		if ar.ID == agentRunID {
			return ar
		}
	}
	return nil
}

func ambientResultSummary(report ambientReport, toolName, status string) string {
	if report.Summary != "" {
		return report.Summary
	}
	return fmt.Sprintf("%s result %s", normalizeAmbientToolName(toolName), status)
}

func codexBackendAgentID(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return findBackendAgentID(v)
}

func findBackendAgentID(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"backend_agent_id", "agent_id"} {
			if value, ok := x[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		for _, value := range x {
			if found := findBackendAgentID(value); found != "" {
				return found
			}
		}
	case []any:
		for _, value := range x {
			if found := findBackendAgentID(value); found != "" {
				return found
			}
		}
	}
	return ""
}

func hashShort(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
