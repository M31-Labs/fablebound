package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func TestRunAmbientDisableEnable(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	if err := runAmbient([]string{"disable"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	marker := filepath.Join(dir, ambientgate.DisabledRelPath)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing after disable: %v", err)
	}
	if !ambientgate.IsDisabled(dir) {
		t.Fatal("ambientgate should report disabled")
	}

	if err := runAmbient([]string{"status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := runAmbient([]string{"enable"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker should be removed after enable, stat err=%v", err)
	}
}

func TestRunAmbientRejectsUnknownCommand(t *testing.T) {
	if err := runAmbient([]string{"pause"}); err == nil {
		t.Fatal("expected unknown ambient command to fail")
	}
}

func TestRunAmbientStatusWithoutRunContextSucceeds(t *testing.T) {
	dir := t.TempDir()
	withChdir(t, dir)
	t.Setenv("TILLER_RUN_DIR", "")

	out := captureAmbientStdout(t, func() {
		if err := runAmbient([]string{"status"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	if !strings.Contains(out, "tiller: ambient enabled for "+dir) {
		t.Fatalf("status output missing ambient marker: %q", out)
	}
	if strings.Contains(out, "run:") {
		t.Fatalf("status without run context should not print run digest: %q", out)
	}
}

func TestRunAmbientNextRequiresRunContext(t *testing.T) {
	t.Setenv("TILLER_RUN_DIR", "")
	if err := runAmbient([]string{"next"}); err == nil || !strings.Contains(err.Error(), "TILLER_RUN_DIR") {
		t.Fatalf("next error = %v, want missing TILLER_RUN_DIR", err)
	}
}

func TestRunAmbientStepRequiresDryRun(t *testing.T) {
	if err := runAmbient([]string{"step"}); err == nil || !strings.Contains(err.Error(), "only --dry-run is supported") {
		t.Fatalf("step error = %v, want dry-run requirement", err)
	}
	if err := runAmbient([]string{"step", "--force"}); err == nil || !strings.Contains(err.Error(), "usage: tiller ambient step --dry-run") {
		t.Fatalf("step --force error = %v, want dry-run usage", err)
	}
}

func TestRunAmbientStepDryRunRequiresRunContext(t *testing.T) {
	t.Setenv("TILLER_RUN_DIR", "")
	if err := runAmbient([]string{"step", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "TILLER_RUN_DIR") {
		t.Fatalf("step --dry-run error = %v, want missing TILLER_RUN_DIR", err)
	}
}

func TestRunAmbientNextPrintsScratchDigest(t *testing.T) {
	dir := t.TempDir()
	withChdir(t, dir)
	runsBase := filepath.Join(dir, ".tiller", "runs")
	st := fsstore.Open(runsBase)
	now := time.Now().UTC()
	runID, err := st.CreateRun(&scratch.Run{
		ID:           "20260612-100000-test",
		Task:         "digest test",
		Workspace:    dir,
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:      "distill-001",
		Kind:    "ambient.distillation",
		Status:  "completed",
		At:      now,
		Summary: "Latest compact state.\nContinue with tests.",
	}); err != nil {
		t.Fatalf("AppendLedgerEvent: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)
	t.Setenv("TILLER_RUN_DIR", runDir)

	out := captureAmbientStdout(t, func() {
		if err := runAmbient([]string{"next"}); err != nil {
			t.Fatalf("next: %v", err)
		}
	})

	want := []string{
		"tiller ambient: enabled for " + dir,
		"run: " + runID,
		"status: running",
		"next_action: proceed confidence=70 risk=low budget=ok fallback=false",
		"target: orchestrator",
		"reason: no blocker, pending work, checkpoint, risk, or spend pressure matched",
		"distillation: Latest compact state. Continue with tests.",
		"suggested_move: continue orchestration",
		"read: " + filepath.Join(runDir, "status.md"),
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Fatalf("next output missing %q:\n%s", line, out)
		}
	}
}

func TestRunAmbientStepDryRunPrintsProceedPacket(t *testing.T) {
	dir := t.TempDir()
	withChdir(t, dir)
	runsBase := filepath.Join(dir, ".tiller", "runs")
	st := fsstore.Open(runsBase)
	now := time.Now().UTC()
	runID, err := st.CreateRun(&scratch.Run{
		ID:           "20260612-120000-test",
		Task:         "step packet test",
		Workspace:    dir,
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendLedgerEvent(runID, scratch.LedgerEvent{
		ID:      "distill-001",
		Kind:    "ambient.distillation",
		Status:  "completed",
		At:      now,
		Summary: "Compact state for packet.",
	}); err != nil {
		t.Fatalf("AppendLedgerEvent: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)
	t.Setenv("TILLER_RUN_DIR", runDir)

	out := captureAmbientStdout(t, func() {
		if err := runAmbient([]string{"step", "--dry-run"}); err != nil {
			t.Fatalf("step --dry-run: %v", err)
		}
	})

	want := []string{
		"dry_run: true",
		"run: " + runID,
		"next_action: proceed confidence=70 risk=low budget=ok fallback=false",
		"agent_type: orchestrator",
		"objective: Continue orchestration using the Arbiter target: orchestrator.",
		"context_paths:",
		"- " + filepath.Join(runDir, "status.md"),
		"- " + filepath.Join(runDir, "ledger.jsonl") + " (fallback/raw context)",
		"constraints:",
		"- command is dry-run and observational only; do not spawn, edit, commit, or mutate checkpoint state while running it",
		"expected_output:",
		"- Outcome",
		"- Distillation when useful",
		"- files inspected/changed",
		"- verification commands and results",
		"- caveats or residual risk",
		"- checkpoint candidate yes/no",
		"- recommended next action",
		"suggested_spawn: none - root orchestrator should continue directly",
		"read:",
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Fatalf("step packet missing %q:\n%s", line, out)
		}
	}
}

func TestRunAmbientStepDryRunMapsReviewToReviewer(t *testing.T) {
	dir := t.TempDir()
	withChdir(t, dir)
	runsBase := filepath.Join(dir, ".tiller", "runs")
	st := fsstore.Open(runsBase)
	now := time.Now().UTC()
	runID, err := st.CreateRun(&scratch.Run{
		ID:           "20260612-130000-test",
		Task:         "step review packet test",
		Workspace:    dir,
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.AppendCheckpointCandidate(runID, scratch.CheckpointCandidate{
		ID:           "cp-001",
		Status:       scratch.CheckpointStatusFresh,
		ReportedAt:   now,
		ChangedFiles: []string{"policy/ambient.arb"},
		Verification: []string{"go test ./internal/policy"},
	}); err != nil {
		t.Fatalf("AppendCheckpointCandidate: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)
	t.Setenv("TILLER_RUN_DIR", runDir)

	out := captureAmbientStdout(t, func() {
		if err := runAmbient([]string{"step", "--dry-run"}); err != nil {
			t.Fatalf("step --dry-run: %v", err)
		}
	})

	want := []string{
		"next_action: review confidence=84 risk=high budget=ok fallback=false",
		"agent_type: tiller-reviewer",
		"profile: read-only review",
		"objective: Review policy, sandbox, or conflicting checkpoint surface and report concrete risks before mutation.",
		"- descriptor posture: read-only sandbox; inspect status.md first and do not edit, commit, or resolve checkpoints",
		`suggested_spawn: spawn_agent agent_type="tiller-reviewer" objective="Review policy, sandbox, or conflicting checkpoint surface and report concrete risks before mutation."`,
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Fatalf("review step packet missing %q:\n%s", line, out)
		}
	}
}

func TestRunAmbientStatusAppendsRunPointer(t *testing.T) {
	dir := t.TempDir()
	withChdir(t, dir)
	runsBase := filepath.Join(dir, ".tiller", "runs")
	st := fsstore.Open(runsBase)
	now := time.Now().UTC()
	runID, err := st.CreateRun(&scratch.Run{
		ID:           "20260612-110000-test",
		Task:         "status pointer test",
		Workspace:    dir,
		Status:       "running",
		ReasonBudget: 2,
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runDir := filepath.Join(runsBase, runID)
	t.Setenv("TILLER_RUN_DIR", runDir)

	out := captureAmbientStdout(t, func() {
		if err := runAmbient([]string{"status"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	want := []string{
		"tiller: ambient enabled for " + dir,
		"run: " + runID,
		"status: running",
		"next_action: proceed confidence=70 risk=low budget=ok fallback=false",
		"read: " + filepath.Join(runDir, "status.md"),
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Fatalf("status output missing %q:\n%s", line, out)
		}
	}
}

func withChdir(t *testing.T, dir string) {
	t.Helper()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})
}

func captureAmbientStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String()
}
