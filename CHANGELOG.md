# Changelog

All notable changes to tiller are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
tiller uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- **Ambient status snapshot** - ambient ledger writes now refresh a generated
  `status.md` beside `ledger.jsonl`, summarizing manifest, dispatch, agent,
  checkpoint, token usage, and recent ledger state for cheap summary agents.
- **Ambient spend budget visibility** - `status.md` can show advisory output
  and reasoning token budget bands from `TILLER_AMBIENT_OUTPUT_TOKEN_BUDGET`,
  `TILLER_AMBIENT_REASONING_TOKEN_BUDGET`, and
  `TILLER_AMBIENT_BUDGET_WARN_RATIO` without changing hook allow/deny policy.
- **Ambient task descriptors** - governed root `Task`/`Agent` and Codex
  `spawn_agent` requests now append descriptor-shaped ledger events and render
  a `## Task Descriptors` section in `status.md`; repeated attempts keep a
  stable `descriptor_id` while adding distinct `attempt_id` refs.
- **Ambient stale/late triage** - `status.md` now renders a `## Stale/Late Work`
  section for late/stale/superseded agents and late/conflicting checkpoint
  candidates, with `none` when there is nothing to triage.
- **Ambient result reconciliation** - governed root `PostToolUse` results now
  append advisory `ambient.task_result` ledger facts, update best-effort agent
  runs/checkpoint candidates from descriptor-compatible reports, and refresh
  `status.md` without changing ambient allow/deny policy.
- **Ambient distillation ledger** - descriptor-compatible reports may include
  `Distillation`, `Distilled State`, or `Reusable Context`; governed
  `PostToolUse` reconciliation appends idempotent `ambient.distillation`
  ledger facts and `status.md` renders a `## Distillation` section for reusable
  compressed state before raw logs/transcripts. This is the foundation for the
  recursive ambient loop: distilled state -> next action/refinement ->
  checkpoint, continue, or escalate.
- **Arbiter ambient next action** - `status.md` now renders
  `## Arbiter Next Action` from the new `ambient_next_action` policy, emitting
  one advisory action with confidence, risk, reason, target, and budget
  posture while keeping `Recommended Next Actions` as legacy/fallback context.
- **Ambient next digest** - `tiller ambient next` now prints a compact
  run-control digest from scratch state, and `tiller ambient status` points to
  the active run's `status.md` and current Arbiter next action when available.
- **Ambient dry-run step descriptor** - `tiller ambient step --dry-run` now
  emits the Arbiter next action as a portable descriptor packet for
  orchestrators, without spawning, editing, committing, or mutating checkpoint
  state.
- **Ambient runtime doctor** - `tiller ambient doctor` verifies the live
  executable/version, source/binary drift, bypass state, ambient command
  classifier, and hook policy smoke behavior.
- **Ambient recommended next actions** - `status.md` now renders a deterministic
  `## Recommended Next Actions` section for stale triage, checkpoint review,
  compaction, waiting, or proceeding.
- **Checkpoint guidance** - ambient prompts now treat coherent verified slices
  as commit checkpoints. Agents report exact files, verification, and caveats;
  a repo-configured checkpoint tool is preferred when present, with plain
  Git/GitHub as the portable fallback.
- **OpenCode project install** - `tiller install --backend opencode --project`
  writes `opencode.json`, `.opencode/tiller.md`, and OpenCode markdown agents
  for a `tiller-orchestrator` primary plus the `tiller-*` subagent set.
- **Codex Spark summary routing** - Codex `tiller-summary` now uses
  `gpt-5.3-codex-spark` with high reasoning for fast read-only
  status/distillation/checkpoint/commit-prep work, while `tiller-scout`
  remains on `gpt-5.4-mini` for cheap bounded reconnaissance.

---

## [0.3.0] - 2026-06-11

### Added

- **Codex ambient mode** - `tiller hook --backend codex` reads Codex hook payloads and
  `turn_context` transcript lines, normalizes `model + effort` into tier aliases such as
  `gpt-5.5 xhigh`, and applies ambient policy for governed tiers.
- **Codex project installer** - `tiller install --backend codex --project` writes
  `.codex/hooks.json`, eight `tiller-*` Codex custom agents, `.codex/config.toml`,
  `AGENTS.md` operating notes, `.codex/skills/using-tiller/SKILL.md`, and
  `.codex/skills/using-sirena/SKILL.md`.
- **Codex scout persona** - `tiller-scout` uses `gpt-5.4-mini` for cheap bounded
  read-only reconnaissance, inventories, docs/log snippets, and simple summaries.
