package scratch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StatusOptions controls deterministic status.md rendering.
type StatusOptions struct {
	UpdatedAt            time.Time
	RecentLimit          int
	OutputTokenBudget    int64
	ReasoningTokenBudget int64
	BudgetWarnRatio      float64
}

// RenderStatusMarkdown builds a derived ambient run status snapshot from the
// canonical scratch records. The returned markdown is suitable for writing to
// status.md beside ledger.jsonl in an fs-backed run directory.
func RenderStatusMarkdown(st Store, runID string, opts StatusOptions) ([]byte, error) {
	if opts.RecentLimit <= 0 {
		opts.RecentLimit = 5
	}

	r, err := st.ReadRun(runID)
	if err != nil {
		return nil, fmt.Errorf("render status: read run: %w", err)
	}
	dispatches, err := st.ListDispatches(runID)
	if err != nil {
		return nil, fmt.Errorf("render status: list dispatches: %w", err)
	}
	agents, err := st.ListAgentRuns(runID)
	if err != nil {
		return nil, fmt.Errorf("render status: list agent runs: %w", err)
	}
	candidates, err := st.ListCheckpointCandidates(runID)
	if err != nil {
		return nil, fmt.Errorf("render status: list checkpoint candidates: %w", err)
	}
	ledger, err := st.ListLedgerEvents(runID)
	if err != nil {
		return nil, fmt.Errorf("render status: list ledger events: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Tiller Run Status\n\n")
	sb.WriteString("Generated snapshot from `manifest.json`, `dispatches/*/meta.json`, `agents/*.json`, `checkpoint_candidates.jsonl`, and `ledger.jsonl`. This file is derived for compact reading and is not a source of truth.\n\n")
	if !opts.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("Updated: %s\n\n", opts.UpdatedAt.UTC().Format(time.RFC3339)))
	}

	sb.WriteString("## Run\n\n")
	writeKV(&sb, "run_id", r.ID)
	writeKV(&sb, "status", r.Status)
	writeKV(&sb, "task", oneLine(r.Task))
	writeKV(&sb, "workspace", r.Workspace)
	writeKV(&sb, "reason_budget", fmt.Sprint(r.ReasonBudget))
	if r.MaxDepth != 0 {
		writeKV(&sb, "max_depth", fmt.Sprint(r.MaxDepth))
	}
	sb.WriteString("\n")

	sb.WriteString("## Dispatches\n\n")
	sb.WriteString(fmt.Sprintf("- total: %d\n", len(dispatches)))
	writeCounts(&sb, "- by_status", dispatchStatusCounts(dispatches))
	writeCounts(&sb, "- by_tier", dispatchTierCounts(dispatches))
	sb.WriteString("\n")

	sb.WriteString("## Agents\n\n")
	sb.WriteString(fmt.Sprintf("- total: %d\n", len(agents)))
	writeCounts(&sb, "- by_status", agentStatusCounts(agents))
	writeCounts(&sb, "- by_tier", agentTierCounts(agents))
	sb.WriteString("- recent:\n")
	for _, ar := range recentAgents(agents, opts.RecentLimit) {
		sb.WriteString(fmt.Sprintf("  - `%s` %s %s %s %s\n", ar.ID, valueOr(ar.Backend, "unknown"), valueOr(ar.Role, "unknown"), valueOr(ar.Status, "unknown"), formatTime(ar.SpawnedAt)))
		if ar.Summary != "" {
			sb.WriteString(fmt.Sprintf("    summary: %s\n", oneLine(ar.Summary)))
		}
	}
	if len(agents) == 0 {
		sb.WriteString("  - none\n")
	}
	sb.WriteString("\n")

	taskDescriptors := effectiveTaskDescriptorEvents(ledger)
	sb.WriteString("## Task Descriptors\n\n")
	sb.WriteString(fmt.Sprintf("- total: %d\n", len(taskDescriptors)))
	writeCounts(&sb, "- by_status", ledgerStatusCounts(taskDescriptors))
	sb.WriteString("- recent:\n")
	for _, ev := range recentLedgerEvents(taskDescriptors, opts.RecentLimit) {
		sb.WriteString(fmt.Sprintf("  - `%s` %s %s %s\n", valueOr(refValue(ev.Refs, "agent_type"), "unknown"), valueOr(ev.Backend, "unknown"), valueOr(ev.Status, "unknown"), formatTime(ev.At)))
		if ev.Summary != "" {
			sb.WriteString(fmt.Sprintf("    summary: %s\n", oneLine(ev.Summary)))
		}
		if len(ev.Refs) > 0 {
			sb.WriteString(fmt.Sprintf("    refs: %s\n", truncateStatusText(strings.Join(ev.Refs, ", "), 240)))
		}
	}
	if len(taskDescriptors) == 0 {
		sb.WriteString("  - none\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Checkpoint Candidates\n\n")
	sb.WriteString(fmt.Sprintf("- total: %d\n", len(candidates)))
	writeCounts(&sb, "- by_status", checkpointStatusCounts(candidates))
	sb.WriteString("- recent:\n")
	for _, c := range recentCandidates(candidates, opts.RecentLimit) {
		sb.WriteString(fmt.Sprintf("  - `%s` %s %s %s\n", c.ID, valueOr(c.Status, "unknown"), valueOr(c.AgentRunID, "no-agent"), formatTime(c.ReportedAt)))
		if c.Summary != "" {
			sb.WriteString(fmt.Sprintf("    summary: %s\n", oneLine(c.Summary)))
		}
		if len(c.ChangedFiles) > 0 {
			sb.WriteString(fmt.Sprintf("    changed_files: %s\n", strings.Join(c.ChangedFiles, ", ")))
		}
	}
	if len(candidates) == 0 {
		sb.WriteString("  - none\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Stale/Late Work\n\n")
	attentionAgents := attentionAgents(agents)
	attentionCandidates := attentionCandidates(candidates)
	if len(attentionAgents) == 0 && len(attentionCandidates) == 0 {
		sb.WriteString("- none\n\n")
	} else {
		sb.WriteString("- agents:\n")
		sb.WriteString(fmt.Sprintf("  - total: %d\n", len(attentionAgents)))
		writeCounts(&sb, "  - by_status", agentStatusCounts(attentionAgents))
		sb.WriteString("  - recent:\n")
		for _, ar := range recentAgents(attentionAgents, opts.RecentLimit) {
			sb.WriteString(fmt.Sprintf("    - `%s` %s %s %s %s\n", ar.ID, valueOr(ar.Backend, "unknown"), valueOr(ar.Role, "unknown"), valueOr(ar.Status, "unknown"), formatTime(ar.SpawnedAt)))
			writeOptionalStatusDetails(&sb, "      ", ar.Summary, ar.ChangedFiles, ar.Caveats)
		}
		if len(attentionAgents) == 0 {
			sb.WriteString("    - none\n")
		}
		sb.WriteString("- checkpoint_candidates:\n")
		sb.WriteString(fmt.Sprintf("  - total: %d\n", len(attentionCandidates)))
		writeCounts(&sb, "  - by_status", checkpointStatusCounts(attentionCandidates))
		sb.WriteString("  - recent:\n")
		for _, c := range recentCandidates(attentionCandidates, opts.RecentLimit) {
			sb.WriteString(fmt.Sprintf("    - `%s` %s %s %s\n", c.ID, valueOr(c.Status, "unknown"), valueOr(c.AgentRunID, "no-agent"), formatTime(c.ReportedAt)))
			writeOptionalStatusDetails(&sb, "      ", c.Summary, c.ChangedFiles, c.Caveats)
		}
		if len(attentionCandidates) == 0 {
			sb.WriteString("    - none\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Recommended Next Actions\n\n")
	for _, action := range recommendedNextActions(taskDescriptors, agents, candidates, opts, sumLedgerUsage(ledger), totalObservedUsage(dispatches, agents, ledger)) {
		sb.WriteString(fmt.Sprintf("- `%s`: %s\n", action.ID, action.Rationale))
	}
	sb.WriteString("\n")

	sb.WriteString("## Observed Token Usage\n\n")
	dispatchUsage := sumDispatchUsage(dispatches)
	agentUsage := sumAgentUsage(agents)
	ledgerUsage := sumLedgerUsage(ledger)
	writeUsage(&sb, "- dispatches", dispatchUsage)
	writeUsage(&sb, "- agents", agentUsage)
	writeUsage(&sb, "- ledger", ledgerUsage)
	total := TokenUsage{}
	total.add(dispatchUsage)
	total.add(agentUsage)
	total.add(ledgerUsage)
	writeUsage(&sb, "- combined_observed", total)
	sb.WriteString("\n")

	if shouldWriteSpendBudget(opts, ledgerUsage, total) {
		sb.WriteString("## Spend Budget\n\n")
		sb.WriteString("Advisory only: bands use observed token metadata from scratch records and may be incomplete; they do not enforce policy.\n\n")
		writeKV(&sb, "ledger_observed_output", fmt.Sprint(ledgerUsage.OutputTokens))
		writeKV(&sb, "ledger_observed_reasoning", fmt.Sprint(ledgerUsage.ReasoningTokens))
		writeKV(&sb, "combined_observed_output", fmt.Sprint(total.OutputTokens))
		writeKV(&sb, "combined_observed_reasoning", fmt.Sprint(total.ReasoningTokens))
		warnRatio := normalizedBudgetWarnRatio(opts)
		writeKV(&sb, "budget_warn_ratio", formatRatio(warnRatio))
		writeBudgetLine(&sb, "output", total.OutputTokens, opts.OutputTokenBudget, warnRatio)
		writeBudgetLine(&sb, "reasoning", total.ReasoningTokens, opts.ReasoningTokenBudget, warnRatio)
		sb.WriteString("\n")
	}

	sb.WriteString("## Recent Ledger Events\n\n")
	recentLedger := tailLedger(ledger, opts.RecentLimit)
	for _, ev := range recentLedger {
		sb.WriteString(fmt.Sprintf("- `%s` %s %s %s\n", valueOr(ev.Kind, "unknown"), valueOr(ev.Backend, "unknown"), valueOr(ev.Status, "unknown"), formatTime(ev.At)))
		if ev.Summary != "" {
			sb.WriteString(fmt.Sprintf("  summary: %s\n", oneLine(ev.Summary)))
		}
		if len(ev.Refs) > 0 {
			sb.WriteString(fmt.Sprintf("  refs: %s\n", strings.Join(ev.Refs, ", ")))
		}
	}
	if len(recentLedger) == 0 {
		sb.WriteString("- none\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Next Useful Read Paths\n\n")
	for _, p := range nextReadPaths(dispatches, agents, candidates, len(ledger) > 0) {
		sb.WriteString(fmt.Sprintf("- `%s`\n", p))
	}

	return []byte(sb.String()), nil
}

func writeKV(sb *strings.Builder, key, value string) {
	sb.WriteString(fmt.Sprintf("- %s: %s\n", key, valueOr(value, "unknown")))
}

func writeCounts(sb *strings.Builder, label string, counts map[string]int) {
	if len(counts) == 0 {
		sb.WriteString(label + ": none\n")
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	sb.WriteString(fmt.Sprintf("%s: %s\n", label, strings.Join(parts, ", ")))
}

func writeUsage(sb *strings.Builder, label string, u TokenUsage) {
	sb.WriteString(fmt.Sprintf("%s: input=%d output=%d cache_create=%d cache_read=%d reasoning=%d total=%d\n",
		label, u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.ReasoningTokens, u.TotalTokens))
}

func writeOptionalStatusDetails(sb *strings.Builder, indent, summary string, changedFiles, caveats []string) {
	if summary != "" {
		sb.WriteString(fmt.Sprintf("%ssummary: %s\n", indent, oneLine(summary)))
	}
	if len(changedFiles) > 0 {
		sb.WriteString(fmt.Sprintf("%schanged_files: %s\n", indent, strings.Join(changedFiles, ", ")))
	}
	if len(caveats) > 0 {
		sb.WriteString(fmt.Sprintf("%scaveats: %s\n", indent, truncateStatusText(strings.Join(oneLineStrings(caveats), "; "), 240)))
	}
}

func shouldWriteSpendBudget(opts StatusOptions, ledgerUsage, combinedUsage TokenUsage) bool {
	return opts.OutputTokenBudget > 0 ||
		opts.ReasoningTokenBudget > 0 ||
		!ledgerUsage.Empty() ||
		!combinedUsage.Empty()
}

func normalizedBudgetWarnRatio(opts StatusOptions) float64 {
	if opts.BudgetWarnRatio > 0 && opts.BudgetWarnRatio <= 1 {
		return opts.BudgetWarnRatio
	}
	return 0.80
}

func writeBudgetLine(sb *strings.Builder, name string, observed, budget int64, warnRatio float64) {
	if budget <= 0 {
		sb.WriteString(fmt.Sprintf("- %s_budget: unconfigured observed=%d band=ok\n", name, observed))
		return
	}
	sb.WriteString(fmt.Sprintf("- %s_budget: configured=%d percent_used=%s band=%s\n",
		name, budget, formatPercent(observed, budget), budgetBand(observed, budget, warnRatio)))
}

func budgetBand(observed, budget int64, warnRatio float64) string {
	if budget <= 0 {
		return "ok"
	}
	if observed >= budget {
		return "over"
	}
	if float64(observed) >= float64(budget)*warnRatio {
		return "warn"
	}
	return "ok"
}

func formatPercent(observed, budget int64) string {
	if budget <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", float64(observed)*100/float64(budget))
}

func formatRatio(ratio float64) string {
	return fmt.Sprintf("%.2f", ratio)
}

func dispatchStatusCounts(dispatches []*Dispatch) map[string]int {
	counts := map[string]int{}
	for _, d := range dispatches {
		counts[valueOr(d.Status, "unknown")]++
	}
	return counts
}

func dispatchTierCounts(dispatches []*Dispatch) map[string]int {
	counts := map[string]int{}
	for _, d := range dispatches {
		tier := d.Tier
		if tier == "" {
			tier = d.Model
		}
		counts[valueOr(tier, "unknown")]++
	}
	return counts
}

func agentStatusCounts(agents []*AgentRun) map[string]int {
	counts := map[string]int{}
	for _, ar := range agents {
		counts[valueOr(ar.Status, "unknown")]++
	}
	return counts
}

func agentTierCounts(agents []*AgentRun) map[string]int {
	counts := map[string]int{}
	for _, ar := range agents {
		counts[valueOr(ar.Tier, "unknown")]++
	}
	return counts
}

func checkpointStatusCounts(candidates []CheckpointCandidate) map[string]int {
	counts := map[string]int{}
	for _, c := range candidates {
		counts[valueOr(c.Status, "unknown")]++
	}
	return counts
}

func ledgerStatusCounts(events []LedgerEvent) map[string]int {
	counts := map[string]int{}
	for _, ev := range events {
		counts[valueOr(ev.Status, "unknown")]++
	}
	return counts
}

func taskDescriptorEvents(events []LedgerEvent) []LedgerEvent {
	out := make([]LedgerEvent, 0)
	for _, ev := range events {
		if ev.Kind == "ambient.task_descriptor" {
			out = append(out, ev)
		}
	}
	return out
}

func effectiveTaskDescriptorEvents(events []LedgerEvent) []LedgerEvent {
	descriptors := taskDescriptorEvents(events)
	resultsByDescriptor := map[string]LedgerEvent{}
	for _, ev := range events {
		if ev.Kind != "ambient.task_result" {
			continue
		}
		descriptorID := refValue(ev.Refs, "descriptor_id")
		if descriptorID == "" {
			continue
		}
		prev, ok := resultsByDescriptor[descriptorID]
		if !ok || ev.At.After(prev.At) || ev.At.Equal(prev.At) && ev.ID > prev.ID {
			resultsByDescriptor[descriptorID] = ev
		}
	}
	for i := range descriptors {
		descriptorID := refValue(descriptors[i].Refs, "descriptor_id")
		result, ok := resultsByDescriptor[descriptorID]
		if !ok {
			continue
		}
		if result.Status != "" {
			descriptors[i].Status = result.Status
		}
		if result.AgentRunID != "" {
			descriptors[i].AgentRunID = result.AgentRunID
		}
	}
	return descriptors
}

func attentionAgents(agents []*AgentRun) []*AgentRun {
	out := make([]*AgentRun, 0)
	for _, ar := range agents {
		switch ar.Status {
		case AgentRunStatusLate, AgentRunStatusStale, AgentRunStatusSuperseded:
			out = append(out, ar)
		}
	}
	return out
}

func attentionCandidates(candidates []CheckpointCandidate) []CheckpointCandidate {
	out := make([]CheckpointCandidate, 0)
	for _, c := range candidates {
		switch c.Status {
		case CheckpointStatusLateValid, CheckpointStatusLateStale, CheckpointStatusConflicting:
			out = append(out, c)
		}
	}
	return out
}

func recentAgents(agents []*AgentRun, limit int) []*AgentRun {
	out := append([]*AgentRun(nil), agents...)
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].SpawnedAt
		tj := out[j].SpawnedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func recentCandidates(candidates []CheckpointCandidate, limit int) []CheckpointCandidate {
	out := append([]CheckpointCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].ReportedAt
		tj := out[j].ReportedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func recentLedgerEvents(events []LedgerEvent, limit int) []LedgerEvent {
	out := append([]LedgerEvent(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].At
		tj := out[j].At
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func tailLedger(events []LedgerEvent, limit int) []LedgerEvent {
	if len(events) <= limit {
		return append([]LedgerEvent(nil), events...)
	}
	return append([]LedgerEvent(nil), events[len(events)-limit:]...)
}

func sumDispatchUsage(dispatches []*Dispatch) TokenUsage {
	var total TokenUsage
	for _, d := range dispatches {
		total.addPtr(d.TokenUsage)
	}
	return total
}

func sumAgentUsage(agents []*AgentRun) TokenUsage {
	var total TokenUsage
	for _, ar := range agents {
		total.addPtr(ar.TokenUsage)
	}
	return total
}

func sumLedgerUsage(events []LedgerEvent) TokenUsage {
	var total TokenUsage
	for _, ev := range events {
		total.addPtr(ev.TokenUsage)
	}
	return total
}

func totalObservedUsage(dispatches []*Dispatch, agents []*AgentRun, ledger []LedgerEvent) TokenUsage {
	total := TokenUsage{}
	total.add(sumDispatchUsage(dispatches))
	total.add(sumAgentUsage(agents))
	total.add(sumLedgerUsage(ledger))
	return total
}

type recommendedAction struct {
	ID        string
	Rationale string
}

func recommendedNextActions(descriptors []LedgerEvent, agents []*AgentRun, candidates []CheckpointCandidate, opts StatusOptions, ledgerUsage, totalUsage TokenUsage) []recommendedAction {
	var actions []recommendedAction
	attentionAgents := attentionAgents(agents)
	attentionCandidates := attentionCandidates(candidates)
	if len(attentionAgents) > 0 || len(attentionCandidates) > 0 {
		actions = append(actions, recommendedAction{
			ID:        "triage_stale_work",
			Rationale: fmt.Sprintf("attention agents=%d checkpoint_candidates=%d ids=%s", len(attentionAgents), len(attentionCandidates), compactIDs(append(agentIDs(attentionAgents), candidateIDs(attentionCandidates)...), 6)),
		})
	}
	checkpointCandidates := checkpointActionCandidates(candidates)
	if len(checkpointCandidates) > 0 {
		actions = append(actions, recommendedAction{
			ID:        "checkpoint_candidate",
			Rationale: fmt.Sprintf("review checkpoint candidates=%d ids=%s", len(checkpointCandidates), compactIDs(candidateIDs(checkpointCandidates), 6)),
		})
	}
	if budgetAdviceNeeded(opts, ledgerUsage, totalUsage) {
		warnRatio := normalizedBudgetWarnRatio(opts)
		actions = append(actions, recommendedAction{
			ID:        "compact_status",
			Rationale: fmt.Sprintf("spend budget band output=%s reasoning=%s", budgetBand(totalUsage.OutputTokens, opts.OutputTokenBudget, warnRatio), budgetBand(totalUsage.ReasoningTokens, opts.ReasoningTokenBudget, warnRatio)),
		})
	}
	if len(actions) == 0 {
		pendingDescriptors := pendingDescriptorEvents(descriptors)
		pendingAgents := pendingAgents(agents)
		if len(pendingDescriptors) > 0 || len(pendingAgents) > 0 {
			actions = append(actions, recommendedAction{
				ID:        "wait_for_agents",
				Rationale: fmt.Sprintf("outstanding descriptors=%d agents=%d ids=%s", len(pendingDescriptors), len(pendingAgents), compactIDs(append(descriptorIDs(pendingDescriptors), agentIDs(pendingAgents)...), 6)),
			})
		}
	}
	if len(actions) == 0 {
		actions = append(actions, recommendedAction{ID: "proceed", Rationale: "no stale work, checkpoint candidate, spend warning, or outstanding agent work"})
	}
	return actions
}

func checkpointActionCandidates(candidates []CheckpointCandidate) []CheckpointCandidate {
	out := make([]CheckpointCandidate, 0)
	for _, c := range candidates {
		switch c.Status {
		case CheckpointStatusProposed, CheckpointStatusFresh, CheckpointStatusLateValid:
			out = append(out, c)
		}
	}
	return out
}

func budgetAdviceNeeded(opts StatusOptions, ledgerUsage, totalUsage TokenUsage) bool {
	if !shouldWriteSpendBudget(opts, ledgerUsage, totalUsage) {
		return false
	}
	warnRatio := normalizedBudgetWarnRatio(opts)
	return budgetBand(totalUsage.OutputTokens, opts.OutputTokenBudget, warnRatio) != "ok" ||
		budgetBand(totalUsage.ReasoningTokens, opts.ReasoningTokenBudget, warnRatio) != "ok"
}

func pendingDescriptorEvents(events []LedgerEvent) []LedgerEvent {
	out := make([]LedgerEvent, 0)
	for _, ev := range events {
		switch ev.Status {
		case AgentRunStatusRequested, AgentRunStatusRunning, AgentRunStatusSpawned:
			out = append(out, ev)
		}
	}
	return out
}

func pendingAgents(agents []*AgentRun) []*AgentRun {
	out := make([]*AgentRun, 0)
	for _, ar := range agents {
		switch ar.Status {
		case AgentRunStatusRequested, AgentRunStatusRunning, AgentRunStatusSpawned:
			out = append(out, ar)
		}
	}
	return out
}

func agentIDs(agents []*AgentRun) []string {
	out := make([]string, 0, len(agents))
	for _, ar := range agents {
		out = append(out, ar.ID)
	}
	return out
}

func candidateIDs(candidates []CheckpointCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.ID)
	}
	return out
}

func descriptorIDs(events []LedgerEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		if id := refValue(ev.Refs, "descriptor_id"); id != "" {
			out = append(out, id)
			continue
		}
		out = append(out, ev.ID)
	}
	return out
}

func compactIDs(ids []string, limit int) string {
	ids = uniqueStrings(ids)
	sort.Strings(ids)
	if len(ids) == 0 {
		return "none"
	}
	if len(ids) > limit {
		return strings.Join(ids[:limit], ", ") + fmt.Sprintf(", +%d more", len(ids)-limit)
	}
	return strings.Join(ids, ", ")
}

func (u *TokenUsage) addPtr(other *TokenUsage) {
	if other == nil {
		return
	}
	u.add(*other)
}

func (u *TokenUsage) add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.TotalTokens += other.TotalTokens
}

func nextReadPaths(dispatches []*Dispatch, agents []*AgentRun, candidates []CheckpointCandidate, hasLedger bool) []string {
	paths := []string{
		"status.md",
		"manifest.json",
		"dispatches/*/meta.json",
		"agents/*.json",
		"checkpoint_candidates.jsonl",
		"ledger.jsonl",
	}
	if len(dispatches) > 0 {
		last := dispatches[len(dispatches)-1]
		paths = append(paths, fmt.Sprintf("dispatches/%s/report.md", last.ID))
	}
	if len(agents) > 0 {
		recent := recentAgents(agents, 1)[0]
		paths = append(paths, fmt.Sprintf("agents/%s.json", recent.ID))
	}
	if len(candidates) > 0 {
		paths = append(paths, "checkpoint_candidates.jsonl")
	}
	if hasLedger {
		paths = append(paths, "ledger.jsonl")
	}
	return uniqueStrings(paths)
}

func refValue(refs []string, key string) string {
	prefix := key + ":"
	for _, ref := range refs {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ""
}

func truncateStatusText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return strings.TrimSpace(value[:max-3]) + "..."
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func oneLine(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "unknown"
	}
	return value
}

func oneLineStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, oneLine(value))
	}
	return out
}

func valueOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown-time"
	}
	return t.UTC().Format(time.RFC3339)
}
