package scratch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCodexAmbientFallbackLedgerAppendReadAndPermissions(t *testing.T) {
	workspace := t.TempDir()
	ev := LedgerEvent{
		ID:      "ledger-001",
		Backend: "codex",
		Kind:    "codex.session_start",
		Status:  "observed",
		At:      time.Now().UTC(),
		Summary: "session started",
	}

	if err := AppendCodexAmbientFallbackLedger(workspace, ev); err != nil {
		t.Fatalf("AppendCodexAmbientFallbackLedger: %v", err)
	}
	events, err := ListCodexAmbientFallbackLedger(workspace)
	if err != nil {
		t.Fatalf("ListCodexAmbientFallbackLedger: %v", err)
	}
	if len(events) != 1 || events[0].ID != ev.ID || events[0].Kind != ev.Kind {
		t.Fatalf("unexpected events: %#v", events)
	}

	path := CodexAmbientFallbackLedgerPath(workspace)
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat ledger file: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("ledger file mode=%o want 600", got)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("stat ledger dir: %v", err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("ledger dir mode=%o want 700", got)
	}
}

func TestCodexAmbientFallbackLedgerConcurrentAppends(t *testing.T) {
	workspace := t.TempDir()
	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AppendCodexAmbientFallbackLedger(workspace, LedgerEvent{
				ID:      fmt.Sprintf("ledger-concurrent-%02d", i),
				Backend: "codex",
				Kind:    "codex.lifecycle_tool",
				Status:  AgentRunStatusRequested,
				At:      time.Now().UTC(),
				Summary: "concurrent append",
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendCodexAmbientFallbackLedger: %v", err)
		}
	}

	events, err := ListCodexAmbientFallbackLedger(workspace)
	if err != nil {
		t.Fatalf("ListCodexAmbientFallbackLedger: %v", err)
	}
	if len(events) != n {
		t.Fatalf("event count=%d want %d", len(events), n)
	}
	for i, ev := range events {
		if ev.Kind != "codex.lifecycle_tool" || ev.Backend != "codex" || ev.Status != AgentRunStatusRequested {
			t.Fatalf("event %d invalid: %#v", i, ev)
		}
	}
}

func TestCodexAmbientFallbackLedgerMissingReturnsEmpty(t *testing.T) {
	events, err := ListCodexAmbientFallbackLedger(t.TempDir())
	if err != nil {
		t.Fatalf("ListCodexAmbientFallbackLedger missing: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events=%#v want empty", events)
	}
}
