# tiller v2 P5.2 — Non-Claude Backend End-to-End Demo

Repeatable acceptance checklist proving a non-Claude command backend executes a
dispatch end-to-end. No real `claude` binary needed; `echo-agent` (a POSIX shell
script) serves the execute tier.

---

## Run-creation decision

`tiller run` always spawns the root orchestrator via the claude-headless adapter
(reads `TILLER_CLAUDE_BIN`). The demo uses `testdata/bin/claude-stub` for the
root — it emits a minimal result JSON and exits immediately without contacting
any AI service. The demo's proof point is the **worker** dispatch executing via
`echo-agent` through the command adapter; the root's claude-stub is inert setup.

---

## Prerequisites

```sh
# Build tiller
cd ~/work/tiller
go build -o /tmp/tiller ./cmd/tiller

# Confirm echo-agent is executable
ls -la testdata/bin/echo-agent
# expected: -rwxr-xr-x ...

# Confirm stubs exist
ls testdata/bin/claude-stub testdata/bin/echo-agent
```

---

## Setup: scratch project directory

```sh
DEMO_DIR=$(mktemp -d /tmp/tiller-v2-demo-XXXX)
echo "DEMO_DIR=$DEMO_DIR"
cd "$DEMO_DIR"

# Create .tiller layout
mkdir -p .tiller/policy

# Copy policies from repo
REPO=~/work/tiller
cp "$REPO/policy/dispatch.arb" .tiller/policy/
cp "$REPO/policy/toolgate.arb" .tiller/policy/

# Write fixture models.toml: execute tier → command:echo-agent/-
ECHO_AGENT="$REPO/testdata/bin/echo-agent"
cat > .tiller/models.toml << EOF
[tiers.reason]
candidates = ["claude-headless:anthropic/fable"]

[tiers.scrutiny]
candidates = ["claude-headless:anthropic/opus"]

[tiers.execute]
candidates = ["command:echo-agent/-"]

[adapter.echo-agent]
argv = ["$ECHO_AGENT", "{brief}"]
report = "stdout"
timeout = "30s"
EOF
```

---

## Check 1 — Create the run (root uses claude-stub)

```sh
ROOT_STUB="$REPO/testdata/bin/claude-stub"
TILLER_CLAUDE_BIN="$ROOT_STUB" /tmp/tiller run "P5.2 demo: echo-agent worker"
```

**Expected**:
```
run <id> started (orchestrator dispatched as root)
run <id>: completed
```

```sh
RUN=$(ls .tiller/runs/ | sort | tail -1)
RUNDIR=".tiller/runs/$RUN"
echo "RUN=$RUN"
```

---

## Check 2 — Queue a worker dispatch with `--queue`

```sh
TILLER_RUN_DIR="$RUNDIR" TILLER_ROLE="user" TILLER_DEPTH="0" \
  /tmp/tiller dispatch --queue --role worker --tier execute --brief "do the thing"
```

**Expected** (stderr + stdout):
```
queued d01 as worker (role=worker, tier=execute, status=pending)
d01
```

Note: `--tier execute` is required here because the dispatch policy
`DenyDegradedInsight` fires when `dispatch.tier == ""` and enforcement is
`degraded`. Passing `--tier execute` makes the requested tier explicit before
policy evaluation.

---

## Check 3 — Start pool, complete within 5s, kill

```sh
/tmp/tiller pool --poll 1s --max-concurrent 1 &
POOL_PID=$!

# Wait up to 5s for d01 to complete
for i in 1 2 3 4 5; do
  sleep 1
  STATUS=$(python3 -c "import json; d=json.load(open('$RUNDIR/dispatches/d01/meta.json')); print(d['status'])" 2>/dev/null)
  echo "t+${i}s: d01=$STATUS"
  [ "$STATUS" = "completed" ] && break
done

kill $POOL_PID
wait $POOL_PID 2>/dev/null
```

**Expected** (pool stderr within 1–2s):
```
tiller pool: starting (poll=1s lease=2m0s max-concurrent=1 ...)
pool: claim <runID>/d01
pool: complete <runID>/d01 status=completed cost=$0.0000
```

---

## Check 4 — `tiller runs show` shows `d01 worker(execute)` completed

```sh
/tmp/tiller runs show "$RUN"
```

