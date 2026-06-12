package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodexDoctorHappyPath(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)
	if err := runInstallCodex(false, true, "/usr/local/bin/tiller hook --backend codex"); err != nil {
		t.Fatalf("runInstallCodex: %v", err)
	}

	out, err := captureStdout(func() error {
		return runCodex([]string{"doctor"})
	})
	if err != nil {
		t.Fatalf("runCodex doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"PASS codex hooks: managed PreToolUse entry present",
		"PASS codex hooks: managed SessionStart entry present",
		"PASS codex hooks: managed SubagentStart entry present",
		"PASS codex config: [features] multi_agent = true",
		"PASS codex config: [agents] max_threads = 12",
		"PASS codex config: [agents] max_depth = 2",
		"PASS codex agents: tiller-scout.toml present",
		"PASS codex agents: tiller-summary.toml present",
		"PASS codex skill using-tiller: managed snippet matches",
		"PASS codex skill using-sirena: managed snippet matches",
		"PASS hook smoke: SessionStart context",
		"PASS hook smoke: SubagentStart tiller-scout context",
		"PASS hook smoke: PreToolUse Read silent allow",
		"PASS hook smoke: PreToolUse apply_patch Codex deny guidance",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL ") {
		t.Fatalf("doctor output contains failure:\n%s", out)
	}
}

func TestRunCodexDoctorMissingInstallFails(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)

	out, err := captureStdout(func() error {
		return runCodex([]string{"doctor"})
	})
	if err == nil {
		t.Fatalf("runCodex doctor unexpectedly succeeded:\n%s", out)
	}
	for _, want := range []string{
		"FAIL codex hooks: missing",
		"FAIL codex config: missing",
		"FAIL codex agents: missing",
		"FAIL codex skill using-tiller: missing",
		"FAIL codex skill using-sirena: missing",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCodexDoctorEditedSkillWarns(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)
	if err := runInstallCodex(false, true, "/usr/local/bin/tiller hook --backend codex"); err != nil {
		t.Fatalf("runInstallCodex: %v", err)
	}
	skillPath := filepath.Join(projectDir, ".codex", "skills", "using-tiller", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(codexSkillSnippet()+"\n<!-- local edit -->\n"), 0o644); err != nil {
		t.Fatalf("write local skill edit: %v", err)
	}

	out, err := captureStdout(func() error {
		return runCodex([]string{"doctor"})
	})
	if err != nil {
		t.Fatalf("runCodex doctor should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN codex skill using-tiller: exists with local edits") {
		t.Fatalf("doctor output missing local edit warning:\n%s", out)
	}
	if strings.Contains(out, "FAIL ") {
		t.Fatalf("doctor output contains failure:\n%s", out)
	}
}

func TestRunCodexDoctorAmbientDisabledWarnsWithoutFailing(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)
	if err := runInstallCodex(false, true, "/usr/local/bin/tiller hook --backend codex"); err != nil {
		t.Fatalf("runInstallCodex: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".tiller"), 0o755); err != nil {
		t.Fatalf("mkdir .tiller: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".tiller", "ambient.disabled"), []byte("disabled\n"), 0o644); err != nil {
		t.Fatalf("write disabled marker: %v", err)
	}

	out, err := captureStdout(func() error {
		return runCodex([]string{"doctor"})
	})
	if err != nil {
		t.Fatalf("runCodex doctor should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN ambient bypass: enabled") {
		t.Fatalf("doctor output missing bypass warning:\n%s", out)
	}
	if !strings.Contains(out, "PASS hook smoke: PreToolUse apply_patch Codex deny guidance") {
		t.Fatalf("doctor smoke should still exercise active hook path:\n%s", out)
	}
	if strings.Contains(out, "FAIL ") {
		t.Fatalf("doctor output contains failure:\n%s", out)
	}
}

func TestRunCodexUsageErrors(t *testing.T) {
	if err := runCodex(nil); err == nil || !strings.Contains(err.Error(), "usage: tiller codex doctor") {
		t.Fatalf("runCodex(nil) err = %v", err)
	}
	if err := runCodex([]string{"wat"}); err == nil || !strings.Contains(err.Error(), "unknown codex subcommand") {
		t.Fatalf("runCodex(wat) err = %v", err)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	callErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)
	_ = r.Close()
	if copyErr != nil && callErr == nil {
		callErr = copyErr
	}
	return buf.String(), callErr
}
