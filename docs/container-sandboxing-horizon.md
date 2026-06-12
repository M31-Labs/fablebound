# Container Sandboxing and Horizon Governance

Tiller should govern agents across four layers:

1. Dispatch policy decides whether an agent may exist and what tier/profile it gets.
2. Toolgate policy governs individual tool calls when the backend exposes hooks.
3. Runtime sandboxing contains backends that cannot expose hooks or that run risky code.
4. Horizon capability manifests optionally add host-side kernel observability or enforcement.

This keeps the boundary clean: Tiller owns agent/session policy, dispatch records,
container lifecycle, and audit. Horizon owns eBPF/LSM/cgroup program generation,
capability manifests, and kernel prerequisite reporting.

## Threat Model

Command-style and generic CLI adapters cannot be trusted to self-police tool
calls. Execution agents can run arbitrary project code. A model may also ignore
instructions, overwrite scratch, open network connections, or mutate files
outside the intended workspace if the runtime lets it.

The sandbox layer is the backstop for those cases. Hook enforcement is still the
best UX when available, but it is not enough for generic backends.

## Contract

Each dispatch can carry a `sandbox` record:

- `mode`: `none`, `process`, or `container`
- `status`: `requested`, `active`, `bypassed`, or `unavailable`
- `profile`: role-shaped policy label such as `orchestrator`, `readonly`, `execution`, or `debugger`
- `runner`: implementation such as `bubblewrap`, `oci`, `process`, or a Horizon sidecar
- `workspace`: `read-only`, `overlay`, or `writable`
- `network`: `inherit`, `disabled`, `loopback`, or `egress`
- `horizon`: capability manifest references with hashes and danger axes

Only `status=active` is a hard isolation claim. `requested` records policy
intent before a runner exists, and `unavailable` or `bypassed` make escape
hatches auditable while this is still experimental.

## Horizon Integration

Horizon should enter through capability manifests, not by making Tiller depend
on Horizon as its container runtime. The first useful flow is:

1. Compile or select Horizon programs outside the dispatch hot path.
2. Load the resulting manifest.
3. Record manifest path, hash, capability name, and danger axes on the dispatch.
4. Let dispatch policy gate whether that capability set may be used.
5. Let a host-managed sidecar attach observe/block programs when approved.

Observe-only manifests are the safe first target: exec observe, file-open
observe, and connect observe. Blocking manifests such as LSM exec deny or
cgroup connect deny should require explicit policy allowance because they need
kernel support and elevated host capabilities.

## First Slices

1. Persist sandbox metadata on dispatch records and queryable mirrors.
2. Teach dispatch policy about `dispatch.sandbox.*` and Horizon manifest count.
3. Add a no-op/process sandbox runner so adapters can receive the same contract.
4. Add a rootless container runner, likely `bubblewrap` first and OCI later.
5. Add a Horizon manifest loader/validator and policy checks for danger axes.
6. Promote generic command adapters from `degraded` to `sandboxed` only when an
   active runtime sandbox wraps the process.

This makes generic agent harnesses usable without pretending they have hook-level
toolgate enforcement.
