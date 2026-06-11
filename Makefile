.PHONY: build test vet clean policy-sync policy-replay seam-check

BIN     := tiller
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X m31labs.dev/tiller/internal/cli.Version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tiller

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN)

# Verify that cli/spawn/hook/hyphae do not import internal/run directly (P1.4).
# Exit 0 = seam is clean. Exit 1 = a direct run import was found.
seam-check:
	@result=$$(grep -rn '"m31labs.dev/tiller/internal/run"' internal/cli internal/spawn internal/hook internal/hyphae 2>/dev/null); \
	if [ -n "$$result" ]; then \
		echo "SEAM VIOLATION: direct internal/run imports found:"; \
		echo "$$result"; \
		exit 1; \
	fi
	@echo "seam-check OK: no direct internal/run imports in cli/spawn/hook/hyphae"

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
