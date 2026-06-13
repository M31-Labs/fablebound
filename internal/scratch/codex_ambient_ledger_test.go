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

func TestCodexAmbientFallbackLedgerAppendRejectsSymlinkedFile(t *testing.T) {
	workspace := t.TempDir()
	path := CodexAmbientFallbackLedgerPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir ledger dir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	const targetContent = "target\n"
	if err := os.WriteFile(target, []byte(targetContent), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatalf("chmod symlink target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink ledger: %v", err)
	}

	err := AppendCodexAmbientFallbackLedger(workspace, LedgerEvent{
		ID:      "ledger-symlink-file",
		Backend: "codex",
		Kind:    "codex.lifecycle_tool",
		Status:  AgentRunStatusRequested,
		At:      time.Now().UTC(),
		Summary: "must not append through symlink",
	})
	if err == nil {
		t.Fatal("AppendCodexAmbientFallbackLedger succeeded for symlinked ledger file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(got) != targetContent {
		t.Fatalf("symlink target content=%q want %q", got, targetContent)
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatalf("stat symlink target: %v", err)
	} else if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("symlink target mode=%o want 644", got)
	}
}

func TestCodexAmbientFallbackLedgerAppendRejectsSymlinkedPathComponents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component string
		rest      []string
		setup     func(t *testing.T, workspace, target string)
	}{
		{
			name:      "tiller",
			component: ".tiller",
			rest:      []string{"scratch", "codex", "ambient-ledger.jsonl"},
			setup: func(t *testing.T, workspace, target string) {
				t.Helper()
				if err := os.Symlink(target, filepath.Join(workspace, ".tiller")); err != nil {
					t.Fatalf("symlink .tiller: %v", err)
				}
			},
		},
		{
			name:      "scratch",
			component: "scratch",
			rest:      []string{"codex", "ambient-ledger.jsonl"},
			setup: func(t *testing.T, workspace, target string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(workspace, ".tiller"), 0o700); err != nil {
					t.Fatalf("mkdir .tiller: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(workspace, ".tiller", "scratch")); err != nil {
					t.Fatalf("symlink scratch: %v", err)
				}
			},
		},
		{
			name:      "codex",
			component: "codex",
			rest:      []string{"ambient-ledger.jsonl"},
			setup: func(t *testing.T, workspace, target string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(workspace, ".tiller", "scratch"), 0o700); err != nil {
					t.Fatalf("mkdir scratch: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(workspace, ".tiller", "scratch", "codex")); err != nil {
					t.Fatalf("symlink codex: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			target := t.TempDir()
			tc.setup(t, workspace, target)

			err := AppendCodexAmbientFallbackLedger(workspace, LedgerEvent{
				ID:      "ledger-symlink-component-" + tc.name,
				Backend: "codex",
				Kind:    "codex.lifecycle_tool",
				Status:  AgentRunStatusRequested,
				At:      time.Now().UTC(),
				Summary: "must not append through symlink path component",
			})
			if err == nil {
				t.Fatalf("AppendCodexAmbientFallbackLedger succeeded for symlinked %s component", tc.component)
			}
			redirected := filepath.Join(append([]string{target}, tc.rest...)...)
			if _, err := os.Stat(redirected); !os.IsNotExist(err) {
				t.Fatalf("redirected ledger stat err=%v want not exist", err)
			}
		})
	}
}

func TestCodexAmbientFallbackLedgerListRejectsSymlinkedFile(t *testing.T) {
	workspace := t.TempDir()
	path := CodexAmbientFallbackLedgerPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir ledger dir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, []byte(`{"id":"target"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink ledger: %v", err)
	}

	if _, err := ListCodexAmbientFallbackLedger(workspace); err == nil {
		t.Fatal("ListCodexAmbientFallbackLedger succeeded for symlinked ledger file")
	}
}

func TestCodexAmbientFallbackLedgerListRejectsSymlinkedPathComponent(t *testing.T) {
	workspace := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(workspace, ".tiller")); err != nil {
		t.Fatalf("symlink .tiller: %v", err)
	}
	if _, err := ListCodexAmbientFallbackLedger(workspace); err == nil {
		t.Fatal("ListCodexAmbientFallbackLedger succeeded for symlinked path component")
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
