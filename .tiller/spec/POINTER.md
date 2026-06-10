# tiller spec & plan — canonical locations

The v1 spec and implementation plan moved to the `m31labs/tiller` hyphae
space on 2026-06-10. Do not author specs/plans in this repo.

| Document | Hypha URI / id | File path |
|---|---|---|
| Spec (v1, normative) | `hypha://m31labs/tiller` · `spec.tiller-v1` | `~/.hyphae/spaces/m31labs-tiller/specs/tiller-v1.md` |
| Implementation plan | `hypha://m31labs/tiller` · `plan.tiller-v1-implementation` | `~/.hyphae/spaces/m31labs-tiller/plans/tiller-v1-implementation.md` |
| Space manifest | `space.m31labs-tiller` | `~/.hyphae/spaces/m31labs-tiller/SPACE.md` |

Retrieve with `hypha show spec.tiller-v1`, `hypha show
plan.tiller-v1-implementation`, or `hypha recall "tiller"`.

The verified default Arbiter policies are product artifacts and live in this
repo at `policy/{dispatch,toolgate}.arb` (re-verified with `arbiter check`
on 2026-06-10).