**Expected** (dispatches section):
```
  └─ d01 worker(execute) [completed] → dispatches/d01/report.md
  └─ root orchestrator(reason) [completed $0.0010] → dispatches/root/report.md
```

Key: `d01 worker(execute) [completed]` — role is `worker`, tier label is `execute`.
The adapter name is not shown in the text tree; verify via meta.json (Check 5).

---

## Check 5 — report.md contains echo-agent output; meta.json shows adapter=command

```sh
# Report content
cat "$RUNDIR/dispatches/d01/report.md"
```

**Expected**:
```
# echo-agent report
brief: do the thing
marker: ECHO-AGENT-OK
```

```sh
# Adapter field in dispatch record
python3 -c "import json; d=json.load(open('$RUNDIR/dispatches/d01/meta.json')); \
  print(f'adapter={d[\"adapter\"]} provider={d[\"provider\"]} enforcement={d[\"enforcement\"]}')"
```

**Expected**:
```
adapter=command provider=echo-agent enforcement=degraded
```

---

## Check 6 — audit/dispatch.jsonl has the Allow event

```sh
python3 - << 'EOF'
import json

with open(f'.tiller/runs/{RUN}/audit/dispatch.jsonl') as f:
    lines = [json.loads(l) for l in f if l.strip()]

print(f"audit events: {len(lines)}")
for obj in lines:
    ctx = obj.get('context', {})
    d = ctx.get('dispatch', {})
    verdicts = [(r.get('action'), r.get('name')) for r in obj.get('rules', [])]
    print(f"  role={d.get('role')} tier={d.get('tier')} verdicts={verdicts}")
EOF
```

**Expected**:
```
audit events: 1
  role=worker tier=execute enforcement=degraded verdicts=[('Allow', 'AllowDispatch')]
```

---

## Teardown

```sh
rm -rf "$DEMO_DIR"
```

---

## Observed run: 20260610-202219-4xji

| Check | Command | Result |
|-------|---------|--------|
| 1 create run | `TILLER_CLAUDE_BIN=claude-stub tiller run "…"` | PASS — `completed` in <1s |
| 2 queue dispatch | `tiller dispatch --queue --role worker --tier execute --brief "do the thing"` | PASS — `d01` queued pending |
| 3 pool completes | `tiller pool --poll 1s &` ; wait 1s | PASS — `d01 status=completed` at t+1s |
| 4 runs show | `tiller runs show $RUN` | PASS — `d01 worker(execute) [completed]` |
| 5 report + meta | `cat report.md` ; `meta.json` | PASS — `marker: ECHO-AGENT-OK` ; `adapter=command` |
| 6 audit Allow | `audit/dispatch.jsonl` | PASS — `AllowDispatch` for worker/execute |

### Key outputs

**`tiller runs show`**:
```
dispatches:
  └─ d01 worker(execute) [completed] → dispatches/d01/report.md
  └─ root orchestrator(reason) [completed $0.0010] → dispatches/root/report.md
```

**`dispatches/d01/report.md`**:
```
# echo-agent report
brief: do the thing
marker: ECHO-AGENT-OK
```

**`audit/dispatch.jsonl` (excerpt)**:
```json
{"context":{"dispatch":{"role":"worker","tier":"execute","enforcement":"degraded",...}},"rules":[{"action":"Allow","name":"AllowDispatch",...}],...}
```

---

## Notes

- **Adapter visibility**: The text tree shows `role(tier)` but not the adapter name.
  Adapter name is in `meta.json` (`adapter: "command"`). Adding it to text output
  was deferred; assert via meta.json inspection (Check 5).
- **`--tier execute` required**: The dispatch policy `DenyDegradedInsight` fires
  when `dispatch.tier` is empty (the requested tier, not the resolved tier) and
  enforcement is `degraded`. Passing `--tier execute` makes the tier explicit at
  policy evaluation time. This is correct behavior — the policy gates on what the
  caller explicitly requests.
- **Go e2e test**: `internal/pool/command_e2e_test.go` `TestCommandAdapterE2E`
  drives the full flow in-process (fsstore + command adapter + pool) without
  a subprocess. It seeds the dispatch directly (bypassing CLI) so audit events
  are not written; audit is verified via the demo CLI flow instead. The test
  asserts: status=completed, adapter=command, report contains ECHO-AGENT-OK.
