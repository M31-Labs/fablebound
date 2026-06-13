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
	if err := ensureCodexAmbientFallbackLedgerDir(workspace); err != nil {
		return err
	}
	if err := rejectSymlinkedCodexAmbientFallbackLedger(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("codex ambient fallback ledger: open %s: %w", path, err)
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
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
	exists, err := validateCodexAmbientFallbackLedgerDir(workspace)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	if err := rejectSymlinkedCodexAmbientFallbackLedger(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
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

func ensureCodexAmbientFallbackLedgerDir(workspace string) error {
	base := filepath.Clean(workspace)
	dir := base
	for _, elem := range []string{".tiller", "scratch", "codex"} {
		dir = filepath.Join(dir, elem)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
				return fmt.Errorf("codex ambient fallback ledger: mkdir %s: %w", dir, err)
			}
			info, err = os.Lstat(dir)
		}
		if err != nil {
			return fmt.Errorf("codex ambient fallback ledger: lstat %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("codex ambient fallback ledger: symlink path component %s rejected", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("codex ambient fallback ledger: path component %s is not a directory", dir)
		}
	}
	if err := chmodDirNoFollow(dir, 0o700); err != nil {
		return fmt.Errorf("codex ambient fallback ledger: chmod %s: %w", dir, err)
	}
	return nil
}

func validateCodexAmbientFallbackLedgerDir(workspace string) (bool, error) {
	dir := filepath.Clean(workspace)
	for _, elem := range []string{".tiller", "scratch", "codex"} {
		dir = filepath.Join(dir, elem)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("codex ambient fallback ledger: lstat %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("codex ambient fallback ledger: symlink path component %s rejected", dir)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("codex ambient fallback ledger: path component %s is not a directory", dir)
		}
	}
	return true, nil
}

func rejectSymlinkedCodexAmbientFallbackLedger(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return err
	}
	if err != nil {
		return fmt.Errorf("codex ambient fallback ledger: lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("codex ambient fallback ledger: symlink ledger file %s rejected", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("codex ambient fallback ledger: ledger file %s is not a regular file", path)
	}
	return nil
}

func chmodDirNoFollow(path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}
