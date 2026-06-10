# tiller T1.9 Live End-to-End Demo

Repeatable acceptance checklist for the T1.9 phase gate.  
Run as a human reviewer with real `claude` on PATH in a scratch git repo.

---

## Prerequisites

```sh
# Build tiller
cd ~/work/tiller
go build -o /tmp/tiller ./cmd/tiller

# Verify claude is available
which claude && claude --version

# (Optional) Build arbiter CLI for replay check
cd ~/work/arbiter
go build -o /tmp/arbiter ./cmd/arbiter
```

---

## Setup: scratch git repo

```sh
DEMO_DIR=/tmp/tiller-demo
rm -rf "$DEMO_DIR" && mkdir "$DEMO_DIR"
cd "$DEMO_DIR"
git init
git config user.email "you@example.com"
git config user.name "Your Name"

cat > README.md << 'EOF'
# The Lighthouse Keeper's Algorithm

[A few paragraphs of prose — something with enough content for an investigator
to summarize. Must be a non-trivial document so the investigator has real work.]
EOF

git add README.md && git commit -m "add README"
```

---

## Check 1 — `init` and `policy vet` exit 0

```sh
PATH=/tmp:$PATH tiller init
PATH=/tmp:$PATH tiller policy vet
```

**Expected**: `tiller init: done` followed by two sha256 hashes and
`arbiter test` suites printing `54 passed, 0 failed` / `52 passed, 0 failed`.
Both commands exit 0.

---

## Check 2 — `tiller run` completes

```sh
PATH=/tmp:$PATH tiller run \
  "Dispatch an investigator to summarize README.md into its report, then \
dispatch a worker to write notes/haiku.md from that report. \
Do not read README.md yourself beyond the first line."
```

**Expected**: `run <id> started ...` then `run <id>: completed`. The run id
(e.g. `20260610-044705-shbh`) is used in subsequent checks — set it:

```sh
RUN=$(ls .tiller/runs/ | sort | tail -1)
RUNDIR=".tiller/runs/$RUN"
```

**Note**: The orchestrator role prompt includes a `**Demo gate probe**` that
instructs the orchestrator to attempt `Bash: ls` once at the start. This
produces a toolgate Deny that satisfies Check 5. Remove the probe section
from `internal/roles/defaults/orchestrator.md` after the demo.

---

## Check 3 — `runs show` renders tree with models

```sh
tiller runs show "$RUN"
```

**Expected output (dispatches section)**:
```
root orchestrator(fable) [completed] → dispatches/root/report.md
  └─ d01 investigator(sonnet) [completed] → dispatches/d01/report.md
  └─ d02 worker(sonnet) [completed] → dispatches/d02/report.md
```

Root must show model `fable`; children must show `sonnet` or `haiku`.

---

## Check 4 — Reports non-empty; `notes/haiku.md` exists

```sh
# Reports
wc -c "$RUNDIR/dispatches/d01/report.md"   # expect > 0 bytes
wc -c "$RUNDIR/dispatches/d02/report.md"   # expect > 0 bytes

# Haiku
cat notes/haiku.md                          # must exist and contain haiku text
```

**Expected**: Both `dispatches/d*/report.md` files are non-empty; `notes/haiku.md`
exists in the workspace root (not in `.tiller/`).

---

## Check 5 — ≥1 toolgate Deny; ≥2 dispatch Allows

```sh
python3 - << 'EOF'
import json

def first_verdict(rules):
    if not rules: return 'unknown', ''
    mp = min(r['priority'] for r in rules)
    mr = [r for r in rules if r['priority'] == mp]
    rank = {'Deny': 2, 'Ask': 1, 'Allow': 0}
    w = max(mr, key=lambda r: rank.get(r['action'], 0))
    return w['action'], w['name']

import os; RUN = os.environ['RUN']; RUNDIR = f'.tiller/runs/{RUN}'

print('=== toolgate.jsonl ===')
denies, allows = [], []
with open(f'{RUNDIR}/audit/toolgate.jsonl') as f:
    for line in f:
        obj = json.loads(line.strip())
        v, n = first_verdict(obj.get('rules', []))
        t = obj.get('context', {}).get('tool', {}).get('name', '')
        if v == 'Deny': denies.append((t, n))
        else: allows.append((t, n))
print(f'Denies: {len(denies)} (expect >=1)')
for d in denies: print(f'  DENY tool={d[0]} rule={d[1]}')

print()
print('=== dispatch.jsonl ===')
da = []
with open(f'{RUNDIR}/audit/dispatch.jsonl') as f:
    for line in f:
        obj = json.loads(line.strip())
        v, n = first_verdict(obj.get('rules', []))
        role = obj.get('context', {}).get('dispatch', {}).get('role', '')
        if v == 'Allow': da.append((role, n))
print(f'Allows: {len(da)} (expect >=2)')
for a in da: print(f'  ALLOW role={a[0]} rule={a[1]}')
EOF
```

**Expected**: toolgate ≥1 Deny (typically `Bash ls` → `OrchestratorDenyRest`);
dispatch ≥2 Allows (investigator + worker, rule `AllowDispatch`).

---

## Check 6 — `arbiter replay` zero diffs

```sh
# Build arbiter if not on PATH
# go build -o /tmp/arbiter ~/work/arbiter/cmd/arbiter

arbiter replay .tiller/policy/toolgate.arb \
  --audit "$RUNDIR/audit/toolgate.jsonl"
# Expected: changed: 0  unchanged: N

arbiter replay .tiller/policy/dispatch.arb \
  --audit "$RUNDIR/audit/dispatch.jsonl"
# Expected: changed: 0  unchanged: 2
```

