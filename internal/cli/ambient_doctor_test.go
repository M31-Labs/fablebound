package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAmbientDoctorHappyPath(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)
	t.Setenv("TILLER_AMBIENT_DISABLED", "")

	out, err := captureStdout(func() error {
		return runAmbient([]string{"doctor"})
	})
	if err != nil {
		t.Fatalf("runAmbient doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"PASS ambient runtime: executable ",
		" version " + Version,
		"PASS ambient bypass: not active",
		"PASS classifier smoke: ambient control status",
		"PASS classifier smoke: ambient control next",
		"PASS classifier smoke: ambient control step dry-run",
		"PASS classifier smoke: ambient control step without dry-run denied",
		"PASS classifier smoke: ambient control doctor",
		"PASS classifier smoke: ambient control doctor extra-arg denied",
		"PASS classifier smoke: git status readonly",
		"PASS classifier smoke: lsof port diagnostics readonly",
		"PASS classifier smoke: ss port diagnostics readonly",
		"PASS classifier smoke: go build denied-classified",
		"PASS fallback ledger smoke: write/read ok",
		`PASS hook smoke: PreToolUse Bash "git status --short" silent allow`,
		`PASS hook smoke: PreToolUse Bash "lsof -iTCP -sTCP:LISTEN -P -n" silent allow`,
		`PASS hook smoke: PreToolUse Bash "tiller ambient status" silent allow`,
		`PASS hook smoke: PreToolUse Bash "tiller ambient next" silent allow`,
		`PASS hook smoke: PreToolUse Bash "tiller ambient step --dry-run" silent allow`,
		`PASS hook smoke: PreToolUse Bash "tiller ambient doctor" silent allow`,
		`PASS hook smoke: PreToolUse Bash "tiller ambient step" denied without dry-run`,
		`PASS hook smoke: PreToolUse Bash "go build ./..." Codex deny guidance`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL ") {
		t.Fatalf("doctor output contains failure:\n%s", out)
	}
}

func TestRunAmbientDoctorBypassMarkerWarnsWithoutFailing(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir)
	t.Setenv("TILLER_AMBIENT_DISABLED", "")
	if err := os.MkdirAll(filepath.Join(projectDir, ".tiller"), 0o755); err != nil {
		t.Fatalf("mkdir .tiller: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".tiller", "ambient.disabled"), []byte("disabled\n"), 0o644); err != nil {
		t.Fatalf("write disabled marker: %v", err)
	}

	out, err := captureStdout(func() error {
		return runAmbient([]string{"doctor"})
	})
	if err != nil {
		t.Fatalf("runAmbient doctor should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN ambient bypass: enabled") {
		t.Fatalf("doctor output missing bypass warning:\n%s", out)
	}
	if strings.Contains(out, "FAIL ") {
		t.Fatalf("doctor output contains failure:\n%s", out)
	}
}

func TestRunAmbientUsageErrorsMentionDoctor(t *testing.T) {
	if err := runAmbient(nil); err == nil || !strings.Contains(err.Error(), "usage: tiller ambient disable|enable|status|next|step --dry-run|doctor") {
		t.Fatalf("runAmbient(nil) err = %v", err)
	}
	if err := runAmbient([]string{"pause"}); err == nil || !strings.Contains(err.Error(), "disable, enable, status, next, step --dry-run, or doctor") {
		t.Fatalf("runAmbient(pause) err = %v", err)
	}
}
