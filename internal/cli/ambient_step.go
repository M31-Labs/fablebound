package cli

import (
	"fmt"
	"path/filepath"
	"strings"
)

type ambientStepDescriptor struct {
	AgentType       string
	Profile         string
	Objective       string
	Constraint      string
	SuggestedSpawn  string
	ExpectedOutputs []string
}

func printAmbientStepDryRun(d *ambientNextDigest) {
	desc := ambientStepDescriptorFor(d.decision.Action, d.decision.Target, d.decision.Reason)

	fmt.Println("dry_run: true")
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
	fmt.Printf("agent_type: %s\n", desc.AgentType)
	fmt.Printf("profile: %s\n", desc.Profile)
	fmt.Printf("objective: %s\n", desc.Objective)
	fmt.Println("context_paths:")
	fmt.Printf("- %s\n", filepath.Join(d.runDir, "status.md"))
	fmt.Printf("- %s (fallback/raw context)\n", filepath.Join(d.runDir, "ledger.jsonl"))
	fmt.Println("constraints:")
	fmt.Println("- command is dry-run and observational only; do not spawn, edit, commit, or mutate checkpoint state while running it")
	fmt.Printf("- descriptor posture: %s\n", desc.Constraint)
	fmt.Println("expected_output:")
	for _, out := range desc.ExpectedOutputs {
		fmt.Printf("- %s\n", out)
	}
	fmt.Printf("suggested_spawn: %s\n", desc.SuggestedSpawn)
	fmt.Println("read:")
	fmt.Printf("- %s\n", filepath.Join(d.runDir, "status.md"))
}

func ambientStepDescriptorFor(action, target, reason string) ambientStepDescriptor {
	target = strings.TrimSpace(target)
	reason = strings.TrimSpace(reason)
	baseExpected := []string{
		"Outcome",
		"Distillation when useful",
		"files inspected/changed",
		"verification commands and results",
		"caveats or residual risk",
		"checkpoint candidate yes/no",
		"recommended next action",
	}

	switch action {
	case "distill":
		objective := "Refresh and compact ambient run status, reconcile stale or noisy state, and preserve the Arbiter next action."
		return ambientStepDescriptor{
			AgentType:       "tiller-summary",
			Profile:         "read-only summary",
			Objective:       objective,
			Constraint:      "read-only sandbox; inspect status.md first, then ledger.jsonl only for raw fallback context",
			SuggestedSpawn:  spawnLine("tiller-summary", objective),
			ExpectedOutputs: baseExpected,
		}
	case "debug":
		objective := "Root-cause failed work and propose or apply a focused fix when the failure is bounded and appropriate."
		return ambientStepDescriptor{
			AgentType:       "tiller-debugger",
			Profile:         "execution/debug",
			Objective:       objective,
			Constraint:      "execution sandbox with focused file scope; mutate only the failed slice and report verification",
			SuggestedSpawn:  spawnLine("tiller-debugger", objective),
			ExpectedOutputs: baseExpected,
		}
	case "retry":
		objective := "Retry the failed descriptor with tighter scope, explicit context, and a descriptor-compatible report."
		return ambientStepDescriptor{
			AgentType:       "tiller-worker",
			Profile:         "execution",
			Objective:       objective,
			Constraint:      "execution sandbox with narrow scope; avoid unrelated worktree changes and report exact changes",
			SuggestedSpawn:  spawnLine("tiller-worker", objective),
			ExpectedOutputs: baseExpected,
		}
	case "review":
		objective := "Review policy, sandbox, or conflicting checkpoint surface and report concrete risks before mutation."
		return ambientStepDescriptor{
			AgentType:       "tiller-reviewer",
			Profile:         "read-only review",
			Objective:       objective,
			Constraint:      "read-only sandbox; inspect status.md first and do not edit, commit, or resolve checkpoints",
			SuggestedSpawn:  spawnLine("tiller-reviewer", objective),
			ExpectedOutputs: baseExpected,
		}
	case "escalate":
		agent := "tiller-investigator"
		profile := "read-only investigation"
		if mentionsArchitecture(target, reason) {
			agent = "tiller-architect"
			profile = "architecture/design"
		}
		objective := "Investigate the escalated Arbiter target and return a concrete unblock plan."
		return ambientStepDescriptor{
			AgentType:       agent,
			Profile:         profile,
			Objective:       objective,
			Constraint:      "read-only unless the orchestrator creates a separate execution descriptor after review",
			SuggestedSpawn:  spawnLine(agent, objective),
			ExpectedOutputs: baseExpected,
		}
	case "wait":
		objective := "Wait for in-flight agents or descriptors, then reconcile their reports before issuing new work."
		return rootDescriptor("orchestrator", "orchestration", objective, "none - pending work should finish before spawning more agents")
	case "checkpoint":
		objective := "Inspect the checkpoint candidate and diff, then stage explicit paths and commit only after verification."
		return rootDescriptor("orchestrator", "checkpoint control", objective, "none - checkpointing is an orchestrator/VCS decision")
	case "ask_user":
		objective := "Ask the user for the blocking decision or budget choice before continuing."
		return rootDescriptor("user", "user decision", objective, "none - user input is required")
	case "halt":
		objective := "Stop further premium work and compact, checkpoint, or reset scope before proceeding."
		return rootDescriptor("orchestrator", "halt control", objective, "none - halt requires root control-plane handling")
	case "proceed":
		objective := "Continue orchestration using the Arbiter target."
		if target != "" {
			objective = "Continue orchestration using the Arbiter target: " + oneLine(target) + "."
		}
		return rootDescriptor("orchestrator", "orchestration", objective, "none - root orchestrator should continue directly")
	default:
		objective := "Continue with the Arbiter target."
		if target != "" {
			objective = "Continue with the Arbiter target: " + oneLine(target) + "."
		}
		return rootDescriptor("orchestrator", "orchestration", objective, "none - unknown action needs root interpretation before delegation")
	}
}

func rootDescriptor(agentType, profile, objective, suggested string) ambientStepDescriptor {
	return ambientStepDescriptor{
		AgentType:      agentType,
		Profile:        profile,
		Objective:      objective,
		Constraint:     "root read/search only until a separate descriptor authorizes execution; do not mutate state from this dry run",
		SuggestedSpawn: suggested,
		ExpectedOutputs: []string{
			"Outcome",
			"Distillation when useful",
			"files inspected/changed",
			"verification commands and results",
			"caveats or residual risk",
			"checkpoint candidate yes/no",
			"recommended next action",
		},
	}
}

func spawnLine(agentType, objective string) string {
	return fmt.Sprintf("spawn_agent agent_type=%q objective=%q", agentType, objective)
}

func mentionsArchitecture(values ...string) bool {
	for _, value := range values {
		v := strings.ToLower(value)
		if strings.Contains(v, "architecture") || strings.Contains(v, "architectural") || strings.Contains(v, "design") {
			return true
		}
	}
	return false
}