**Expected**: both replays report `changed: 0`.

---

## Check 7 — Automagic traces: non-empty, correct kinds, role+depth match

```sh
python3 - << 'EOF'
import json, glob, os; RUN = os.environ['RUN']; RUNDIR = f'.tiller/runs/{RUN}'

# 7a: tool_trace.jsonl non-empty for every dispatch
print('tool_trace.jsonl:')
for path in sorted(glob.glob(f'{RUNDIR}/dispatches/*/tool_trace.jsonl')):
    d = os.path.basename(os.path.dirname(path))
    with open(path) as f: evs = [json.loads(l) for l in f if l.strip()]
    print(f'  {d}: {len(evs)} events, first role={evs[0].get("role")} depth={evs[0].get("depth")}')

# 7b: root context_trace has >=2 dispatch entries
print()
with open(f'{RUNDIR}/dispatches/root/context_trace.jsonl') as f:
    evs = [json.loads(l) for l in f if l.strip()]
dispatches = [e for e in evs if e.get('kind') == 'dispatch']
print(f'root context_trace dispatch entries: {len(dispatches)} (expect >=2)')
for e in dispatches:
    print(f'  child_id={e.get("child_id")} role={e.get("role")} depth={e.get("depth")}')

# 7c: each child has kind:report in context_trace
print()
for d in ['d01', 'd02']:
    with open(f'{RUNDIR}/dispatches/{d}/context_trace.jsonl') as f:
        evs = [json.loads(l) for l in f if l.strip()]
    reports = [e for e in evs if e.get('kind') == 'report']
    print(f'{d} context_trace report entries: {len(reports)} (expect 1)')
    for e in reports:
        print(f'  role={e.get("role")} depth={e.get("depth")}')
EOF
```

**Expected**:
- Every dispatch's `tool_trace.jsonl` is non-empty with correct `role`/`depth`.
- root's `context_trace.jsonl` has 2 `kind:dispatch` entries (d01, d02), each with `depth=0` (orchestrator).
- d01 and d02 each have 1 `kind:report` entry with `depth=1`.

---

## Check 8 — Both hook blocks in every `settings.json`

```sh
python3 - << 'EOF'
import json, glob, os; RUN = os.environ['RUN']; RUNDIR = f'.tiller/runs/{RUN}'

for d in ['root', 'd01', 'd02']:
    path = f'{RUNDIR}/dispatches/{d}/settings.json'
    with open(path) as f: s = json.load(f)
    hooks = s.get('hooks', {})
    pre = hooks.get('PreToolUse', [])
    post = hooks.get('PostToolUse', [])
    def has_tiller(block):
        return any('tiller hook' in (h2.get('command','') if isinstance(h2,dict) else '')
                   for h in block for h2 in (h.get('hooks',[]) if isinstance(h,dict) else []))
    print(f'{d}: PreToolUse tiller={has_tiller(pre)}, PostToolUse tiller={has_tiller(post)}')
EOF
```

**Expected**: All three dispatches (root, d01, d02) print `PreToolUse tiller=True, PostToolUse tiller=True`.

---

## §replay — Policy Replay Regression Gate (T3.2)

`make policy-replay RUN=<id>` replays both policies against a run's audit
files using the arbiter CLI.  It exits non-zero if any audit event differs
from what the current policy would decide, catching accidental policy
regressions.

```sh
# Basic usage: replay against the most recent run.
RUN=$(ls .tiller/runs/ | sort | tail -1)
ARBITER_BIN=/tmp/arbiter make policy-replay RUN="$RUN"
# Expected output (no regressions):
#   === replaying toolgate.arb against .tiller/runs/<id>/audit/toolgate.jsonl ===
#   changed: 0  unchanged: N
#   === replaying dispatch.arb against .tiller/runs/<id>/audit/dispatch.jsonl ===
#   changed: 0  unchanged: 2
#   replay complete: no diffs
```

**Regression workflow**: To verify the gate catches real regressions:

```sh
# 1. Edit toolgate.arb to deny git log for readonly_bash_roles.
#    (Add a high-priority deny rule for "git log" before ReadOnlyBash.)
#
# 2. Replay against a run that used git log (e.g. from an investigator).
ARBITER_BIN=/tmp/arbiter make policy-replay RUN="$RUN"
# Expected: changed: N (non-zero) for toolgate.jsonl — replay fails.

# 3. Revert the edit.
git checkout policy/toolgate.arb

# 4. Replay again.
ARBITER_BIN=/tmp/arbiter make policy-replay RUN="$RUN"
# Expected: changed: 0 — replay passes.
```

---

## Post-demo cleanup

Remove the demo probe from the orchestrator role (already done in the source —
the `**Demo gate probe**` paragraph was removed from
`internal/roles/defaults/orchestrator.md` after the demo passed).

---

## Observed run: 20260610-044705-shbh

| Check | Result |
|-------|--------|
| 1 init+vet | PASS (exit 0, 54+52 tests green, 100% coverage) |
| 2 run completed | PASS (2.8 min) |
| 3 tree with models | PASS (root=fable, d01+d02=sonnet) |
| 4 reports+haiku.md | PASS (d01: 46259B, d02: 41727B; haiku.md: 121B) |
| 5 ≥1 Deny + ≥2 Allow | PASS (Deny: Bash ls / OrchestratorDenyRest; Allow: 2 dispatches) |
| 6 arbiter replay | PASS (toolgate 17/0 changed, dispatch 2/0 changed) |
| 7 traces | PASS (all tool_trace non-empty; root 2 dispatch events; children 1 report each) |
| 8 hook blocks | PASS (Pre+Post in root/d01/d02) |
