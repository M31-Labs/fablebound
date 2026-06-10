.PHONY: build test vet clean policy-sync policy-replay

BIN := tiller

build:
	go build -o $(BIN) ./cmd/tiller

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN)

# Keep policy/ and internal/policy/defaults/ in sync (T0.2).
policy-sync:
	cp policy/dispatch.arb internal/policy/defaults/dispatch.arb
	cp policy/toolgate.arb internal/policy/defaults/toolgate.arb

# Replay both policies against a run's audit files (T3.2).
# Usage: make policy-replay RUN=<run-id>
# Replays policy/toolgate.arb against .tiller/runs/<RUN>/audit/toolgate.jsonl
# and policy/dispatch.arb against .tiller/runs/<RUN>/audit/dispatch.jsonl.
# Exits non-zero if any audit event differs from what the current policy would decide.
# Requires: arbiter CLI on PATH (or ARBITER_BIN=<path>).
ARBITER_BIN ?= arbiter
RUNDIR := .tiller/runs/$(RUN)

policy-replay:
	@if [ -z "$(RUN)" ]; then echo "error: RUN is required (make policy-replay RUN=<run-id>)" >&2; exit 1; fi
	@if [ ! -d "$(RUNDIR)" ]; then echo "error: run directory $(RUNDIR) not found" >&2; exit 1; fi
	@echo "=== replaying toolgate.arb against $(RUNDIR)/audit/toolgate.jsonl ==="
	$(ARBITER_BIN) replay policy/toolgate.arb --audit $(RUNDIR)/audit/toolgate.jsonl
	@echo "=== replaying dispatch.arb against $(RUNDIR)/audit/dispatch.jsonl ==="
	$(ARBITER_BIN) replay policy/dispatch.arb --audit $(RUNDIR)/audit/dispatch.jsonl
	@echo "replay complete: no diffs"
