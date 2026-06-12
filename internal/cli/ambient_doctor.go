package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/ambientgate"
	"m31labs.dev/tiller/internal/hook"
)

type ambientDoctor struct {
	failures int
}

func runAmbientDoctor() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	d := &ambientDoctor{}
	d.checkRuntime()
	d.checkSourceDrift(cwd)
	d.checkAmbientBypass(cwd)
	d.checkClassifierSmoke()
	d.checkHookSmoke()
	if d.failures > 0 {
		return fmt.Errorf("ambient doctor found %d failing check(s)", d.failures)
	}
	return nil
}

func (d *ambientDoctor) pass(format string, args ...any) {
	fmt.Printf("PASS "+format+"\n", args...)
}

func (d *ambientDoctor) warn(format string, args ...any) {
	fmt.Printf("WARN "+format+"\n", args...)
}

func (d *ambientDoctor) fail(format string, args ...any) {
	d.failures++
	fmt.Printf("FAIL "+format+"\n", args...)
}

func (d *ambientDoctor) checkRuntime() {
	exe, err := os.Executable()
	if err != nil {
		d.warn("ambient runtime: executable unavailable: %v; version %s", err, Version)
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	d.pass("ambient runtime: executable %s version %s", exe, Version)
}

func (d *ambientDoctor) checkSourceDrift(cwd string) {
	goMod := filepath.Join(cwd, "go.mod")
	data, err := os.ReadFile(goMod)
	if os.IsNotExist(err) {
		d.warn("ambient runtime drift: not a Tiller checkout at %s; skipping source/binary mtime check", cwd)
		return
	}
	if err != nil {
		d.warn("ambient runtime drift: read %s: %v; skipping source/binary mtime check", goMod, err)
		return
	}
	if !strings.Contains(string(data), "module m31labs.dev/tiller") {
		d.warn("ambient runtime drift: %s is not module m31labs.dev/tiller; skipping source/binary mtime check", goMod)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		d.warn("ambient runtime drift: executable unavailable: %v; skipping source/binary mtime check", err)
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		d.warn("ambient runtime drift: stat executable %s: %v; skipping source/binary mtime check", exe, err)
		return
	}

	newer := newerAmbientSources(cwd, exeInfo.ModTime().UnixNano())
	if len(newer) == 0 {
		d.pass("ambient runtime drift: executable is current with key ambient sources")
		return
	}
	d.warn("ambient runtime drift: %s newer than executable %s; run go install ./cmd/tiller or make build", strings.Join(newer, ", "), exe)
}

func newerAmbientSources(cwd string, exeModUnixNano int64) []string {
	var newer []string
	for _, rel := range []string{
		"internal/cli/ambient.go",
		"internal/cli/ambient_step.go",
		"internal/hook/cmdclass.go",
		"internal/policy/defaults/ambient.arb",
		"policy/ambient.arb",
		"cmd/tiller/main.go",
	} {
		path := filepath.Join(cwd, rel)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() > exeModUnixNano {
			newer = append(newer, rel)
		}
	}
	return newer
}

func (d *ambientDoctor) checkAmbientBypass(cwd string) {
	if ambientgate.IsDisabled(cwd) {
		d.warn("ambient bypass: enabled by .tiller/ambient.disabled or TILLER_AMBIENT_DISABLED")
		return
	}
	d.pass("ambient bypass: not active")
}

func (d *ambientDoctor) checkClassifierSmoke() {
	checks := []struct {
		name string
		ok   bool
	}{
		{"ambient control status", hook.IsAmbientControl("tiller ambient status")},
		{"ambient control next", hook.IsAmbientControl("tiller ambient next")},
		{"ambient control step dry-run", hook.IsAmbientControl("tiller ambient step --dry-run")},
		{"ambient control step without dry-run denied", !hook.IsAmbientControl("tiller ambient step")},
		{"ambient control doctor", hook.IsAmbientControl("tiller ambient doctor")},
		{"ambient control doctor extra-arg denied", !hook.IsAmbientControl("tiller ambient doctor --force")},
		{"git status readonly", hook.ClassifyCommand("git status --short") == "readonly"},
		{"lsof port diagnostics readonly", hook.ClassifyCommand("lsof -iTCP -sTCP:LISTEN -P -n") == "readonly"},
		{"ss port diagnostics readonly", hook.ClassifyCommand("ss -ltnp") == "readonly"},
		{"go build denied-classified", hook.ClassifyCommand("go build ./...") == "other"},
	}
	for _, check := range checks {
		if !check.ok {
			d.fail("classifier smoke: %s", check.name)
			continue
		}
		d.pass("classifier smoke: %s", check.name)
	}
}

func (d *ambientDoctor) checkHookSmoke() {
	transcript, cleanup, err := codexDoctorTranscript()
	if err != nil {
		d.fail("hook smoke: %v", err)
		return
	}
	defer cleanup()
	smokeWorkspace, cleanupWorkspace, err := codexDoctorSmokeWorkspace()
	if err != nil {
		d.fail("hook smoke: %v", err)
		return
	}
	defer cleanupWorkspace()
	oldDisabled, hadDisabled := os.LookupEnv("TILLER_AMBIENT_DISABLED")
	_ = os.Unsetenv("TILLER_AMBIENT_DISABLED")
	defer func() {
		if hadDisabled {
			_ = os.Setenv("TILLER_AMBIENT_DISABLED", oldDisabled)
		}
	}()

	for _, command := range []string{
		"git status --short",
		"lsof -iTCP -sTCP:LISTEN -P -n",
		"tiller ambient status",
		"tiller ambient next",
		"tiller ambient step --dry-run",
		"tiller ambient doctor",
	} {
		out, err := codexDoctorRunHook(smokeWorkspace, codexDoctorPreToolEvent(transcript, "Bash", map[string]any{"command": command}))
		if err != nil {
			d.fail("hook smoke Bash %q: %v", command, err)
			continue
		}
		if strings.TrimSpace(string(out)) != "" {
			d.fail("hook smoke Bash %q: expected silent allow, got %s", command, bytes.TrimSpace(out))
			continue
		}
		d.pass("hook smoke: PreToolUse Bash %q silent allow", command)
	}

	out, err := codexDoctorRunHook(smokeWorkspace, codexDoctorPreToolEvent(transcript, "Bash", map[string]any{"command": "tiller ambient step"}))
	if err != nil {
		d.fail("hook smoke Bash %q: %v", "tiller ambient step", err)
		return
	}
	reason := codexDoctorDecisionReason(out)
	if decision := codexDoctorDecision(out); decision != "deny" {
		d.fail("hook smoke Bash %q: expected deny, got %q", "tiller ambient step", decision)
		return
	}
	if !containsAll(reason, []string{"spawn_agent", "tiller-worker"}) {
		d.fail("hook smoke Bash %q: deny reason missing Codex delegation guidance: %q", "tiller ambient step", reason)
		return
	}
	d.pass("hook smoke: PreToolUse Bash %q denied without dry-run", "tiller ambient step")

	out, err = codexDoctorRunHook(smokeWorkspace, codexDoctorPreToolEvent(transcript, "Bash", map[string]any{"command": "go build ./..."}))
	if err != nil {
		d.fail("hook smoke Bash %q: %v", "go build ./...", err)
		return
	}
	reason = codexDoctorDecisionReason(out)
	if decision := codexDoctorDecision(out); decision != "deny" {
		d.fail("hook smoke Bash %q: expected deny, got %q", "go build ./...", decision)
		return
	}
	if !containsAll(reason, []string{"spawn_agent", "tiller-worker"}) {
		d.fail("hook smoke Bash %q: deny reason missing Codex delegation guidance: %q", "go build ./...", reason)
		return
	}
	d.pass("hook smoke: PreToolUse Bash %q Codex deny guidance", "go build ./...")
}
