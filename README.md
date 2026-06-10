# tiller

A harness that gates Claude Code sessions with compiled [Arbiter](https://github.com/odvcencio/arbiter) policies, enforces tier-cost discipline, and makes the reason-tier model's orchestrator-only role the default ambient experience.

## Why

Reason-tier tokens are expensive. When you have reason-tier access, the natural waste pattern is using it for mechanical execution — writing files, running commands, editing boilerplate. tiller prevents this structurally: the reason-tier model orchestrates and reasons; cheaper tiers (scrutiny, execute) do the work. In ambient mode this happens automatically inside your normal `claude` session without any project setup.

## Quickstart: Ambient Mode (recommended)

Ambient mode is the primary way to use tiller. It self-activates whenever your Claude Code session is running a reason-tier model and is completely invisible for every other model.

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

**Trialing is safe.** `tiller uninstall` reverts everything tiller installed — hooks and tiller-* personas — in one shot. It works even from inside a gated fable session (the hook explicitly allows it). Your run history (`.tiller/` dirs in your projects) is never touched. Use `tiller uninstall --print` to preview exactly what would be removed before committing.

`tiller install` does two things:
1. Merges `PreToolUse` and `PostToolUse` hook entries into `~/.claude/settings.json`
2. Writes the six tiller-* subagent persona files into `~/.claude/agents/`

Then, in any `claude` session:

```
/model fable
```

Ambient mode engages immediately. The fable root is restricted to orchestration-only tools — with targeted carve-outs described below. Execution is automatically delegated to tiller-* subagents on cheaper models.

**How it works.** The installed `PreToolUse` hook reads the session transcript to find the model of the most recent assistant turn. The transcript is read backward from EOF so detection costs ~0.5 ms even on 50 MB files. If that model maps to the reason tier (`claude-fable-5` or `fable`), the hook evaluates `ambient.arb` — the orchestrator-only policy. For any other model the hook exits 0 immediately. Subagent calls (where `agent_id` is present) pass through unconditionally.

**Fail-open.** Any error reading the transcript (missing path, no assistant line, unreadable file) causes the hook to exit 0. It never blocks a non-reason-tier session. Model switches (e.g. `/model sonnet` → `/model fable`) are tracked live via the transcript.

**What the ambient orchestrator can do.** The ambient policy is not fully read-only. The following carve-outs apply to the root reason-tier session (ground truth: `internal/policy/defaults/ambient.arb`):

| Carve-out rule | What is permitted |
|---|---|
| `AllowReadOnlyBash` | Read-only Bash: `git log`, `ls`, `cat`, `gts *`, `hypha recall` (including `\| head` pipelines via `2>&1`), and equivalent read commands. Unquoted `>`, `>>`, `` ` ``, `$(` → denied. |
| `AllowMarkdownAuthoring` | `Write`/`Edit` on `*.md` paths — specs, plans, prompts, directives, briefs, code-in-docs. Code files, notebooks, no-extension paths → denied. |
| `AllowOrchestrationTools` | `ToolSearch`, `Skill`, `AskUserQuestion`, `EnterPlanMode`/`ExitPlanMode`, and `TaskCreate`/`TaskGet`/`TaskList`/`TaskUpdate`/`TaskOutput`/`TaskStop`. |

Everything else (mutations to code files, running commands, launching daemons) stays denied. `DenyHyphaDaemons` explicitly blocks `hypha mcp serve` and `hypha hub serve` regardless of classifier output.

**Subagent model guards.** Two rules protect against silent reason-tier budget leak in `Task`/`Agent` dispatches:

- `DenyReasonModelSubagent` — blocks any `Task`/`Agent` call that carries an explicit reason-tier model override for a persona that is not `tiller-architect` or `tiller-deep-report`. Pass a cheaper model or pick the right persona.
- `DenyImplicitReasonInheritance` — blocks generic subagent types (`general-purpose`, `claude`, `Explore`, `Plan`, or blank) with no explicit model field; in a reason-tier ambient session these silently inherit the parent model. Either name a cheaper model explicitly or use a named `tiller-*` persona whose frontmatter pins the model.

## Upgrading

tiller hooks exec the binary fresh on every tool call and the ambient policy is embedded in the binary, so upgrading is a single command:

```sh
go install m31labs.dev/tiller/cmd/tiller@latest
```

All running ambient sessions pick up the new binary and policy on their next tool call — **no session restart needed** for policy changes.

Exceptions that do require a session restart:
- Hook re-registration (if the hook entry format changes) — run `tiller install` again.
- Persona file changes — run `tiller install` again to redeploy `~/.claude/agents/tiller-*.md`.

The executor pool (`tiller pool`) must be restarted to pick up a new binary.

## Canonical Subagent Personas

When the reason-tier root delegates via the Agent/Task tool, these personas route work to cheaper models:

| Persona | Model | Tier | Use for |
|---|---|---|---|
| `tiller-worker` | sonnet | execute | Writing/editing code, running builds and tests, all file-mutating work |
| `tiller-debugger` | sonnet | execute | Systematic debugging — root-cause, fix, verify |
| `tiller-investigator` | opus | scrutiny | Deep read-only investigation, code tracing, adversarial verification |
| `tiller-reviewer` | opus | scrutiny | Code review — correctness, security, quality |
| `tiller-architect` | fable | reason | Architectural specs, deep design, complex trade-off analysis |
| `tiller-deep-report` | fable | reason | Exhaustive multi-source research reports |

The deny reason when a reason-tier model tries to execute directly: `"tiller: ambient orchestrator runs in read/dispatch mode — delegate with the Task tool: code changes → tiller-worker, debugging → tiller-debugger, investigation → tiller-investigator, review → tiller-reviewer; reserve tiller-architect/tiller-deep-report for deep design and research. {tool.name} is not permitted for the root orchestrator agent."` — this steers the orchestrator toward the right persona without a second prompt.

**Enforcement layering.** Ambient mode governs the root reason-tier session only. Subagents spawned via Task are unaffected by ambient policy — they pass through. Their model is baked into the persona frontmatter (`model: sonnet`/`opus`/`fable`), which is the primary cost lever in ambient mode.

## Tiers & Model Routing

tiller speaks three tiers throughout — policies route on them, audit logs record them, and the persona table maps to them:

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
```

Each candidate is `adapter:provider/model`. The first candidate that resolves wins; haiku serves as a canary or fallback for the execute tier.

Command (non-Claude) backends use the `command` adapter — see [Non-Claude Backends](#non-claude-backends) below.

## Managed Mode: `tiller run` (optional)

`tiller run` is the heavyweight alternative: it spawns agents as separate `claude -p` processes, enforces dispatch depth, writes full audit trails, and creates a run artifact tree. Use it when you need replayable audits, process-tree isolation, or the full dispatch → report → promote workflow.

```sh
# Initialize a project (materializes .tiller/policy/*.arb and roles/*.md)
cd your-project
tiller init

# Run a task
tiller run "investigate why the payment retry queue is backing up and write a findings report"

# Flags
tiller run --reason-budget 3 --max-depth 3 "my task"
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
     │                └─ execs claude -p --model <tier-resolved model> --output-format json
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
| Enforcement | Root reason-tier session only | Full process tree, every agent |
| Artifacts | None | Full run dir, JSONL audit trails |
| Depth | Flat (subagents cannot spawn subagents) | Up to configurable depth (`--max-depth`) |
| Model routing | Persona frontmatter | `dispatch.arb` strategy rules + `models.toml` |

## Managed Mode: Role × Tier Matrix

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
# → prints dispatch id (e.g. d01) and exits 0

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
        < [future] Horizon LSM exec-deny profile (kernel-level, see §Appendix)
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
    d01/, d02/, …/
      brief.md  report.md  settings.json  meta.json
      tool_trace.jsonl  context_trace.jsonl  transcript.json  supervise.log
```

```sh
tiller runs list
tiller runs show <run-id>
tiller runs gc --keep 20 [--dry-run]
```

## Storage Backends

tiller writes run artifacts (manifest, dispatches, audit logs) to a configurable storage backend. Three backends are available:

| Backend | Flag/Env | Description |
|---------|----------|-------------|
| `fs` (default) | `--store fs` | Writes to `.tiller/runs/<id>/` on disk. No DSN required. |
| `pg` | `--store pg` | Writes to PostgreSQL. Requires `--store-dsn` / `TILLER_STORE_DSN`. |
| `tee` | `--store tee` | Writes to both fs and pg. **Rollout mode.** |

**Selecting a backend:**

```sh
# Explicit flag (highest priority)
tiller run --store tee --store-dsn postgres://user:pass@host/db "my task"

# Environment variables (inherited by all child dispatches)
TILLER_STORE=tee TILLER_STORE_DSN=postgres://... tiller run "my task"
```

Resolution order: `--store` flag → `TILLER_STORE` env → default `fs`.

**Tee rollout semantics.** In `tee` mode, fs is authoritative:
- Every write goes to fs first, synchronously. Error semantics are identical to `fs` alone.
- pg mirror writes are async (bounded queue, single goroutine). A mirror failure logs and drops; it never slows or fails the caller.
- All reads come from fs. If fs and pg diverge, fs wins.
- `Close` (called at end of `tiller run`) drains the mirror queue before returning.

**Hot-path guard.** When `TILLER_RUN_DIR` is set (hook and child dispatch invocations), the store is always opened as `fs` regardless of `TILLER_STORE`/`TILLER_STORE_DSN`. The toolgate evaluates policy locally and must never touch the network.

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
| `TILLER_ROLE` | — | Agent role; set by tiller at spawn |
| `TILLER_DEPTH` | `0` | Spawn depth; set by tiller at spawn |
| `TILLER_RUN_DIR` | — | Absolute path to the run scratch directory |
| `TILLER_DISPATCH_ID` | — | Dispatch id of the current agent |
| `TILLER_STORE` | `fs` | Storage backend: `fs`\|`pg`\|`tee` |
| `TILLER_STORE_DSN` | — | PostgreSQL DSN (required for `pg` and `tee` backends) |

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
