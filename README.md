# tiller

A governance layer for interactive coding agents with compiled [Arbiter](https://github.com/odvcencio/arbiter) policies, tier-cost discipline, and an orchestrator/executor split. Claude Code and Codex ambient mode are implemented today through backend-specific adapters.

Tiller is not a model, not a new reasoning architecture, not a true Recursive Language Model (RLM) implementation, and not a security sandbox. It is RLM-inspired only in the loose harness sense: work can recurse through delegated agent tasks, but the recursion happens at the orchestration/control layer around existing interactive coding agents. Its value is practical control: keeping expensive reasoning sessions focused on orchestration, separating roles by cost and authority, gating tool use with policy, and leaving audit trails that can be inspected or replayed.

## Why

Reason-tier tokens are expensive. When you have reason-tier access, the natural waste pattern is using it for mechanical execution - writing files, running commands, editing boilerplate. tiller prevents this structurally: the reason-tier model orchestrates and reasons; cheaper tiers do the work. The strongest path today is Claude Code, with Codex ambient support also implemented. Arbiter policies, Hyphae observability/promotion, and the deferred Horizon backstop are implementation choices and optional integrations, not claims of general agent ecosystem adoption or benchmarked reasoning improvements.

## Quickstart: Ambient Mode

Ambient mode self-activates whenever your agent session is running a configured reason-tier model and is completely invisible for every other model.

```sh
# Install (requires Go 1.25+)
go install m31labs.dev/tiller/cmd/tiller@latest

# Choose Claude Code, Codex, OpenCode, or all and install into the current project
tiller install

# Preview what would be installed without writing
tiller install --backend codex --project --print

# Automation-friendly explicit install
tiller install --backend codex --project

# Verify the project Codex ambient install end-to-end
tiller codex doctor

# User-scope install
tiller install --backend claude-code --global

# Remove the project Codex install
tiller uninstall --backend codex --project

# Preview the Codex removal without writing
tiller uninstall --backend codex --project --print

# Temporarily bypass ambient enforcement while testing
tiller ambient disable
tiller ambient enable
```

**Trialing is safe.** `tiller uninstall --backend <name> --project` reverts the matching project install in one shot. For Codex that includes hooks, managed tiller-* personas, managed agent defaults, and generated Codex skills when they have not been locally edited. It works even from inside a gated fable session (the hook explicitly allows it). Your run history (`.tiller/` dirs in your projects) is never touched. Use `tiller uninstall --backend <name> --project --print` to preview exactly what would be removed before committing.

`tiller install` with no flags is intentionally project-local. It asks which agent harness config to install, then writes only under the current directory's agent config (`.codex/` or `.claude/`). Use explicit `--backend ... --global` only when you want user-scope install.

For Codex, the project install writes `PreToolUse`, `SessionStart`, and `SubagentStart` hooks to `.codex/hooks.json`, the eight Codex custom agent personas to `.codex/agents/`, `.codex/config.toml` defaults of `features.multi_agent = true`, `[agents] max_threads = 12`, and `max_depth = 2`, plus project operating notes in `AGENTS.md` and Codex skills at `.codex/skills/using-tiller/SKILL.md` and `.codex/skills/using-sirena/SKILL.md`. Run `tiller codex doctor` from the project root after install to verify hooks, config, agents, skills, bypass state, and Codex hook smoke checks.

For OpenCode, the project install writes `opencode.json` with a managed instruction reference, `.opencode/tiller.md` operating notes, and OpenCode markdown agents under `.opencode/agents/`. Use the `tiller-orchestrator` primary agent for the root orchestration path and the `tiller-*` subagents for execution, debugging, investigation, review, and synthesis.

`tiller ambient disable` creates `.tiller/ambient.disabled` in the current project. While that marker exists, ambient hooks pass through silently. `tiller ambient enable` removes it. `TILLER_AMBIENT_DISABLED=1` is also honored as a process-local bypass.

For Claude Code, then in any `claude` session:

```
/model fable
```

Ambient mode engages immediately. The fable root is restricted to orchestration-only tools - with targeted carve-outs described below. Execution is automatically delegated to tiller-* subagents on cheaper models.

For Codex, open or restart a Codex session in the installed project so Codex loads the project `.codex/` config, hooks, and custom agents. Codex remains silent at startup unless `SessionStart` can prove the root session maps to a governed tier, such as `gpt-5.5 xhigh`, from the hook payload or transcript. When that proof exists, `SessionStart` adds the Tiller operating context up front. `SubagentStart` adds role-specific context for each `tiller-*` agent. For OpenCode, open or restart OpenCode in the installed project and switch to the `tiller-orchestrator` primary agent.

**How it works.** The installed `PreToolUse` hook reads the session transcript to find the model of the most recent assistant turn. The transcript is read backward from EOF so detection costs ~0.5 ms even on 50 MB files. If that model maps to a governed tier in `models.toml` (`reason` by default), the hook evaluates `ambient.arb` - the orchestrator-only policy. For any other model the hook exits 0 immediately. Subagent calls (where `agent_id` is present) pass through unconditionally.

**Fail-open.** Any error reading the transcript (missing path, no assistant line, unreadable file) causes the hook to exit 0. It never blocks a non-reason-tier session. Model switches (e.g. `/model sonnet` to `/model fable`) are tracked live via the transcript.

**What the ambient orchestrator can do.** The ambient policy is not fully read-only. The following carve-outs apply to the root reason-tier session (ground truth: `internal/policy/defaults/ambient.arb`):

| Carve-out rule | What is permitted |
|---|---|
| `AllowReadOnlyBash` | Read-only Bash: `git log`, `ls`, `cat`, `gts *`, `hypha recall` (including `\| head` pipelines via `2>&1`), and equivalent read commands. Unquoted `>`, `>>`, `` ` ``, `$(` are denied. |
| `AllowMarkdownAuthoring` | `Write`/`Edit` on `*.md` paths - specs, plans, prompts, directives, briefs, code-in-docs. Code files, notebooks, no-extension paths are denied. |
| `AllowOrchestrationTools` | `ToolSearch`, `Skill`, `AskUserQuestion`, `EnterPlanMode`/`ExitPlanMode`, `TaskCreate`/`TaskGet`/`TaskList`/`TaskUpdate`/`TaskOutput`/`TaskStop`, and Codex multi-agent tools such as `spawn_agent`, `send_input`, `resume_agent`, `wait_agent`, and `close_agent`. |
| `AllowPermittedBash` | Also allows constrained `codex exec` delegation when the command pins `gpt-5.5`, sets `model_reasoning_effort` to `xhigh`, `high`, or `medium`, avoids dangerous sandbox bypasses, and writes optional reports only under `.tiller/`. `xhigh` requires `--sandbox read-only`. |

Everything else (mutations to code files, running commands, launching daemons) stays denied. `DenyHyphaDaemons` explicitly blocks `hypha mcp serve` and `hypha hub serve` regardless of classifier output.

**Subagent model guards.** Two rules protect against silent reason-tier budget leak in `Task`/`Agent` dispatches:

- `DenyReasonModelSubagent` - blocks any `Task`/`Agent` call that carries an explicit reason-tier model override for an execution persona. Reason-tier overrides are reserved for `tiller-architect`, `tiller-deep-report`, `tiller-investigator`, and `tiller-reviewer`.
- `DenyImplicitReasonInheritance` - blocks generic subagent types (`general-purpose`, `claude`, `Explore`, `Plan`, or blank) with no explicit model field; in a reason-tier ambient session these silently inherit the parent model. Either name a cheaper model explicitly or use a named `tiller-*` persona whose frontmatter pins the model.

## Upgrading

tiller hooks exec the binary fresh on every tool call and the ambient policy is embedded in the binary, so upgrading is a single command:

```sh
go install m31labs.dev/tiller/cmd/tiller@latest
```

All running ambient sessions pick up the new binary and policy on their next tool call - **no session restart needed** for policy changes.

Exceptions that do require a session restart:
- Hook re-registration (if the hook entry format changes) - run `tiller install` again for the current project, or use explicit `--backend ... --project`.
- Persona file changes - run install again to redeploy `~/.claude/agents/tiller-*.md` or `.codex/agents/tiller-*.toml`.

The executor pool (`tiller pool`) must be restarted to pick up a new binary.

## Canonical Subagent Personas

When the reason-tier root delegates via the Agent/Task tool, these personas route work to cheaper models:

| Persona | Model | Tier | Use for |
|---|---|---|---|
| `tiller-summary` | haiku | execute | Status compaction, run ledger summaries, stale/late report triage, checkpoint candidate synthesis |
| `tiller-worker` | sonnet | execute | Writing/editing code, running builds and tests, all file-mutating work |
| `tiller-debugger` | sonnet | execute | Systematic debugging - root-cause, fix, verify |
| `tiller-investigator` | opus | scrutiny | Deep read-only investigation, code tracing, adversarial verification |
| `tiller-reviewer` | opus | scrutiny | Code review - correctness, security, quality |
| `tiller-architect` | fable | reason | Architectural specs, deep design, complex trade-off analysis |
| `tiller-deep-report` | fable | reason | Exhaustive multi-source research reports |

Claude Code deny reasons mention the Task tool. Codex deny reasons are Codex-native: the root can read/search directly, execution or mutation should use `spawn_agent` with the right `agent_type`, then `wait_agent`/`close_agent`.

**Enforcement layering.** Ambient mode governs the root reason-tier session only. Subagents spawned via Task are unaffected by ambient policy; they pass through. Their model is baked into the persona frontmatter (`model: sonnet`/`opus`/`fable`), which is the primary cost lever in ambient mode.

## Codex Operating Profile

Codex has the same shape: a root interactive thread, lifecycle hooks, custom agents, model plus reasoning-effort settings, managed configuration, and discoverable project instructions. The project installer generates Codex agents in `.codex/agents/`, hooks in `.codex/hooks.json`, `AGENTS.md` instructions, plus `using-tiller` and `using-sirena` skills so Codex can use the technique immediately when `tiller` is on PATH:

| Agent | Codex model settings | Tier | Use for |
|---|---|---|---|
| `tiller-scout` | `gpt-5.4-mini`, `model_reasoning_effort = "medium"`, read-only | scout | Cheap bounded reconnaissance, inventories, docs/log snippets, simple summaries |
| `tiller-summary` | `gpt-5.4-mini`, `model_reasoning_effort = "medium"`, read-only | scrutiny | Compact status updates, run ledger summaries, stale/late report triage, checkpoint candidate synthesis |
| `tiller-worker` | `gpt-5.5`, `model_reasoning_effort = "medium"` | execute | Bounded implementation, edits, builds, tests |
| `tiller-debugger` | `gpt-5.5`, `model_reasoning_effort = "high"` | execute | Root-cause analysis, fixes, verification |
| `tiller-investigator` | `gpt-5.5`, `model_reasoning_effort = "xhigh"`, read-only | reason | Deep investigation and adversarial verification |
| `tiller-reviewer` | `gpt-5.5`, `model_reasoning_effort = "xhigh"`, read-only | reason | Code review, correctness, security, missing tests |
| `tiller-architect` | `gpt-5.5`, `model_reasoning_effort = "xhigh"` | reason | Architecture, design, trade-off analysis |
| `tiller-deep-report` | `gpt-5.5`, `model_reasoning_effort = "xhigh"` | reason | Multi-source research and synthesis |

The Codex operating rule is to right-size the agent before spending reasoning budget: root reads/searches directly and makes routing decisions; `tiller-scout` and `tiller-summary` use `gpt-5.4-mini` for cheap reconnaissance and status compaction; `tiller-worker` uses `gpt-5.5 medium`; `tiller-debugger` uses `gpt-5.5 high`; investigator/reviewer/architect/deep-report use `gpt-5.5 xhigh` for high-stakes reasoning, review, and synthesis. `tiller hook --backend codex` reads Codex `turn_context` transcript lines, normalizes `model + effort` into aliases such as `gpt-5.5 xhigh`, and applies ambient policy only for governed tiers. For Codex `PreToolUse`, allow decisions are silent and deny decisions use Codex hook output with `spawn_agent`/`wait_agent`/`close_agent` guidance.

Codex `SessionStart` returns `additionalContext` before any denial is needed only when the session is proven governed. If ambient is active, it reminds the root that reads/searches are direct, cheap reconnaissance can go to `tiller-scout`, status compaction can go to `tiller-summary`, and execution routes through `spawn_agent`. If `.tiller/ambient.disabled` or `TILLER_AMBIENT_DISABLED=1` disables ambient, that governed startup context says normal tools are allowed. Without governed/xhigh proof, `SessionStart` exits silently so Codex ambient activation stays invisible to non-governed sessions. Codex `SubagentStart` returns role-specific `additionalContext` for scout, summary, worker/debugger, investigator/reviewer, architect/deep-report, and unknown `tiller-*` agent types; it never blocks startup.

Ambient run directories include a generated `status.md` snapshot beside `ledger.jsonl` when lifecycle, descriptor, or usage ledger events are observed. It is derived from `manifest.json`, dispatch metadata, agent lifecycle records, checkpoint candidates, and the ledger. Governed root `Task`/`Agent` and Codex `spawn_agent` dispatch requests are captured as `ambient.task_descriptor` ledger events and rendered into a `## Task Descriptors` section so `tiller-summary` can read compact run state before opening raw records. Optional advisory spend bands are configured with `TILLER_AMBIENT_OUTPUT_TOKEN_BUDGET`, `TILLER_AMBIENT_REASONING_TOKEN_BUDGET`, and `TILLER_AMBIENT_BUDGET_WARN_RATIO`; they are visibility-only and do not deny hooks.

Use `.tiller/scratch/codex/` as the shared ambient Codex scratch path for terse handoff notes, reports, and claims. Root may write notes there for subagents; subagents should read relevant notes first when present and write final reports or handoff notes there when useful.

Checkpointing is part of the workflow for ambient installs. At natural verified boundaries, agents should surface a checkpoint with exact changed files, verification, and caveats. Prefer the repo's configured checkpoint tool when one is present; otherwise use normal Git/GitHub. Stage explicit paths, inspect the diff, and never commit unrelated dirty-worktree changes.

The root Codex orchestrator should read directly: file reads, searches, and
safe read-only shell inspection do not require a subagent. Delegate when the
work becomes mutation, build/test execution, debugging, deep investigation, or
review.

Codex orchestrator artifacts should be terse, direct, and explicit: concrete
paths, commands, diagnostics, decisions, and next actions over broad prose.

From a gated Claude/Fable ambient root, Codex can also be used as a CLI delegation target:

```sh
codex exec -m gpt-5.5 -c model_reasoning_effort=medium "make a bounded edit and report back"
codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh --sandbox read-only \
  --output-last-message .tiller/reports/review.md "review the current diff"
```

This is intentionally a narrow test bridge. A generalized harness adapter can provide a broader provider/runtime abstraction later.

## Tiers & Model Routing

tiller speaks three tiers throughout - policies route on them, audit logs record them, and the persona table maps to them:

| Tier | Role in the system | Default candidate |
|---|---|---|
| `reason` | Orchestration, planning, deep analysis | `claude-headless:anthropic/fable` |
| `scrutiny` | Read-only investigation, review | `claude-headless:anthropic/opus` |
| `execute` | Implementation, file mutation, command execution | `claude-headless:anthropic/sonnet` (fallback: haiku) |

These defaults live in `internal/tier/defaults/models.toml`. Override them for a project by creating `.tiller/models.toml`:

```toml
[tiers.reason]
candidates = ["claude-headless:anthropic/fable"]

[tiers.scrutiny]
candidates = ["claude-headless:anthropic/opus"]

[tiers.execute]
candidates = ["claude-headless:anthropic/sonnet", "claude-headless:anthropic/haiku"]

[ambient.claude-code]
detector = "claude-jsonl-transcript"
govern_tiers = ["reason"]
reason_models = ["fable", "claude-fable-5"]
scrutiny_models = ["opus", "claude-opus-4-8"]
execute_models = ["sonnet", "claude-sonnet-4-5", "haiku", "claude-haiku-4-5"]

[ambient.codex]
detector = "codex-jsonl-transcript"
govern_tiers = ["reason"]
reason_models = ["5.5 xhigh", "gpt-5.5 xhigh"]
execute_models = ["5.5 high", "5.5 medium", "5.5 low", "gpt-5.5 high", "gpt-5.5 medium", "gpt-5.5 low"]
```

Each candidate is `adapter:provider/model`. The first candidate that resolves wins; haiku serves as a canary or fallback for the execute tier.

Command (non-Claude) backends use the `command` adapter - see [Non-Claude Backends](#non-claude-backends) below.

`[ambient.<backend>]` sections are for interactive ambient sessions. They map backend-specific model strings to tiller tier labels before policy evaluation. Claude Code reads root assistant model IDs from its JSONL transcript. Codex reads the hook payload plus transcript `turn_context` and normalizes `gpt-5.5` with `xhigh` into the alias `gpt-5.5 xhigh` (or the shorthand `5.5 xhigh`) before policy evaluation.

For Claude Code installs, `tiller install` renders the `model:` frontmatter in the `tiller-*` personas from `[ambient.claude-code]`: worker/debugger use the execute alias, architect/deep-report use the reason alias, and investigator/reviewer use the scrutiny alias when present or the reason alias otherwise. This keeps persona routing configurable without editing embedded markdown templates.

For Codex, `tiller install --backend codex --project` writes the equivalent `.codex/agents/*.toml` files, `.codex/hooks.json`, `.codex/config.toml`, `AGENTS.md` operating notes, `.codex/skills/using-tiller/SKILL.md`, and `.codex/skills/using-sirena/SKILL.md` with subagent limits of 12 concurrent threads and max depth 2.

## Managed Mode: `tiller run` (optional)

`tiller run` is the heavyweight alternative: it spawns agents as separate `claude -p` processes, enforces dispatch depth, writes full audit trails, and creates a run artifact tree. Use it when you need replayable audits, process-tree isolation, or the full dispatch -> report -> promote workflow.

```sh
# Initialize a project (materializes .tiller/policy/*.arb and roles/*.md)
cd your-project
tiller init

# Run a task
tiller run "investigate why the payment retry queue is backing up and write a findings report"

# Flags
tiller run --reason-budget 3 --max-depth 2 "my task"
tiller run --store tee --store-dsn postgres://user:pass@host/db "my task"

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
 -- tiller (root CLI)
     -- creates .tiller/runs/<run-id>/
     -- spawns orchestrator: claude -p <task> --model fable \
     |          --settings <generated> --permission-mode dontAsk \
     |          --append-system-prompt roles/orchestrator.md
     |  (env: TILLER_ROLE=orchestrator, TILLER_DEPTH=0,
     |        TILLER_RUN_DIR, TILLER_DISPATCH_ID=root)
     |
     |  every tool call --> PreToolUse hook: tiller hook
     |                       -- toolgate.arb -> allow/deny + audit
     |
     |  orchestrator runs: Bash(tiller dispatch --role investigator ...)
     |    -- tiller dispatch (child CLI invocation in the claude process)
     |        -- builds DispatchRequest -> dispatch.arb (rules gate + strategy route)
     |        -- DENIED -> exit 3, policy reason on stderr (orchestrator re-plans)
     |        -- ALLOWED -> writes dispatches/<id>/{brief.md,settings.json,meta.json}
     |            -- spawns detached tiller _supervise <run> <id>
     |                -- execs claude -p --model <tier-resolved model> --output-format json
     |                    captures report.md, finalizes meta.json
     |
     -- depth-1 agents can dispatch further; depth-2 agents are terminal
        (dispatch.arb DenyTerminalDepth, toolgate DenyTerminalDispatch,
         generated settings replace Bash(tiller *) with Bash(tiller note *))
```

## Ambient vs Managed Mode

| | Ambient | Managed (`tiller run`) |
|---|---|---|
| Setup | `tiller install` once | `tiller init` per project |
| Session | Normal interactive `claude` | Spawned `claude -p` processes |
| Enforcement | Root reason-tier session only | Full process tree, every agent |
| Artifacts | None | Full run dir, JSONL audit trails |
| Depth | Flat (subagents cannot spawn subagents) | Up to configurable depth (`--max-depth`) |
| Model routing | Persona frontmatter | `dispatch.arb` strategy rules + `models.toml` |

## Managed Mode: Role x Tier Matrix

| Role | Tier | Profile | Edit/Write | Bash | May dispatch |
|---|---|---|---|---|---|
| `orchestrator` | reason | orchestrator | denied | `tiller *`, `hypha *` only | all roles |
| `chief-architect` | reason | insight | scratch only | read-only prefixes | investigator |
| `deep-report` | reason | insight | scratch only | read-only prefixes | investigator |
| `investigator` | scrutiny | readonly | denied | read-only prefixes | investigator |
| `worker` | execute | execution | yes (workspace) | yes | investigator, debugger |
| `debugger` | execute | execution | yes (workspace) | yes | investigator |
| `reviewer` | scrutiny | readonly | scratch only | read-only prefixes | none |

Reason-tier dispatches (`chief-architect`, `deep-report`) are limited per run (default 2, controlled by `--reason-budget`). The reason budget is a hard policy rule in `dispatch.arb`.

## Queued Dispatch & the Executor Pool

For workloads where dispatches should not block the caller, tiller supports a queue + pool execution model:

```sh
# Queue a dispatch (returns dispatch id immediately, no spawn)
tiller dispatch --queue --role worker --tier execute --brief "do the thing"
# -> prints dispatch id (e.g. d01) and exits 0

# Run the executor pool (host-managed singleton)
tiller pool

# Pool flags
tiller pool --poll 5s --max-concurrent 4 --lease 2m --renew 1m
tiller pool --store pg --store-dsn postgres://... --journal /var/lib/tiller/pool.jsonl

# Watch a specific dispatch
tiller poll <dispatch-id>
tiller await <dispatch-id>
```

**How the pool works.** `tiller pool` is a host-managed singleton process that continuously sweeps the store for `pending` dispatches, claims them with a time-bounded lease, spawns the appropriate adapter, and marks them `completed` or `failed`. Key properties:

- **Claim/lease semantics.** A dispatch is claimed atomically; the pool renews the lease on a fixed interval. If the pool crashes mid-execution the lease expires and another pool process (or a restarted pool) can reclaim it.
- **Journal exactly-once.** A JSONL delivery journal (`--journal`, default `.tiller/pool-journal.jsonl`) records every completed dispatch ID. On restart the pool skips already-journaled IDs, preventing double-execution.
- **Denied status.** A dispatch that fails policy evaluation is written as `status: denied` with the policy reason; the pool does not retry denied dispatches.
- **Concurrency.** `--max-concurrent` limits simultaneous adapter runs. Default is 4.

## Non-Claude Backends

tiller's adapter seam is provider-agnostic. Any process can serve as an execute-tier backend via the `command` adapter. Configure it in `.tiller/models.toml`:

```toml
[tiers.execute]
candidates = ["command:my-agent/-"]

[adapter.my-agent]
argv    = ["/path/to/my-agent", "{brief}"]
report  = "stdout"
timeout = "30s"
```

The `{brief}` placeholder is replaced with the path to the dispatch's `brief.md`. The adapter captures stdout as `report.md`.

**Enforcement note.** When the execute tier resolves to the `command` adapter, enforcement is `degraded`: only execute-tier dispatches are permitted (the `DenyDegradedInsight` policy rule blocks reason/scrutiny roles through a command backend). Policy is still evaluated and audit events are still written; the backend just cannot enforce toolgate rules that depend on Claude Code's hook mechanism.

A full end-to-end demo with an echo-agent backend is in [demo/DEMO-v2.md](demo/DEMO-v2.md).

## Enforcement Layering

From weakest to strongest, each layer independent:

```
role .md (cooperative)
  < settings deny (removes tools from context)
    < PreToolUse hook + toolgate.arb (re-checks every call, flock-serialized audit JSONL)
      < dispatch.arb (gates every dispatch request with identity from env)
        < [future] Horizon LSM exec-deny profile (kernel-level, see section Appendix)
```

**Ambient mode** adds one enforcement point at the top of the root reason-tier session. It governs only the root; subagents pass through. **Managed mode** applies toolgate at every level.

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
  manifest.json                      # task, workspace, policy sha256s, status, reason_budget
  task.md                            # root task brief
  audit/
    dispatch.jsonl                   # DecisionEvents for all dispatch requests (replayable)
    toolgate.jsonl                   # DecisionEvents for all tool calls (replayable)
  notes/                             # shared scratch
  dispatches/
    root/                            # orchestrator
    d01/, d02/, .../
      brief.md  report.md  settings.json  meta.json
      tool_trace.jsonl  context_trace.jsonl  transcript.json  supervise.log
```

```sh
tiller runs list
tiller runs show <run-id>
tiller runs gc --keep 20 [--dry-run]
```

## Scratch Space & Storage

tiller is filesystem-first. By default it writes every run artifact to `.tiller/runs/<id>/` and requires no database, daemon, account, bucket, or network service. The run directory is the scratch space: briefs, reports, per-dispatch metadata, hook settings, audit JSONL, tool traces, context traces, and notes all live there in a layout that can be inspected with normal shell tools and replayed by Arbiter.

That local layout is also the enforcement anchor. Hooks verify identity and evaluate toolgate policy from the local run tree; they do not dial a database or object store. Remote or queryable backends should mirror the scratch bus, not replace the hot-path files.

The current store implementations are:

| Backend | Flag/Env | Description |
|---------|----------|-------------|
| `fs` (default) | `--store fs` | Writes to `.tiller/runs/<id>/` on disk. No DSN required. |
| `pg` | `--store pg` | Writes to PostgreSQL. Requires `--store-dsn` / `TILLER_STORE_DSN`. |
| `tee` | `--store tee` | Writes to fs synchronously and mirrors to pg asynchronously. **Rollout mode.** |

PostgreSQL is optional. It is useful when you need a shared multi-host scratch bus, central SQL queries, or long-lived infrastructure integration. Most users should start with `fs`.

**Selecting a backend:**

```sh
# Explicit flag (highest priority)
tiller run --store tee --store-dsn postgres://user:pass@host/db "my task"

# Environment variables (inherited by all child dispatches)
TILLER_STORE=tee TILLER_STORE_DSN=postgres://... tiller run "my task"
```

Resolution order: `--store` flag -> `TILLER_STORE` env -> default `fs`.

**Tee rollout semantics.** In `tee` mode, fs is authoritative:
- Every write goes to fs first, synchronously. Error semantics are identical to `fs` alone.
- pg mirror writes are async (bounded queue, single goroutine). A mirror failure logs and drops; it never slows or fails the caller.
- All reads come from fs. If fs and pg diverge, fs wins.
- `Close` (called at end of `tiller run`) drains the mirror queue before returning.

**Hot-path guard.** When `TILLER_RUN_DIR` is set (hook and child dispatch invocations), the store is always opened as `fs` regardless of `TILLER_STORE`/`TILLER_STORE_DSN`. The toolgate evaluates policy locally and must never touch the network.

**Pluggable store shape.** All backends implement `scratch.Store`: run lifecycle, dispatch allocation, brief/report documents, notes, adapter config, trace append, audit sinks, materialization, rendered trees, summaries, and pool lease operations. That interface is deliberately compatible with local mirrors and remote stores:

- SQLite is a natural next backend for local queryable logs: a single `.tiller/tiller.sqlite` mirror with no server, while fs remains the enforcement authority.
- S3 or object storage is a natural artifact mirror: run records as keys, JSONL append streams as objects, and local materialization before adapter execution.
- PostgreSQL remains the shared relational backend for multi-host pools and centralized dashboards.

**DSN.** The DSN is a standard PostgreSQL connection string:

```
postgres://user:password@host:5432/dbname?sslmode=disable
```

**Exporting a pg-stored run.** After a run with `--store pg` or `--store tee`, materialise the v1 file layout so that `arbiter replay` and other file-based tools work verbatim:

```sh
tiller runs export <run-id> --dir /tmp/myrun \
  --store pg --store-dsn postgres://...

# Then replay the audit log
arbiter replay .tiller/policy/toolgate.arb \
  --audit /tmp/myrun/audit/toolgate.jsonl
```

`tiller runs export` is idempotent: re-running into the same `--dir` overwrites files in place.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `TILLER_CLAUDE_BIN` | `claude` | Path to the `claude` CLI binary |
| `TILLER_ROLE` | - | Agent role; set by tiller at spawn |
| `TILLER_DEPTH` | `0` | Spawn depth; set by tiller at spawn |
| `TILLER_RUN_DIR` | - | Absolute path to the run scratch directory |
| `TILLER_DISPATCH_ID` | - | Dispatch id of the current agent |
| `TILLER_STORE` | `fs` | Storage backend: `fs`\|`pg`\|`tee` |
| `TILLER_STORE_DSN` | - | PostgreSQL DSN (required for `pg` and `tee` backends) |

## Hyphae Integration

tiller integrates with [hyphae](https://github.com/M31-Labs/hyphae) for live observability and knowledge promotion. If `hypha` is not on PATH, all calls degrade to logged skips.

- **Traces**: `tiller run` opens a hypha trace; every dispatch emits a tick.
- **Recall**: role prompts instruct agents to `hypha recall <q>` before non-trivial work.
- **Promotion**: `tiller promote <run-id>` composes a spore from the run's task, dispatch tree, and report excerpts.

## Policy Schemas

Input structs are in `internal/policy/schemas.go`. `dispatch.arb` input: `DispatchRequest`. `toolgate.arb` input: `ToolCallRequest`. Renaming an `arb`-tagged field breaks policy load immediately.

## Horizon Backstop (Deferred)

Intended kernel-level backstop: a Horizon-compiled LSM exec-deny profile. Requires `CONFIG_BPF_LSM` (not in stock WSL2), Horizon DSL v0.3+ helpers, and `CAP_BPF`. Until prerequisites are met, the enforcement ceiling is settings + hook + policy.

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for version history.

## License

MIT
