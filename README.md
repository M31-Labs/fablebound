# fablebound

A harness that runs Claude Code in an RLM (Recursive Language Model) pattern: a root orchestrator that can only read and dispatch, with all execution delegated to worker agents on cheaper models — enforced, auditable, and replayable.

## Why

Fable tokens are the scarce resource. Left unrestricted, a root Claude Code session can edit files, run arbitrary Bash, and burn expensive model time on mechanical execution tasks. fablebound prevents this structurally:

- The root agent is spawned with `Write`, `Edit`, `Agent`, `WebFetch`, and `WebSearch` removed from its settings and auto-denied under `dontAsk`. It has no mechanism to execute — only to read, plan, and dispatch.
- All execution runs in separate `claude -p` subprocesses on cheaper models (sonnet/haiku), spawned and supervised by fablebound itself.
- Every dispatch and every tool call at every level flows through compiled [Arbiter](https://github.com/M31-Labs/arbiter) policies — `dispatch.arb` gates and routes dispatch requests, `toolgate.arb` re-checks every tool call via the `PreToolUse` hook. Both write replayable JSONL audit trails.
- Fable is reserved for the root orchestrator and at most two explicitly-budgeted insight dispatches (`chief-architect`, `deep-report`) per run. The budget is a hard policy rule, not a suggestion.

The result is a run tree where the Fable model orchestrates and insight-dispatches, sonnet/haiku workers do the actual reading and editing, and every decision — "may this dispatch proceed?", "may this tool call fire?" — has a policy-backed audit record you can replay against the exact policy that produced it.

## Architecture

```
user: fablebound run "<task>"
 └─ fablebound (root CLI)
     ├─ creates .fablebound/runs/<run-id>/
     ├─ spawns orchestrator: claude -p <task> --model fable \
     │          --settings <generated> --permission-mode dontAsk \
     │          --append-system-prompt roles/orchestrator.md
     │  (env: FABLEBOUND_ROLE=orchestrator, FABLEBOUND_DEPTH=0,
     │        FABLEBOUND_RUN_DIR, FABLEBOUND_DISPATCH_ID=root)
     │
     │  every tool call ──▶ PreToolUse hook: fablebound hook
     │                       └─ toolgate.arb → allow/deny + audit
     │
     │  orchestrator runs: Bash(fablebound dispatch --role investigator ...)
     │    └─ fablebound dispatch (child CLI invocation in the claude process)
     │        ├─ builds DispatchRequest → dispatch.arb (rules gate + strategy route)
     │        ├─ DENIED → exit 3, policy reason on stderr (orchestrator re-plans)
     │        └─ ALLOWED → writes dispatches/<id>/{brief.md,settings.json,meta.json}
     │            └─ spawns detached fablebound _supervise <run> <id>
     │                └─ execs claude -p --model <route.model> --output-format json
     │                    captures report.md, finalizes meta.json
     │
     └─ depth-1 agents can dispatch further; depth-2 agents are terminal
        (dispatch.arb DenyTerminalDepth, toolgate DenyTerminalDispatch,
         generated settings replace Bash(fablebound *) with Bash(fablebound note *))
```

Depth is fablebound state (`FABLEBOUND_DEPTH`), set by fablebound at spawn and cross-checked against the run's dispatch meta at every hook invocation.

## Quickstart

```sh
# Install (requires Go 1.25+, claude CLI on PATH)
go install m31labs.dev/fablebound/cmd/fablebound@latest

# Initialize a project (materializes .fablebound/policy/*.arb and roles/*.md)
cd your-project
fablebound init

# Run a task
fablebound run "investigate why the payment retry queue is backing up and write a findings report"

# Inspect the run
fablebound runs list
fablebound runs show <run-id>

# Promote findings into a hyphae knowledge spore (optional, requires hypha on PATH)
fablebound promote <run-id>
```

`fablebound run` blocks until the orchestrator finishes. Progress appears on stderr. Use `fablebound runs list` to see all runs and their cost summaries.

## Ambient Mode (auto-enable on fable)

Install fablebound once globally and it self-activates whenever your main Claude Code session is running the fable model — without any project-level setup. For any other model it is completely invisible.

```sh
fablebound install        # adds hooks to ~/.claude/settings.json
fablebound uninstall      # removes them
fablebound install --print  # preview the JSON without writing
```

**How it works.** The installed `PreToolUse` hook reads the session transcript to find the model of the most recent assistant turn. If that model is `claude-fable-5` or `fable`, the hook evaluates `ambient.arb` — an orchestrator-only policy that denies Edit, Write, NotebookEdit, and Bash, while allowing Read, Glob, Grep, Agent (Task dispatch), TodoWrite, and WebFetch. For any other model the hook exits 0 immediately — it has zero effect. Subagent calls (where `agent_id` is present) also pass through unconditionally, so tools inside Task-spawned agents are unrestricted by ambient mode.

**Fail-open.** Any error reading the transcript (missing path, no assistant line yet, unreadable file) causes the hook to exit 0 — it never blocks a non-fable session. The deny reason is instructive: `fablebound: fable runs orchestrator-only — dispatch a subagent (Task) to execute; <tool> is not permitted for the root fable agent.`

**Ambient vs managed mode.** Ambient mode is invisible infrastructure: interactive, Task-based, model-switch-aware, no run artifacts. Managed mode (`fablebound run`) is explicit and heavy: it spawns processes, enforces depth, writes audit traces, and creates a full run artifact tree. Ambient mode does not write any artifacts; if there is no `FABLEBOUND_RUN_DIR`, PostToolUse exits cleanly.

**Enforcement layering.** Ambient mode is one enforcement point. It governs only the root fable session. Subagents spawned via Task run at a different model and are unaffected by ambient policy (they pass through). For full process-tree enforcement, use `fablebound run`.

## Canonical Personas

| Role | Model | Tier | Profile |
|---|---|---|---|
| `orchestrator` | fable | planning, specs, deep synthesis | orchestrator |
| `chief-architect` | fable | planning, specs, deep synthesis | insight |
| `deep-report` | fable | planning, specs, deep synthesis | insight |
| `investigator` | opus | adversarial verification, deep tracing | readonly |
| `reviewer` | opus | adversarial verification, deep tracing | readonly |
| `worker` | sonnet | writing code, executing specs | execution |
| `debugger` | sonnet | systematic debugging, writing code | execution |

Fable is reserved for roles that genuinely require the most capable model (orchestration, architecture, exhaustive synthesis). Investigator and reviewer use opus for rigorous, adversarial verification. Worker and debugger use sonnet for execution.

## Role × Model × Depth Matrix

| Role | Settings profile | Model (policy-routed) | Edit/Write | Bash | May dispatch | Depth cap |
|---|---|---|---|---|---|---|
| `orchestrator` | orchestrator | fable | denied | `fablebound *`, `hypha *` only | all roles | 0–1 |
| `chief-architect` | insight | fable | hook-gated: scratch only | `fablebound *`, `hypha *`, read-only prefixes | investigator | 0–1 |
| `deep-report` | insight | fable | hook-gated: scratch only | same as chief-architect | investigator | 0–1 |
| `investigator` | readonly | opus | denied | read-only prefixes + `fablebound *`, `hypha *` | investigator | 0–1 |
| `worker` | execution | sonnet | yes (workspace) | yes, minus deny rules | investigator, debugger | 0–1 |
| `debugger` | execution | sonnet | yes (workspace) | yes, minus deny rules | investigator | 0–1 |
| `reviewer` | readonly | opus | hook-gated: scratch only | read-only prefixes | none | 0–1 |

Depth-2 agents are blocked at three independent points: `dispatch.arb` `DenyTerminalDepth` rule, `toolgate.arb` `DenyTerminalDispatch` rule, and generated settings that remove `Bash(fablebound dispatch*)` from the allow list. Fable is routed only to `orchestrator`, `chief-architect`, and `deep-report`, only when called by the orchestrator, and only within the per-run `fable_budget` (default 2). Execution roles cannot dispatch fable roles.

Read-only Bash prefixes (investigator/reviewer/insight profiles): `ls`, `rg`, `grep`, `find`, `git log`, `git show`, `git diff`, `go doc`, `go vet`, `gts`, `wc`, `head`, `tail`, `fablebound`, `hypha`.

## Enforcement Layering

From weakest to strongest, each layer independent:

```
role .md (cooperative)
  < settings deny (removes tools from context)
    < PreToolUse hook + toolgate.arb (re-checks every call, flock-serialized audit JSONL)
      < dispatch.arb (gates every dispatch request with identity from env)
        < [future] Horizon LSM exec-deny profile (kernel-level, see §Appendix)
```

The settings `deny` list removes `Write`, `Edit`, `Agent`, `NotebookEdit`, `WebFetch`, `WebSearch` from the orchestrator's tool context entirely — under `dontAsk`, anything not allow-listed is auto-denied before the hook is even invoked. The hook handles what settings cannot express: path containment, command regex, kill switches, and audit. Hook identity (`agent.role`, `agent.depth`, `agent.dispatch_id`) is read from env set by fablebound at spawn and verified against the on-disk dispatch meta; the run dir itself is bound to the real workspace via canonical-path containment.

**Layering caveats.** The settings deny list and toolgate command-prefix rules are best-effort: a sufficiently creative command construction (`cd x && fablebound dispatch`) can evade prefix matching. The robust backstop is `dispatch.arb` keyed on env-derived `caller.depth`/`caller.role` combined with the hook's meta identity cross-check. Neither layer is effective if a worker can escape the filesystem sandbox entirely (e.g. by exploiting an unpatched kernel), which is why the Horizon LSM backstop remains on the roadmap.

**Fail closed.** Any internal error in `fablebound hook` (missing env, policy compile failure, unparseable input) exits 2, which Claude Code treats as a deny.

## Policy Customization

Policies live in `.fablebound/policy/{dispatch.arb,toolgate.arb}`. Defaults are embedded and materialized by `fablebound init`; the project copy wins. Edits take effect on the next invocation — the CLI compiles policies per invocation (~1ms; eval ~223ns).

```sh
# Compile + schema-typecheck policies; run .test.arb suites if arbiter CLI is present
fablebound policy vet

# Replay a past run's toolgate audit against a candidate policy
arbiter replay .fablebound/policy/toolgate.arb \
  --audit .fablebound/runs/<id>/audit/toolgate.jsonl

# Diff decisions between current and candidate policy
arbiter diff .fablebound/policy/dispatch.arb candidate.arb \
  --data-file audit-contexts.json
```

`fablebound policy vet` runs `arbiter check --go schemas.go --type DispatchRequest` (and `ToolCallRequest`) to typecheck policy field references against the Go input structs — field renames fail loudly at policy load, never silently misroute.

**Kill switches** are one-line edits. Add a `HaltAll priority 0` rule to `toolgate.arb` to stop all tool calls across active runs on the next hook invocation.

Policies use Arbiter's `rule` modality (many matching governed outcomes) and `strategy` for exactly-one routing. The decision combinator: lowest priority number wins; ties resolve `Deny > Ask > Allow`; no match → deny.

The `.test.arb` unit suites (`dispatch.test.arb`, `toolgate.test.arb`) ship alongside the defaults and are run by `fablebound policy vet` when `arbiter` is on PATH.

## Run Artifact Layout

Every run is a persisted directory:

```
.fablebound/runs/<run-id>/           # run-id = YYYYMMDD-HHMMSS-<4 base36>
  manifest.json                      # task, workspace, policy sha256s, status, fable_budget
  task.md                            # root task brief
  audit/
    dispatch.jsonl                   # DecisionEvents for all dispatch requests (replayable)
    toolgate.jsonl                   # DecisionEvents for all tool calls (replayable)
  notes/                             # shared scratch (any agent reads; scoped writes per role)
  dispatches/
    root/                            # orchestrator (always present)
    d01/, d02/, …/                   # numbered in spawn order
      brief.md                       # task brief written by fablebound from --brief
      report.md                      # agent final output
      settings.json                  # exact generated settings (reproducibility)
      meta.json                      # {id, parent, role, model, status, depth, cost_usd, …}
      tool_trace.jsonl               # per-tool-call PostToolUse captures (hook-derived)
      context_trace.jsonl            # read/dispatch/report events (hook-derived)
      transcript.json                # raw --output-format json result
      supervise.log                  # stdio from the claude process
```

Tool and context traces are derived from hook events and fablebound's own dispatch records — they do not depend on agent self-reporting. `context_trace.jsonl` carries `kind:"dispatch"` events (child id, role, model) from the caller's trace and `kind:"report"` from the supervisor, so replay reconstructs the full dispatch tree from `meta.json parent` fields and trace files alone.

`.fablebound/runs/` is gitignored by `fablebound init`.

```sh
# List all runs (id, status, task, dispatch count, Σcost)
fablebound runs list

# Render manifest summary + dispatch tree
fablebound runs show <run-id>
fablebound runs show <run-id> --json

# Delete oldest terminal runs beyond N most-recent
fablebound runs gc --keep 20 [--dry-run]
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `FABLEBOUND_CLAUDE_BIN` | `claude` | Path to the `claude` CLI binary |
| `FABLEBOUND_ROLE` | `user` (when outside a run) | Agent role; set by fablebound at spawn |
| `FABLEBOUND_DEPTH` | `0` | Spawn depth; set by fablebound at spawn |
| `FABLEBOUND_RUN_DIR` | — | Absolute path to the run scratch directory |
| `FABLEBOUND_DISPATCH_ID` | — | Dispatch id of the current agent (e.g. `root`, `d01`) |

The last four are set automatically by fablebound at spawn. `FABLEBOUND_CLAUDE_BIN` is the only one operators need to set, and only when `claude` is not on PATH.

## Hyphae Integration

fablebound integrates with [hyphae](https://github.com/M31-Labs/hyphae) for live observability and knowledge promotion. The integration is a soft dependency — if `hypha` is not on PATH, all calls degrade to logged skips without failing the run.

- **Traces**: `fablebound run` opens a hypha trace; every dispatch and completion emits a tick. `hypha trace list --active` shows in-flight runs.
- **Recall**: role prompts instruct agents to `hypha recall <q>` before non-trivial work. `Bash(hypha *)` is allow-listed for all profiles (minus daemon subcommands — `hypha mcp serve` and `hypha hub serve` are denied by `toolgate.arb` `DenyHyphaDaemons`).
- **Promotion**: `fablebound promote <run-id>` composes a spore from the run's task, dispatch tree, report excerpts, and an operator-editable `## Lessons` section, then runs `hypha spore submit`.

## Horizon Backstop (Deferred)

The intended kernel-level backstop is a Horizon-compiled LSM exec-deny profile scoped to the run's process subtree — ensuring the orchestrator tree cannot exec non-allowlisted binaries even if every userspace layer fails. This requires `CONFIG_BPF_LSM` (not available in stock WSL2), Horizon path/dentry helpers not yet in DSL v0.3, and a privilege story for `CAP_BPF`. Until those prerequisites are met, the enforcement ceiling is settings + hook + policy.

## Policy Schemas

Input structs are in `internal/policy/schemas.go`, also embedded so `fablebound policy vet` can pass them to `arbiter check`. Renaming an `arb`-tagged field breaks policy load immediately — no silent misroutes.

`dispatch.arb` input: `DispatchRequest` (`dispatch.*`, `caller.*`, `run.*` fields).
`toolgate.arb` input: `ToolCallRequest` (`agent.*`, `tool.*`, `run.*` fields).

## License

MIT
