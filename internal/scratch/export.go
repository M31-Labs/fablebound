package scratch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/arbiter/audit"
)

// ExportRun reads all run artifacts from src (typically a pgstore) and writes
// the v1 file layout to destDir. Idempotent: existing files are overwritten.
//
// Layout written (identical to fsstore on-disk layout):
//
//	<destDir>/
//	  manifest.json
//	  task.md
//	  dispatches/<id>/meta.json
//	  dispatches/<id>/brief.md         (if present)
//	  dispatches/<id>/report.md        (if present)
//	  dispatches/<id>/settings.json    (if present)
//	  audit/dispatch.jsonl
//	  audit/toolgate.jsonl
//
// This function is used by `tiller runs export` to materialise a pg-stored run
// so that `arbiter replay` and other file-based tools keep working verbatim.
func ExportRun(src Store, runID, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("export: mkdir %s: %w", destDir, err)
	}

	// ── manifest.json ─────────────────────────────────────────────────────────
	runRec, err := src.ReadRun(runID)
	if err != nil {
		return fmt.Errorf("export: read run: %w", err)
	}
	manifestData, err := json.MarshalIndent(runRec, "", "  ")
	if err != nil {
		return fmt.Errorf("export: marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "manifest.json"), manifestData, 0o644); err != nil {
		return fmt.Errorf("export: write manifest.json: %w", err)
	}

	// ── task.md ───────────────────────────────────────────────────────────────
	if runRec.Task != "" {
		if err := os.WriteFile(filepath.Join(destDir, "task.md"), []byte(runRec.Task), 0o644); err != nil {
			return fmt.Errorf("export: write task.md: %w", err)
		}
	}

	// ── dispatches/ ───────────────────────────────────────────────────────────
	dispatches, err := src.ListDispatches(runID)
	if err != nil {
		return fmt.Errorf("export: list dispatches: %w", err)
	}

	dispBase := filepath.Join(destDir, "dispatches")
	if err := os.MkdirAll(dispBase, 0o755); err != nil {
		return fmt.Errorf("export: mkdir dispatches: %w", err)
	}

	for _, d := range dispatches {
		dDir := filepath.Join(dispBase, d.ID)
		if err := os.MkdirAll(dDir, 0o755); err != nil {
			return fmt.Errorf("export: mkdir dispatch %s: %w", d.ID, err)
		}

		// meta.json
		metaData, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return fmt.Errorf("export: marshal dispatch %s meta: %w", d.ID, err)
		}
		if err := os.WriteFile(filepath.Join(dDir, "meta.json"), metaData, 0o644); err != nil {
			return fmt.Errorf("export: write dispatch %s meta.json: %w", d.ID, err)
		}

		// brief.md (soft-fail: may not be present for all dispatches/stores)
		if brief, err := src.ReadBrief(runID, d.ID); err == nil && len(brief) > 0 {
			if err := os.WriteFile(filepath.Join(dDir, "brief.md"), brief, 0o644); err != nil {
				return fmt.Errorf("export: write dispatch %s brief.md: %w", d.ID, err)
			}
		}

		// report.md (soft-fail)
		if report, err := src.ReadReport(runID, d.ID); err == nil && len(report) > 0 {
			if err := os.WriteFile(filepath.Join(dDir, "report.md"), report, 0o644); err != nil {
				return fmt.Errorf("export: write dispatch %s report.md: %w", d.ID, err)
			}
		}

		// settings.json (soft-fail)
		if cfg, err := src.ReadAdapterConfig(runID, d.ID); err == nil && len(cfg) > 0 {
			if err := os.WriteFile(filepath.Join(dDir, "settings.json"), cfg, 0o644); err != nil {
				return fmt.Errorf("export: write dispatch %s settings.json: %w", d.ID, err)
			}
		}
	}

	// ── audit/*.jsonl ─────────────────────────────────────────────────────────
	auditDir := filepath.Join(destDir, "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return fmt.Errorf("export: mkdir audit: %w", err)
	}

	for _, kind := range []string{"dispatch", "toolgate"} {
		destPath := filepath.Join(auditDir, kind+".jsonl")
		if err := exportAuditKind(src, runID, kind, destPath); err != nil {
			return fmt.Errorf("export: audit %s: %w", kind, err)
		}
	}

	return nil
}

// exportAuditKind opens an audit sink from src for the given run/kind and copies
// the JSONL content to destPath. Each line must be a valid DecisionEvent.
func exportAuditKind(src Store, runID, kind, destPath string) error {
	sink, closer, err := src.AuditSink(runID, kind)
	if err != nil {
		// No audit data: write empty file.
		return os.WriteFile(destPath, nil, 0o644)
	}
	defer closer.Close()

	srcPath := sink.Path()

	// Same file: nothing to copy.
	if srcPath == destPath {
		return nil
	}

	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(destPath, nil, 0o644)
		}
		return fmt.Errorf("read %s audit: %w", kind, err)
	}

	// Validate lines are valid DecisionEvent JSON (best-effort; don't fail on
	// partial data — just copy what's there).
	_ = validateAuditLines(srcData)

	return os.WriteFile(destPath, srcData, 0o644)
}

// validateAuditLines checks that each non-empty line is a valid JSON
// audit.DecisionEvent. Used for diagnostics; callers ignore the error.
func validateAuditLines(data []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var ev audit.DecisionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return fmt.Errorf("invalid DecisionEvent JSON: %w", err)
		}
	}
	return sc.Err()
}
