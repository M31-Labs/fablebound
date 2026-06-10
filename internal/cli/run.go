package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/fablebound/internal/hyphae"
	"m31labs.dev/fablebound/internal/policy"
	"m31labs.dev/fablebound/internal/run"
	"m31labs.dev/fablebound/internal/spawn"
)

// runRun is the handler for `fablebound run "<task>"`.
// It creates the run scratch space, generates orchestrator settings,
// spawns the orchestrator as dispatch "root", waits for it to complete,
// then finalizes the manifest.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		fableBudget = fs.Int("fable-budget", 2, "max fable-model dispatches per run (default 2)")
		policyDir   = fs.String("policy-dir", "", "project directory override for policy loading")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	taskArgs := fs.Args()
	if len(taskArgs) == 0 {
		return fmt.Errorf("run: a task description is required (e.g. fablebound run \"my task\")")
	}
	task := strings.Join(taskArgs, " ")

	// Determine workspace and project directory.
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("run: getwd: %w", err)
	}

	projectDir := *policyDir
	if projectDir == "" {
		projectDir = workspace
	}

	// Resolve/ensure the runs directory.
	runsBase := filepath.Join(workspace, ".fablebound", "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		return fmt.Errorf("run: create runs dir: %w", err)
	}

	// Load policies early so we can record sha256s and detect misconfigs before spawning.
	dispatchLoaded, err := policy.Load("dispatch", projectDir)
	if err != nil {
		return fmt.Errorf("run: load dispatch policy: %w", err)
	}
	toolLoaded, err := policy.Load("toolgate", projectDir)
	if err != nil {
		return fmt.Errorf("run: load toolgate policy: %w", err)
	}

	// Create the run directory.
	store := run.NewStore(runsBase)
	runID, err := store.CreateRun()
	if err != nil {
		return fmt.Errorf("run: create run: %w", err)
	}
	runDir := store.RunDir(runID)

	// Write task.md.
	taskMDPath := filepath.Join(runDir, "task.md")
	if err := os.WriteFile(taskMDPath, []byte(task), 0o644); err != nil {
		return fmt.Errorf("run: write task.md: %w", err)
	}

	// Write manifest (status: running).
	now := time.Now()
	manifest := &run.Manifest{
		RunID:       runID,
		Task:        task,
		Workspace:   workspace,
		Status:      "running",
		FableBudget: *fableBudget,
		CreatedAt:   now,
		PolicySHAs: map[string]string{
			"dispatch": dispatchLoaded.SHA256,
			"toolgate": toolLoaded.SHA256,
		},
	}
	if err := run.WriteManifest(runDir, manifest); err != nil {
		return fmt.Errorf("run: write manifest: %w", err)
	}

	// Open a hypha trace (soft-fail: missing hypha must never fail the run).
	{
		hyp := hyphae.New(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "fablebound run [hypha]: "+format+"\n", args...)
		})
		phase := run.FirstLine(task)
		traceID := hyp.TraceStart(runID, phase, "")
		if traceID != "" {
			manifest.HyphaTraceID = traceID
			// Best-effort manifest update to persist trace id.
			_ = run.WriteManifest(runDir, manifest)
		}
	}

	// Create the root dispatch directory.
	rootDir, err := store.CreateDispatch(runID, "root")
	if err != nil {
		return fmt.Errorf("run: create root dispatch dir: %w", err)
	}

	// Write brief.md for root (same as task.md).
	briefPath := filepath.Join(rootDir, "brief.md")
	if err := os.WriteFile(briefPath, []byte(task), 0o644); err != nil {
		return fmt.Errorf("run: write root brief.md: %w", err)
	}

	// Generate orchestrator settings (depth 0).
	settingsJSON, err := spawn.Settings("orchestrator", 0)
	if err != nil {
		return fmt.Errorf("run: generate settings: %w", err)
	}
	settingsPath := filepath.Join(rootDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
		return fmt.Errorf("run: write settings.json: %w", err)
	}

	// Resolve role prompt path for orchestrator.
	rolePromptPath := spawn.RolePromptPath(runDir, "orchestrator")

	// Write root meta.json (status: running, parent: "").
	rootMeta := &run.Meta{
		ID:             "root",
		Parent:         "",
		Role:           "orchestrator",
		Model:          "fable",
		Profile:        "orchestrator",
		Status:         "running",
		Depth:          0,
		StartedAt:      now,
		TimeoutMinutes: 0, // no timeout for run-initiated orchestrator
	}
	if err := run.WriteMeta(runDir, rootMeta); err != nil {
		return fmt.Errorf("run: write root meta: %w", err)
	}

	// Find fablebound binary.
	fablebound, err := os.Executable()
	if err != nil {
		return fmt.Errorf("run: find executable: %w", err)
	}

	// Spawn the root dispatch via _supervise with the orchestrator args.
	// We use SpawnDetachedRoot which carries the orchestrator-specific args
	// (model=fable, settings, role prompt) while also setting the root env.
	if err := spawnRootSupervisor(fablebound, runDir, briefPath, settingsPath, rolePromptPath); err != nil {
		return fmt.Errorf("run: spawn root supervisor: %w", err)
	}

	fmt.Fprintf(os.Stderr, "run %s started (orchestrator dispatched as root)\n", runID)

	// Wait for root dispatch to reach terminal status (no cap — user-invoked).
	if err := waitForRoot(runDir); err != nil {
		return fmt.Errorf("run: wait for root: %w", err)
	}

	// Finalize manifest.
	if err := finalizeManifest(runDir, runID, *fableBudget); err != nil {
		return fmt.Errorf("run: finalize manifest: %w", err)
	}

	// Print run id and final status.
	manifest, err = run.ReadManifest(runDir)
	if err == nil {
		fmt.Printf("run %s: %s\n", runID, manifest.Status)
	}

	return nil
}

