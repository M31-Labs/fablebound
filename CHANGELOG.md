# Changelog

All notable changes to tiller are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
tiller uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.2.0] — 2026-06-10

### Added

- **Self-uninstall escape hatch** — `tiller uninstall` (and `--print`/`--project` variants) is now
  explicitly allowed through the ambient gate for a gated fable orchestrator. The Go-side
  `IsSelfUninstall` predicate (quote-aware tokenizer, single-segment check, argv validation) sets
  command class `"self-uninstall"`. Chained forms (`tiller uninstall && rm x`) remain denied.

- **Hardened uninstall**:
  - Foreign hook preservation — `removeHookEntries` strips only entries whose command base-name is
    `tiller`; all other hooks survive intact.
  - Owned-persona-only removal — `ownedTillerAgentFiles` compares against the embedded `tiller-*.md`
    names (not a glob); user-created `tiller-custom.md` files are never touched.
  - Empty-container cleanup — `pruneEmptyHookContainers` removes empty `[]` arrays and the `"hooks"`
    map itself after tiller entries are removed, leaving no empty husks in `settings.json`.
  - Idempotency — second `tiller uninstall` on a clean system prints `"tiller: nothing to uninstall"`.
  - `--print` no-write — `tiller uninstall --print` prints the removal plan without modifying any file.
  - Trial-exit report — after real uninstall, prints what was removed (hook count, agent count,
    settings path), what remains on disk (binary, `.tiller/` run dirs), and how to finish cleanup.

- **"Trying tiller" README guidance** — Quickstart section notes that `tiller uninstall` reverts
  everything, works from inside a gated session, and `--print` previews before committing.

### Fixed / Changed

- **`AllowPermittedBash` rule consolidation** — the two separate inline-condition rules
  (`AllowReadOnlyBash` for `"readonly"` and a separate self-uninstall rule) were merged into a
  single OR condition. Root cause: arbiter v1.8.0 VM bug — two consecutive inline-condition rules
  before a segment-based rule cause the segment rule to not evaluate. Documented upstream.

---

## [0.1.1] — 2026-06-10

### Fixed

- **Cost parsing** (`tiller runs list/show` showed `$0.0000`): the Claude CLI ≥ 2.1.x changed its
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

- **Ambient model detection** — reads the session transcript backward from EOF instead of
  scanning the full file. Warm latency on a 50 MB transcript improved from ~48 ms to ~0.55 ms
  (87×). This path is paid on every tool call in ambient mode.

### Added

- **Ambient policy carve-outs** (all applied to the root reason-tier session only):
  - `AllowReadOnlyBash` — read-only Bash commands permitted: `git log`, `ls`, `cat`, `gts`,
    `hypha recall` (including pipelines through `2>&1 | head`), and equivalent read commands.
    Daemons (`hypha mcp serve`, `hypha hub serve`) are denied by `DenyHyphaDaemons` regardless.
  - `AllowMarkdownAuthoring` — `Write`/`Edit` on `*.md` paths permitted: specs, plans, prompts,
    directives, briefs, and code-in-docs. Code files, notebooks, and no-extension paths remain denied.
  - `AllowOrchestrationTools` — `ToolSearch`, `Skill`, `AskUserQuestion`, `EnterPlanMode`,
    `ExitPlanMode`, and the `TaskCreate`/`TaskGet`/`TaskList`/`TaskUpdate`/`TaskOutput`/`TaskStop`
    family are permitted for the ambient orchestrator.
  - `DenyReasonModelSubagent` (guard) — blocks `Task`/`Agent` calls that carry an explicit
    reason-tier model override for any persona other than `tiller-architect` or `tiller-deep-report`.
  - `DenyImplicitReasonInheritance` (guard) — blocks `Task`/`Agent` calls with a generic subagent
    type (`general-purpose`, `claude`, `Explore`, `Plan`, or blank) and no explicit model field;
    these silently inherit the ambient reason-tier model. Must name a cheaper model or a
    `tiller-*` persona.

- **`tiller --version` / `tiller -v`** — short version aliases alongside `tiller version`.

---

## [0.1.0] — 2026-06-10

Initial release.

- **Ambient mode** — orchestrator-only gating for reason-tier sessions via `PreToolUse`/`PostToolUse`
  hooks; self-activates on `claude-fable-5`/`fable`, transparent otherwise; fail-open on transcript errors.
- **Managed runs** — `tiller run` with full audit trails, arbiter-replayable JSONL, and `tiller promote` → hyphae spore.
- **Tier routing** — `models.toml`: reason / scrutiny / execute; first-match candidate resolution.
- **Scratch store seam** — `fsstore` + postgres + tee rollout backend; `TILLER_RUN_DIR` hot-path guard.
- **Queued dispatch + executor pool** — claim/lease atomics, delivery journal (exactly-once), `tiller pool`.
- **Generic command adapter** — non-Claude backends, degraded enforcement, `DenyDegradedInsight`.
- **Hyphae integration** — traces, recall, `tiller promote`.
- **Canonical subagent personas** — six `tiller-*` agents embedded and installed by `tiller install`.

[0.2.0]: https://github.com/odvcencio/tiller/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/odvcencio/tiller/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/odvcencio/tiller/releases/tag/v0.1.0