- **Ambient summary persona** - `tiller-summary` uses cheap read-only models for
  compact status updates, distilled ambient state, run ledger summaries,
  stale/late report triage, and checkpoint candidate synthesis across Claude
  Code, Codex, and OpenCode installs.
- **Codex lifecycle context** - Codex installs `SessionStart`, `SubagentStart`,
  and `PostToolUse` hooks alongside `PreToolUse`. `SessionStart` emits startup context only when
  the root session is proven governed, such as `gpt-5.5 xhigh`; subagent
  context is role-specific and non-blocking for `tiller-*` agents.
- **Sirena Codex skill** - Codex installs a `using-sirena` skill for `.sir`,
  `sirena.yaml`, Mermaid ingestion, mdpp Sirena fences, render, lint, format,
  bake, and emit workflows.
- **Project-local install wizard** - `tiller install` with no flags now prompts for
  Claude Code, Codex, OpenCode, or all, and installs into the current project instead of user scope.
- **Codex subagent limits** - the installer writes `features.multi_agent = true` plus
  `[agents] max_threads = 12` and `max_depth = 2` for the selected Codex config.
- **Backend-aware uninstall** - `tiller uninstall --backend codex --project` removes the
  matching project install, including managed Codex agent defaults when they still match.
- **Temporary ambient bypass** - `tiller ambient disable|enable|status` toggles a
  project-local `.tiller/ambient.disabled` marker, and `TILLER_AMBIENT_DISABLED=1` provides
  a process-local bypass while ambient mode is being tested.
- **Constrained Codex CLI delegation** - a governed Claude/Fable root may run pinned
  `codex exec` commands for `gpt-5.5` with `xhigh`, `high`, or `medium` effort. `xhigh`
  requires read-only sandboxing; optional report output is confined to `.tiller/`.
- **Codex-native deny guidance** - Codex `DenyExecution` output names
  `spawn_agent`, `agent_type`, `wait_agent`, and `close_agent` instead of Claude's
  Task tool.
- **Codex doctor** - `tiller codex doctor` verifies project-local Codex hooks,
  config defaults, embedded `tiller-*` agents, generated skills, bypass state,
  and internal hook smoke behavior.

### Changed

- **Managed max depth default is now 2** - root orchestrators run at depth 0, depth-1
  child agents can dispatch bounded follow-up work, and depth-2 agents are terminal.
- **Bump arbiter to v1.8.1** - picks up the segment-only rule syntax (`when segment NAME`) and the engine-wide empty-condition consistency fix. The evaluation workaround noted in v0.2.0 is no longer needed; segment-only rules in ambient policy now fire correctly on all eval paths.
- **Ambient orchestration tools include Codex lifecycle paths** - the root orchestrator can
  use normal Codex multi-agent tools to spawn, check in on, resume, wait for, and close
  subagents.
- **Codex orchestrator docs are more explicit** - generated Codex notes and skills now
  ask for right-sized agent routing and terse, direct technical artifacts: concrete paths,
  commands, diagnostics, decisions, and next actions over broad prose.
- **Generalized harness path remains open** - the Codex CLI bridge is intentionally
  narrow for testing; a broader provider/runtime abstraction can be added later.

---

## [0.2.0] - 2026-06-10

### Added

- **Self-uninstall escape hatch** - `tiller uninstall` (and `--print`/`--project` variants) is now
  explicitly allowed through the ambient gate for a gated fable orchestrator. The Go-side
  `IsSelfUninstall` predicate (quote-aware tokenizer, single-segment check, argv validation) sets
  command class `"self-uninstall"`. Chained forms (`tiller uninstall && rm x`) remain denied.

- **Hardened uninstall**:
  - Foreign hook preservation - `removeHookEntries` strips only entries whose command base-name is
    `tiller`; all other hooks survive intact.
  - Owned-persona-only removal - `ownedTillerAgentFiles` compares against the embedded `tiller-*.md`
    names (not a glob); user-created `tiller-custom.md` files are never touched.
  - Empty-container cleanup - `pruneEmptyHookContainers` removes empty `[]` arrays and the `"hooks"`
    map itself after tiller entries are removed, leaving no empty husks in `settings.json`.
  - Idempotency - second `tiller uninstall` on a clean system prints `"tiller: nothing to uninstall"`.
  - `--print` no-write - `tiller uninstall --print` prints the removal plan without modifying any file.
  - Trial-exit report - after real uninstall, prints what was removed (hook count, agent count,
    settings path), what remains on disk (binary, `.tiller/` run dirs), and how to finish cleanup.

