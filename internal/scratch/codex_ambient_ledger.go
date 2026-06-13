package scratch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const codexAmbientFallbackLedgerRelPath = ".tiller/scratch/codex/ambient-ledger.jsonl"

// CodexAmbientFallbackLedgerPath returns the unmanaged Codex ambient ledger path.
func CodexAmbientFallbackLedgerPath(workspace string) string {
	return filepath.Join(filepath.Clean(workspace), codexAmbientFallbackLedgerRelPath)
}

// AppendCodexAmbientFallbackLedger appends one JSONL ledger event to the
// unmanaged Codex ambient ledger. Callers in hook paths should treat errors as
// best-effort/fail-open observations.
func AppendCodexAmbientFallbackLedger(workspace string, ev LedgerEvent) error {
	path := CodexAmbientFallbackLedgerPath(workspace)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("codex ambient fallback ledger: mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("codex ambient fallback ledger: chmod %s: %w", dir, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("codex ambient fallback ledger: open %s: %w", path, err)
	}
	defer f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("codex ambient fallback ledger: chmod %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("codex ambient fallback ledger: flock %s: %w", path, err)
	}
	return json.NewEncoder(f).Encode(ev)
}

// ListCodexAmbientFallbackLedger reads unmanaged Codex ambient ledger events in
// append order. A missing ledger returns an empty slice.
func ListCodexAmbientFallbackLedger(workspace string) ([]LedgerEvent, error) {
	path := CodexAmbientFallbackLedgerPath(workspace)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codex ambient fallback ledger: open %s: %w", path, err)
	}
	defer f.Close()

	var out []LedgerEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev LedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("codex ambient fallback ledger: decode %s:%d: %w", path, lineNo, err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex ambient fallback ledger: scan %s: %w", path, err)
	}
	return out, nil
}
