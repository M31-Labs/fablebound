.PHONY: build test vet clean policy-sync

BIN := fablebound

build:
	go build -o $(BIN) ./cmd/fablebound

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
