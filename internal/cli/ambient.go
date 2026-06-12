package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func runAmbient(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tiller ambient disable|enable|status|next|doctor")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	switch args[0] {
	case "disable", "off":
		path, changed, err := ambientgate.Disable(cwd)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("tiller: ambient disabled for %s (%s)\n", cwd, path)
		} else {
			fmt.Printf("tiller: ambient already disabled for %s (%s)\n", cwd, path)
		}
		return nil

	case "enable", "on":
		path, changed, err := ambientgate.Enable(cwd)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("tiller: ambient enabled for %s\n", cwd)
		} else {
			fmt.Printf("tiller: ambient already enabled for %s (%s absent)\n", cwd, path)
		}
		return nil

	case "status":
		path := ambientgate.DisabledPath(cwd)
		if ambientgate.IsDisabled(cwd) {
			fmt.Printf("tiller: ambient disabled for %s (%s)\n", cwd, path)
		} else {
			fmt.Printf("tiller: ambient enabled for %s (%s absent)\n", cwd, path)
		}
		if digest, err := buildAmbientNextDigest(cwd, false); err == nil {
			fmt.Printf("run: %s\n", digest.runID)
			fmt.Printf("status: %s\n", valueOrUnknown(digest.runStatus))
			fmt.Printf("next_action: %s confidence=%d risk=%s budget=%s fallback=%t\n",
				valueOrUnknown(digest.decision.Action), digest.decision.Confidence,
				valueOrUnknown(digest.decision.Risk), valueOrUnknown(digest.decision.BudgetPosture), digest.fallback)
			fmt.Printf("read: %s\n", filepath.Join(digest.runDir, "status.md"))
		}
		return nil

	case "next":
		digest, err := buildAmbientNextDigest(cwd, true)
		if err != nil {
			return err
		}
		printAmbientNextDigest(digest)
		return nil

	case "doctor":
		return runAmbientDoctor()

	default:
		return fmt.Errorf("unknown ambient command %q (want disable, enable, status, next, or doctor)", args[0])
	}
}

type ambientNextDigest struct {
	cwd          string
	disabledPath string
	disabled     bool
	runDir       string
	runID        string
	runStatus    string
	decision     policy.AmbientNextActionDecision
	fallback     bool
	distillation string
}

func buildAmbientNextDigest(cwd string, requireRun bool) (*ambientNextDigest, error) {
	runDir := strings.TrimSpace(os.Getenv("TILLER_RUN_DIR"))
	if runDir == "" {
		if requireRun {
			return nil, fmt.Errorf("ambient next: TILLER_RUN_DIR is not set")
		}
		return nil, fmt.Errorf("ambient next: TILLER_RUN_DIR is not set")
	}
	if _, err := os.Stat(runDir); err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: TILLER_RUN_DIR %q: %w", runDir, err)
		}
		return nil, err
	}

	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	r, err := st.ReadRun(runID)
	if err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: read run %s: %w", runID, err)
		}
		return nil, err
	}
	dispatches, err := st.ListDispatches(runID)
	if err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: list dispatches: %w", err)
		}
		return nil, err
	}
	agents, err := st.ListAgentRuns(runID)
	if err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: list agent runs: %w", err)
		}
		return nil, err
	}
	candidates, err := st.ListCheckpointCandidates(runID)
	if err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: list checkpoint candidates: %w", err)
		}
		return nil, err
	}
	ledger, err := st.ListLedgerEvents(runID)
	if err != nil {
		if requireRun {
			return nil, fmt.Errorf("ambient next: list ledger events: %w", err)
		}
		return nil, err
	}

	opts := ambientNextStatusOptions(time.Now())
	facts := scratch.BuildAmbientNextActionFacts(r, dispatches, agents, candidates, ledger, opts)
	decision, fallback := scratch.EvalAmbientNextActionForStatusInProject(facts, r.Workspace)
	return &ambientNextDigest{
		cwd:          cwd,
		disabledPath: ambientgate.DisabledPath(cwd),
		disabled:     ambientgate.IsDisabled(cwd),
		runDir:       runDir,
		runID:        runID,
		runStatus:    r.Status,
		decision:     decision,
		fallback:     fallback,
		distillation: latestAmbientDistillationSummary(ledger),
	}, nil
}