- **"Trying tiller" README guidance** - Quickstart section notes that `tiller uninstall` reverts
  everything, works from inside a gated session, and `--print` previews before committing.

### Fixed / Changed

- **`AllowPermittedBash` rule consolidation** - the two separate inline-condition rules
  (`AllowReadOnlyBash` for `"readonly"` and a separate self-uninstall rule) were merged into a
  single OR condition. Root cause: arbiter v1.8.0 VM bug - two consecutive inline-condition rules
  before a segment-based rule cause the segment rule to not evaluate. Documented upstream.

---

## [0.1.1] - 2026-06-10

### Fixed

- **Cost parsing** (`tiller runs list/show` showed `$0.0000`): the Claude CLI >= 2.1.x changed its
  output to a JSON array with a `total_cost_usd` field at the top level. The parser now handles
  both the new array shape (`total_cost_usd`) and the legacy single-object shape (`cost_usd`).
  A real-world `claude-2.1.172` transcript is included as a test fixture.

- **Ambient command classifier** (read-only Bash carve-out was blocking real sessions):
  the classifier was splitting on whitespace and mis-flagging commands that contained
  alternation patterns (`grep 'foo|bar'`), quoted pipes, or quoted redirects as
  non-readonly. Replaced the naive splitter with a quote-aware state machine that
  correctly handles single/double-quoted strings and shell escapes.
  `"2>&1"` (the exact token) is permitted; unquoted `>`, `>>`, `` ` ``, and `$(` remain denied.

### Performance

- **Ambient model detection** - reads the session transcript backward from EOF instead of
  scanning the full file. Warm latency on a 50 MB transcript improved from ~48 ms to ~0.55 ms
  (87x). This path is paid on every tool call in ambient mode.

### Added

- **Ambient policy carve-outs** (all applied to the root reason-tier session only):
  - `AllowReadOnlyBash` - read-only Bash commands permitted: `git log`, `ls`, `cat`, `gts`,
    `hypha recall` (including pipelines through `2>&1 | head`), and equivalent read commands.
    Daemons (`hypha mcp serve`, `hypha hub serve`) are denied by `DenyHyphaDaemons` regardless.
  - `AllowMarkdownAuthoring` - `Write`/`Edit` on `*.md` paths permitted: specs, plans, prompts,
    directives, briefs, and code-in-docs. Code files, notebooks, and no-extension paths remain denied.
  - `AllowOrchestrationTools` - `ToolSearch`, `Skill`, `AskUserQuestion`, `EnterPlanMode`,
    `ExitPlanMode`, and the `TaskCreate`/`TaskGet`/`TaskList`/`TaskUpdate`/`TaskOutput`/`TaskStop`
    family are permitted for the ambient orchestrator.
  - `DenyReasonModelSubagent` (guard) - blocks `Task`/`Agent` calls that carry an explicit
    reason-tier model override for any persona other than `tiller-architect` or `tiller-deep-report`.
  - `DenyImplicitReasonInheritance` (guard) - blocks `Task`/`Agent` calls with a generic subagent
    type (`general-purpose`, `claude`, `Explore`, `Plan`, or blank) and no explicit model field;
    these silently inherit the ambient reason-tier model. Must name a cheaper model or a
    `tiller-*` persona.

- **`tiller --version` / `tiller -v`** - short version aliases alongside `tiller version`.

---

## [0.1.0] - 2026-06-10

Initial release.

- **Ambient mode** - orchestrator-only gating for reason-tier sessions via `PreToolUse`/`PostToolUse`
  hooks; self-activates on `claude-fable-5`/`fable`, transparent otherwise; fail-open on transcript errors.
- **Managed runs** - `tiller run` with full audit trails, arbiter-replayable JSONL, and `tiller promote` -> hyphae spore.
- **Tier routing** - `models.toml`: reason / scrutiny / execute; first-match candidate resolution.
- **Scratch store seam** - `fsstore` + postgres + tee rollout backend; `TILLER_RUN_DIR` hot-path guard.
- **Queued dispatch + executor pool** - claim/lease atomics, delivery journal (exactly-once), `tiller pool`.
- **Generic command adapter** - non-Claude backends, degraded enforcement, `DenyDegradedInsight`.
- **Hyphae integration** - traces, recall, `tiller promote`.
- **Canonical subagent personas** - six `tiller-*` agents embedded and installed by `tiller install`.

[0.3.0]: https://github.com/odvcencio/tiller/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/odvcencio/tiller/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/odvcencio/tiller/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/odvcencio/tiller/releases/tag/v0.1.0