// spawnRootSupervisor starts a detached _supervise process for the root dispatch.
// Unlike regular dispatch, the root has model=fable and no dispatch policy check.
// We directly invoke _supervise which will call Supervise() reading meta.json,
// but we need to override the ClaudeArgs for the root — in particular the
// role prompt path, which Supervise() builds from RolePromptPath(runDir, "orchestrator").
// Since Supervise already handles all of this via meta.json, we just SpawnDetached.
func spawnRootSupervisor(fablebound, runDir, briefPath, settingsPath, rolePromptPath string) error {
	_ = briefPath
	_ = settingsPath
	_ = rolePromptPath
	// SpawnDetached starts `fablebound _supervise <runDir> root`.
	// Supervise() reads meta.json to get role/model/profile/depth,
	// reads brief.md and settings.json from the dispatch directory,
	// and calls RolePromptPath to find the role prompt.
	// All of these are already written above, so a standard SpawnDetached works.
	return spawn.SpawnDetached(fablebound, runDir, "root")
}

// waitForRoot polls root meta.json until terminal, with no timeout cap.
func waitForRoot(runDir string) error {
	const pollInterval = 500 * time.Millisecond
	for {
		m, err := run.ReadMeta(runDir, "root")
		if err == nil && m.IsTerminal() {
			return nil
		}
		time.Sleep(pollInterval)
	}
}

// finalizeManifest reads all dispatch metas, kills any still-running dispatches
// (grace kill), and writes the final manifest status.
func finalizeManifest(runDir, runID string, fableBudget int) error {
	metas, err := run.ScanMetas(runDir)
	if err != nil {
		return fmt.Errorf("scan metas: %w", err)
	}

	// Check for any still-running dispatches (other than root which should be terminal).
	anyRunning := false
	for _, m := range metas {
		if m.Status == "running" {
			anyRunning = true
			// Grace kill: send SIGTERM to any fablebound processes watching this run.
			// The simplest approach: mark them failed in meta directly.
			// (Actual process kill is best-effort via process group.)
			graceFail(runDir, m)
		}
	}

	// Re-read root meta for session_id.
	rootMeta, err := run.ReadMeta(runDir, "root")
	if err != nil {
		return fmt.Errorf("read root meta: %w", err)
	}

	finalStatus := "completed"
	if anyRunning || rootMeta.Status == "failed" || rootMeta.Status == "halted" {
		finalStatus = "failed"
	}

	now := time.Now()
	manifest, err := run.ReadManifest(runDir)
	if err != nil {
		// Reconstruct a minimal manifest.
		manifest = &run.Manifest{
			RunID:       runID,
			FableBudget: fableBudget,
		}
	}
	manifest.Status = finalStatus
	manifest.EndedAt = &now
	manifest.RootSessionID = rootMeta.SessionID

	if err := run.WriteManifest(runDir, manifest); err != nil {
		return err
	}

	// Close the hypha trace (soft-fail).
	if manifest.HyphaTraceID != "" {
		hyp := hyphae.New(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "fablebound run [hypha]: "+format+"\n", args...)
		})
		hyp.TraceDone(manifest.HyphaTraceID, finalStatus)
	}

	return nil
}

// graceFail marks a running dispatch as failed (best-effort).
// This is called when the orchestrator finishes while children are still running.
func graceFail(runDir string, m *run.Meta) {
	// Attempt to send SIGTERM to related processes (best effort).
	// Since we don't track supervisor PIDs, just update meta status.
	now := time.Now()
	m.Status = "failed"
	m.EndedAt = &now

	// Attempt a kill signal to any _supervise process for this dispatch
	// by searching /proc (Linux-specific, best effort).
	killSupervisorProcess(runDir, m.ID)

	_ = run.WriteMeta(runDir, m)
}

// killSupervisorProcess attempts to find and kill a fablebound _supervise
// process for the given dispatch (Linux /proc, best effort).
func killSupervisorProcess(runDir, dispatchID string) {
	procDir := "/proc"
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return
	}
	target := "_supervise " + runDir + " " + dispatchID
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cmdlineData, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		// cmdline is NUL-separated.
		cmdline := strings.ReplaceAll(string(cmdlineData), "\x00", " ")
		if strings.Contains(cmdline, target) {
			// Parse PID.
			var pid int
			if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err == nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	}
}