func printAmbientNextDigest(d *ambientNextDigest) {
	state := "enabled"
	pathSuffix := fmt.Sprintf("%s absent", d.disabledPath)
	if d.disabled {
		state = "disabled"
		pathSuffix = d.disabledPath
	}
	fmt.Printf("tiller ambient: %s for %s (%s)\n", state, d.cwd, pathSuffix)
	fmt.Printf("run: %s\n", d.runID)
	fmt.Printf("status: %s\n", valueOrUnknown(d.runStatus))
	fmt.Printf("next_action: %s confidence=%d risk=%s budget=%s fallback=%t\n",
		valueOrUnknown(d.decision.Action), d.decision.Confidence,
		valueOrUnknown(d.decision.Risk), valueOrUnknown(d.decision.BudgetPosture), d.fallback)
	if d.decision.Target != "" {
		fmt.Printf("target: %s\n", oneLine(d.decision.Target))
	}
	if d.decision.Reason != "" {
		fmt.Printf("reason: %s\n", oneLine(d.decision.Reason))
	}
	if d.distillation != "" {
		fmt.Printf("distillation: %s\n", oneLine(d.distillation))
	} else {
		fmt.Println("distillation: none")
	}
	fmt.Printf("suggested_move: %s\n", suggestedAmbientMove(d.decision.Action, d.decision.Target))
	fmt.Printf("read: %s\n", filepath.Join(d.runDir, "status.md"))
}

func latestAmbientDistillationSummary(ledger []scratch.LedgerEvent) string {
	var latest *scratch.LedgerEvent
	for i := range ledger {
		ev := &ledger[i]
		if ev.Kind != "ambient.distillation" || strings.TrimSpace(ev.Summary) == "" {
			continue
		}
		if latest == nil || ev.At.After(latest.At) || ev.At.Equal(latest.At) && ev.ID > latest.ID {
			latest = ev
		}
	}
	if latest == nil {
		return ""
	}
	return truncateOneLine(latest.Summary, 240)
}

func suggestedAmbientMove(action, target string) string {
	switch action {
	case "wait":
		return "wait for in-flight agents/descriptors before integrating"
	case "distill":
		return "spawn/use tiller-summary with a bounded status-compaction prompt"
	case "debug":
		return "spawn tiller-debugger for failed work"
	case "retry":
		return "retry failed descriptor with a tighter prompt"
	case "review":
		return "spawn tiller-reviewer for policy/sandbox/conflicting checkpoint surface"
	case "checkpoint":
		return "inspect checkpoint candidate/diff, stage explicit paths, commit"
	case "ask_user":
		return "ask the user for the blocking decision or budget choice"
	case "halt":
		return "stop and compact/checkpoint before further premium spend"
	case "proceed":
		return "continue orchestration"
	case "escalate":
		return "use tiller-investigator or tiller-architect depending on blocker"
	default:
		if strings.TrimSpace(target) != "" {
			return "continue with the Arbiter target: " + oneLine(target)
		}
		return "continue orchestration"
	}
}

func ambientNextStatusOptions(updatedAt time.Time) scratch.StatusOptions {
	return scratch.StatusOptions{
		UpdatedAt:            updatedAt,
		OutputTokenBudget:    positiveInt64Env("TILLER_AMBIENT_OUTPUT_TOKEN_BUDGET"),
		ReasoningTokenBudget: positiveInt64Env("TILLER_AMBIENT_REASONING_TOKEN_BUDGET"),
		BudgetWarnRatio:      budgetWarnRatioEnv("TILLER_AMBIENT_BUDGET_WARN_RATIO"),
	}
}

func positiveInt64Env(name string) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func budgetWarnRatioEnv(name string) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 || parsed > 1 {
		return 0
	}
	return parsed
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateOneLine(s string, limit int) string {
	s = oneLine(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func valueOrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
