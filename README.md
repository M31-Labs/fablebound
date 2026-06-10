# tiller

A harness that gates Claude Code sessions with compiled [Arbiter](https://github.com/odvcencio/arbiter) policies, enforces model-cost discipline, and makes the fable model's orchestrator-only role the default ambient experience.

## Why

Fable tokens are expensive. When you have fable access, the natural waste pattern is using it for mechanical execution — writing files, running commands, editing boilerplate. tiller prevents this structurally: the fable model orchestrates and reasons; cheaper models (sonnet, opus) do the work. In ambient mode this happens automatically inside your normal `claude` session without any project setup.

## Quickstart: Ambient Mode (recommended)

Ambient mode is the primary way to use tiller. It self-activates whenever your Claude Code session is running the fable model and is completely invisible for every other model.

```sh
# Install (requires Go 1.25+)
go install m31labs.dev/tiller/cmd/tiller@latest

# Install hooks and tiller-* subagent personas globally
tiller install

# Preview what would be installed without writing
tiller install --print

# Install into the current project only (repo-local)
tiller install --project

# Remove everything tiller installed
tiller uninstall
```

`tiller install` does two things:
1. Merges `PreToolUse` and `PostToolUse` hook entries into `~/.claude/settings.json`
2. Writes the six tiller-* subagent persona files into `~/.claude/agents/`

Then, in any `claude` session:

```
/model fable
```

Ambient mode engages immediately. The fable root is restricted to orchestration-only tools (Read, Glob, Grep, Agent/Task, TodoWrite, WebFetch). Execution is automatically delegated to tiller-* subagents on cheaper models.

**How it works.** The installed `PreToolUse` hook reads the session transcript to find the model of the most recent assistant turn. If that model is `claude-fable-5` or `fable`, the hook evaluates `ambient.arb` — an orchestrator-only policy that denies Edit, Write, NotebookEdit, and Bash, while allowing Read, Glob, Grep, Agent/Task dispatch, TodoWrite, and WebFetch. For any other model the hook exits 0 immediately. Subagent calls (where `agent_id` is present) pass through unconditionally.

**Fail-open.** Any error reading the transcript (missing path, no assistant line, unreadable file) causes the hook to exit 0. It never blocks a non-fable session. Model switches (e.g. `/model sonnet` → `/model fable`) are tracked live via the transcript.

## Canonical Subagent Personas

When the fable root delegates via the Agent/Task tool, these personas route work to cheaper models:

| Persona | Model | Use for |
|---|---|---|
| `tiller-worker` | sonnet | Writing/editing code, running builds and tests, all file-mutating work |
| `tiller-debugger` | sonnet | Systematic debugging — root-cause, fix, verify |
| `tiller-investigator` | opus | Deep read-only investigation, code tracing, adversarial verification |
| `tiller-reviewer` | opus | Code review — correctness, security, quality |
| `tiller-architect` | fable | Architectural specs, deep design, complex trade-off analysis |
| `tiller-deep-report` | fable | Exhaustive multi-source research reports |

The deny reason when fable tries to execute directly names these personas: `"tiller: fable is orchestrator-only — delegate this with the Task tool: code changes → tiller-worker (sonnet), debugging → tiller-debugger (sonnet), investigation → tiller-investigator (opus), review → tiller-reviewer (opus); reserve fable for tiller-architect/tiller-deep-report. (<tool> blocked for the root fable agent.)"` — this steers the orchestrator toward the right persona without a second prompt.

**Enforcement layering.** Ambient mode governs the root fable session only. Subagents spawned via Task are unaffected by ambient policy — they pass through. Their model is baked into the persona frontmatter (`model: sonnet`/`opus`/`fable`), which is the primary cost lever. The hook cannot inspect the target model of an Agent/Task invocation (the `tool_input` payload does not expose it), so persona frontmatter is the enforcement point for subagent model routing.

## Managed Mode: `tiller run` (optional)

`tiller run` is the heavyweight alternative: it spawns agents as separate `claude -p` processes, enforces dispatch depth, writes full audit trails, and creates a run artifact tree. Use it when you need replayable audits, process-tree isolation, or the full dispatch → report → promote workflow.

```sh
# Initialize a project (materializes .tiller/policy/*.arb and roles/*.md)
cd your-project
tiller init

# Run a task
tiller run "investigate why the payment retry queue is backing up and write a findings report"

# Inspect the run
tiller runs list
tiller runs show <run-id>

# Promote findings into a hyphae knowledge spore (optional, requires hypha on PATH)
tiller promote <run-id>
```

`tiller run` blocks until the orchestrator finishes. Progress appears on stderr.

### Architecture

```
user: tiller run "<task>"
 └─ tiller (root CLI)
     ├─ creates .tiller/runs/<run-id>/
     ├─ spawns orchestrator: claude -p <task> --model fable \
     │          --settings <generated> --permission-mode dontAsk \
     │          --append-system-prompt roles/orchestrator.md
     │  (env: TILLER_ROLE=orchestrator, TILLER_DEPTH=0,
     │        TILLER_RUN_DIR, TILLER_DISPATCH_ID=root)
     │
     │  every tool call ──▶ PreToolUse hook: tiller hook
     │                       └─ toolgate.arb → allow/deny + audit
     │
     │  orchestrator runs: Bash(tiller dispatch --role investigator ...)
     │    └─ tiller dispatch (child CLI invocation in the claude process)
     │        ├─ builds DispatchRequest → dispatch.arb (rules gate + strategy route)
     │        ├─ DENIED → exit 3, policy reason on stderr (orchestrator re-plans)
     │        └─ ALLOWED → writes dispatches/<id>/{brief.md,settings.json,meta.json}
     │            └─ spawns detached tiller _supervise <run> <id>
     │                └─ execs claude -p --model <route.model> --output-format json
     │                    captures report.md, finalizes meta.json
     │
     └─ depth-1 agents can dispatch further; depth-2 agents are terminal
        (dispatch.arb DenyTerminalDepth, toolgate DenyTerminalDispatch,
         generated settings replace Bash(tiller *) with Bash(tiller note *))
```

## Ambient vs Managed Mode

| | Ambient | Managed (`tiller run`) |
|---|---|---|
| Setup | `tiller install` once | `tiller init` per project |
| Session | Normal interactive `claude` | Spawned `claude -p` processes |
| Enforcement | Root fable session only | Full process tree, every agent |
| Artifacts | None | Full run dir, JSONL audit trails |
| Depth | Flat (subagents cannot spawn subagents) | Up to depth-2 dispatch trees |
| Model routing | Persona frontmatter | `dispatch.arb` strategy rules |

## Managed Mode: Role × Model Matrix

| Role | Model | Profile | Edit/Write | Bash | May dispatch |
|---|---|---|---|---|---|
| `orchestrator` | fable | orchestrator | denied | `tiller *`, `hypha *` only | all roles |
| `chief-architect` | fable | insight | scratch only | read-only prefixes | investigator |
| `deep-report` | fable | insight | scratch only | read-only prefixes | investigator |
| `investigator` | opus | readonly | denied | read-only prefixes | investigator |
| `worker` | sonnet | execution | yes (workspace) | yes | investigator, debugger |
| `debugger` | sonnet | execution | yes (workspace) | yes | investigator |
| `reviewer` | opus | readonly | scratch only | read-only prefixes | none |

Fable-model dispatches (`chief-architect`, `deep-report`) are limited per run (default 2). The fable budget is a hard policy rule in `dispatch.arb`.

## Enforcement Layering

From weakest to strongest, each layer independent:

```
role .md (cooperative)
  < settings deny (removes tools from context)
    < PreToolUse hook + toolgate.arb (re-checks every call, flock-serialized audit JSONL)
      < dispatch.arb (gates every dispatch request with identity from env)
        < [future] Horizon LSM exec-deny profile (kernel-level, see §Appendix)
```

**Ambient mode** adds one enforcement point at the top of the root fable session. It governs only the root; subagents pass through. **Managed mode** applies toolgate at every level.

**Fail closed.** Any internal error in `tiller hook` (missing env, policy compile failure, unparseable input) exits 2, which Claude Code treats as a deny. Ambient mode fails open (exit 0) on transcript read errors.

**Layering caveats.** The settings deny list and toolgate command-prefix rules are best-effort. The robust backstop is `dispatch.arb` keyed on env-derived `caller.depth`/`caller.role` combined with the hook's meta identity cross-check.

## Policy Customization

Policies live in `.tiller/policy/{dispatch.arb,toolgate.arb}`. Defaults are embedded and materialized by `tiller init`; the project copy wins.

```sh
# Compile + schema-typecheck policies
tiller policy vet

# Replay a past run's toolgate audit against a candidate policy
arbiter replay .tiller/policy/toolgate.arb \
  --audit .tiller/runs/<id>/audit/toolgate.jsonl
```

**Kill switches** are one-line edits. Add a `HaltAll priority 0` rule to `toolgate.arb` to stop all tool calls across active runs on the next hook invocation.

## Run Artifact Layout

```
.tiller/runs/<run-id>/           # run-id = YYYYMMDD-HHMMSS-<4 base36>
  manifest.json                      # task, workspace, policy sha256s, status, fable_budget
  task.md                            # root task brief
  audit/
    dispatch.jsonl                   # DecisionEvents for all dispatch requests (replayable)
    toolgate.jsonl                   # DecisionEvents for all tool calls (replayable)
  notes/                             # shared scratch
  dispatches/
    root/                            # orchestrator
    d01/, d02/, …/
      brief.md  report.md  settings.json  meta.json
      tool_trace.jsonl  context_trace.jsonl  transcript.json  supervise.log
```

```sh
tiller runs list
tiller runs show <run-id>
tiller runs gc --keep 20 [--dry-run]
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `TILLER_CLAUDE_BIN` | `claude` | Path to the `claude` CLI binary |
| `TILLER_ROLE` | — | Agent role; set by tiller at spawn |
| `TILLER_DEPTH` | `0` | Spawn depth; set by tiller at spawn |
| `TILLER_RUN_DIR` | — | Absolute path to the run scratch directory |
| `TILLER_DISPATCH_ID` | — | Dispatch id of the current agent |

## Hyphae Integration

tiller integrates with [hyphae](https://github.com/M31-Labs/hyphae) for live observability and knowledge promotion. If `hypha` is not on PATH, all calls degrade to logged skips.

- **Traces**: `tiller run` opens a hypha trace; every dispatch emits a tick.
- **Recall**: role prompts instruct agents to `hypha recall <q>` before non-trivial work.
- **Promotion**: `tiller promote <run-id>` composes a spore from the run's task, dispatch tree, and report excerpts.

## Policy Schemas

Input structs are in `internal/policy/schemas.go`. `dispatch.arb` input: `DispatchRequest`. `toolgate.arb` input: `ToolCallRequest`. Renaming an `arb`-tagged field breaks policy load immediately.

## Horizon Backstop (Deferred)

Intended kernel-level backstop: a Horizon-compiled LSM exec-deny profile. Requires `CONFIG_BPF_LSM` (not in stock WSL2), Horizon DSL v0.3+ helpers, and `CAP_BPF`. Until prerequisites are met, the enforcement ceiling is settings + hook + policy.

## License

MIT
