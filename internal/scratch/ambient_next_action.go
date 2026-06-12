package scratch

import (
	"strings"
	"time"

	"m31labs.dev/tiller/internal/policy"
)

// BuildAmbientNextActionFacts derives the compact Arbiter input facts for
// ambient next-action routing from canonical scratch state.
func BuildAmbientNextActionFacts(r *Run, dispatches []*Dispatch, agents []*AgentRun, candidates []CheckpointCandidate, ledger []LedgerEvent, opts StatusOptions) policy.AmbientNextActionRequest {
	taskDescriptors := effectiveTaskDescriptorEvents(ledger)
	distillations := distillationLedgerEvents(ledger)
	totalUsage := totalObservedUsage(dispatches, agents, ledger)
	warnRatio := normalizedBudgetWarnRatio(opts)

	facts := policy.AmbientNextActionRequest{
		RunOutputBudgetBand:        budgetBand(totalUsage.OutputTokens, opts.OutputTokenBudget, warnRatio),
		RunReasoningBudgetBand:     budgetBand(totalUsage.ReasoningTokens, opts.ReasoningTokenBudget, warnRatio),
		WorkPendingDescriptorCount: len(pendingDescriptorEvents(taskDescriptors)),
		WorkPendingAgentCount:      len(pendingAgents(agents)),
		WorkStaleAgentCount:        len(attentionAgents(agents)),
		DistillationAvailable:      len(distillations) > 0,
		DistillationCount:          len(distillations),
		DistillationStatus:         distillationFreshness(distillations, opts.UpdatedAt),
		CheckpointHasVerification:  checkpointHasVerification(candidates),
		RiskChangedFilesCount:      len(changedFilesFromAmbientState(agents, candidates)),
		WorkIterationCount:         ambientIterationCount(ledger, distillations),
	}
	if r != nil {
		facts.RunStatus = r.Status
		facts.RunReasonBudget = r.ReasonBudget
	}
	facts.RunReasonBudgetUsed = reasonDispatchCount(dispatches)
	facts.WorkFailedDescriptorCount = failedDescriptorCount(taskDescriptors)
	facts.WorkFailedAgentCount = failedAgentCount(agents)
	facts.CheckpointFreshCount = checkpointCount(candidates, CheckpointStatusFresh)
	facts.CheckpointProposedCount = checkpointCount(candidates, CheckpointStatusProposed)
	facts.CheckpointLateCount = checkpointCount(candidates, CheckpointStatusLateValid, CheckpointStatusLateStale)
	facts.CheckpointConflictingCount = checkpointCount(candidates, CheckpointStatusConflicting)
	changedFiles := changedFilesFromAmbientState(agents, candidates)
	facts.RiskTouchesPolicy = touchesAnyPath(changedFiles, isPolicyPath)
	facts.RiskTouchesSandbox = touchesAnyPath(changedFiles, isSandboxPath)
	facts.DistillationAgeSeconds = distillationAgeSeconds(distillations, opts.UpdatedAt)

	return facts
}

func EvalAmbientNextActionForStatus(facts policy.AmbientNextActionRequest) (policy.AmbientNextActionDecision, bool) {
	return EvalAmbientNextActionForStatusInProject(facts, "")
}

func EvalAmbientNextActionForStatusInProject(facts policy.AmbientNextActionRequest, projectDir string) (policy.AmbientNextActionDecision, bool) {
	loaded, err := policy.Load("ambient_next_action", projectDir)
	if err != nil {
		return fallbackAmbientNextAction(facts, "policy load failed"), true
	}
	res, err := policy.EvalAmbientNextAction(loaded, facts)
	if err != nil {
		return fallbackAmbientNextAction(facts, "policy evaluation failed"), true
	}
	if res.Decision.Action == "" {
		return fallbackAmbientNextAction(facts, "policy returned empty action"), true
	}
	return res.Decision, false
}

func fallbackAmbientNextAction(facts policy.AmbientNextActionRequest, reason string) policy.AmbientNextActionDecision {
	if facts.RunOutputBudgetBand == "over" || facts.RunReasoningBudgetBand == "over" {
		return policy.AmbientNextActionDecision{
			Action:        "halt",
			Confidence:    50,
			Risk:          "high",
			Reason:        reason + "; fallback saw over-budget spend",
			Target:        "budget",
			BudgetPosture: "over",
		}
	}
	if facts.WorkPendingDescriptorCount > 0 || facts.WorkPendingAgentCount > 0 {
		return policy.AmbientNextActionDecision{
			Action:        "wait",
			Confidence:    50,
			Risk:          "low",
			Reason:        reason + "; fallback saw pending work",
			Target:        "agents",
			BudgetPosture: maxBudgetPosture(facts),
		}
	}
	return policy.AmbientNextActionDecision{
		Action:        "proceed",
		Confidence:    50,
		Risk:          "medium",
		Reason:        reason + "; deterministic fallback",
		Target:        "orchestrator",
		BudgetPosture: maxBudgetPosture(facts),
	}
}

func reasonDispatchCount(dispatches []*Dispatch) int {
	count := 0
	for _, d := range dispatches {
		if d.Tier == "reason" {
			count++
		}
	}
	return count
}

func failedDescriptorCount(events []LedgerEvent) int {
	count := 0
	for _, ev := range events {
		switch ev.Status {
		case "failed", "halted", "denied":
			count++
		}
	}
	return count
}

func failedAgentCount(agents []*AgentRun) int {
	count := 0
	for _, ar := range agents {
		switch ar.Status {
		case AgentRunStatusFailed, AgentRunStatusHalted:
			count++
		}
	}
	return count
}

func checkpointCount(candidates []CheckpointCandidate, statuses ...string) int {
	want := map[string]bool{}
	for _, status := range statuses {
		want[status] = true
	}
	count := 0
	for _, c := range candidates {
		if want[c.Status] {
			count++
		}
	}
	return count
}

func checkpointHasVerification(candidates []CheckpointCandidate) bool {
	for _, c := range candidates {
		switch c.Status {
		case CheckpointStatusProposed, CheckpointStatusFresh:
		default:
			continue
		}
		if len(c.Verification) > 0 {
			return true
		}
	}
	return false
}

func distillationAgeSeconds(events []LedgerEvent, now time.Time) int {
	if len(events) == 0 || now.IsZero() {
		return 0
	}
	recent := recentLedgerEvents(events, 1)[0]
	if recent.At.IsZero() || now.Before(recent.At) {
		return 0
	}
	return int(now.Sub(recent.At).Seconds())
}

func distillationFreshness(events []LedgerEvent, now time.Time) string {
	if len(events) == 0 {
		return ""
	}
	age := distillationAgeSeconds(events, now)
	if age == 0 {
		return "fresh"
	}
	if age >= int((2 * time.Hour).Seconds()) {
		return "stale"
	}
	return "fresh"
}

func changedFilesFromAmbientState(agents []*AgentRun, candidates []CheckpointCandidate) []string {
	var files []string
	for _, ar := range agents {
		files = append(files, ar.ChangedFiles...)
	}
	for _, c := range candidates {
		files = append(files, c.ChangedFiles...)
	}
	return uniqueStrings(files)
}

func touchesAnyPath(paths []string, pred func(string) bool) bool {
	for _, p := range paths {
		if pred(p) {
			return true
		}
	}
	return false
}

func isPolicyPath(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	return strings.HasPrefix(path, "policy/") ||
		strings.HasPrefix(path, ".tiller/policy/") ||
		strings.HasPrefix(path, "internal/policy/")
}

func isSandboxPath(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	return strings.Contains(path, "sandbox") ||
		strings.HasPrefix(path, "internal/hook/") ||
		strings.HasPrefix(path, "internal/ambientgate/")
}

func ambientIterationCount(events []LedgerEvent, distillations []LedgerEvent) int {
	attemptIDs := map[string]bool{}
	for _, ev := range events {
		if ev.Kind != "ambient.task_descriptor" && ev.Kind != "ambient.task_result" {
			continue
		}
		if attemptID := refValue(ev.Refs, "attempt_id"); attemptID != "" {
			attemptIDs[attemptID] = true
		}
	}
	if len(attemptIDs) > len(distillations) {
		return len(attemptIDs)
	}
	return len(distillations)
}

func maxBudgetPosture(facts policy.AmbientNextActionRequest) string {
	if facts.RunOutputBudgetBand == "over" || facts.RunReasoningBudgetBand == "over" {
		return "over"
	}
	if facts.RunOutputBudgetBand == "warn" || facts.RunReasoningBudgetBand == "warn" {
		return "warn"
	}
	return "ok"
}
