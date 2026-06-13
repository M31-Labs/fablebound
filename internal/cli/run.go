package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/harness"
	"m31labs.dev/tiller/internal/hyphae"
	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/procutil"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/spawn"
	"m31labs.dev/tiller/internal/storeutil"
	"m31labs.dev/tiller/internal/tier"
)

// storeKindValid reports whether k is a valid store kind name.
func storeKindValid(k string) bool {
	switch k {
	case "", "fs", "pg", "tee":
		return true
	}
	return false
}

// runRun is the handler for `tiller run "<task>"`.
// It creates the run scratch space, generates orchestrator settings,
// spawns the orchestrator as dispatch "root", waits for it to complete,
// then finalizes the manifest.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		fableBudget = fs.Int("reason-budget", 2, "max reason-tier dispatches per run (default 2)")
		maxDepth    = fs.Int("max-depth", harness.DefaultMaxDepth, "max dispatch depth (default 2)")
		policyDir   = fs.String("policy-dir", "", "project directory override for policy loading")
		storeFlag   = fs.String("store", "", "storage backend: fs|pg|tee (default: TILLER_STORE env or fs)")
		dsnFlag     = fs.String("store-dsn", "", "PostgreSQL DSN for pg/tee backends (default: TILLER_STORE_DSN env)")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if !storeKindValid(*storeFlag) {
		return fmt.Errorf("run: --store must be fs, pg, or tee (got %q)", *storeFlag)
	}

	taskArgs := fs.Args()
	if len(taskArgs) == 0 {
		return fmt.Errorf("run: a task description is required (e.g. tiller run \"my task\")")
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

	// Resolve/ensure the runs directory (needed even for non-fs stores because
	// the fs layer is always present as the authoritative copy).
	runsBase := filepath.Join(workspace, ".tiller", "runs")
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

	// Open the store using the provider-agnostic resolver.
	// --store flag → TILLER_STORE env → default fs.
	// Note: TILLER_RUN_DIR is NOT set here (this is the top-level `tiller run`
	// invocation, not a child dispatch), so the hot-path guard does not fire.
	//
	// Set TILLER_RUN_BASE so that fsstore.Resolve (called inside storeutil.Resolve)
	// picks up the right runs directory when TILLER_RUN_DIR is not set.
	if err := os.Setenv("TILLER_RUN_BASE", runsBase); err != nil {
		return fmt.Errorf("run: setenv TILLER_RUN_BASE: %w", err)
	}
	// Propagate --store and --store-dsn to child processes via env so that
	// dispatches (child invocations) inherit the same store selection.
	if *storeFlag != "" {
		if err := os.Setenv("TILLER_STORE", *storeFlag); err != nil {
			return fmt.Errorf("run: setenv TILLER_STORE: %w", err)
		}
	}
	if *dsnFlag != "" {
		if err := os.Setenv("TILLER_STORE_DSN", *dsnFlag); err != nil {
			return fmt.Errorf("run: setenv TILLER_STORE_DSN: %w", err)
		}
	}

	storeOpts := &storeutil.Options{
		StoreKind: *storeFlag,
		DSN:       *dsnFlag,
	}
	st, _, storeCloser, err := storeutil.Resolve(storeOpts)
	if err != nil {
		return fmt.Errorf("run: open store: %w", err)
	}
	if storeCloser != nil {
		defer storeCloser()
	}

	// Determine the effective store kind so it can be recorded in the manifest.
	// Children read this field to open the same store even when TILLER_RUN_DIR is set.
	effectiveStore := *storeFlag
	if effectiveStore == "" {
		effectiveStore = os.Getenv("TILLER_STORE")
	}
	if effectiveStore == "" {
		effectiveStore = "fs"
	}

	// Create the run record.
	now := time.Now()
	r := &scratch.Run{
		Task:         task,
		Workspace:    workspace,
		Status:       "running",
		ReasonBudget: *fableBudget,
		MaxDepth:     *maxDepth,
		CreatedAt:    now,
		StoreMode:    effectiveStore,
		PolicySHAs: map[string]string{
			"dispatch": dispatchLoaded.SHA256,
			"toolgate": toolLoaded.SHA256,
		},
	}
	runID, err := st.CreateRun(r)
	if err != nil {
		return fmt.Errorf("run: create run: %w", err)
	}

	// runDir is <runsBase>/<runID>; used for spawn helpers that take a path string.
	runDir := filepath.Join(runsBase, runID)

	// Write task.md (input data, not a store record).
	taskMDPath := filepath.Join(runDir, "task.md")
	if err := os.WriteFile(taskMDPath, []byte(task), 0o644); err != nil {
		return fmt.Errorf("run: write task.md: %w", err)
	}

	// Open a hypha trace (soft-fail: missing hypha must never fail the run).
	{
		hyp := hyphae.New(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "tiller run [hypha]: "+format+"\n", args...)
		})
		phase := firstLine(task)
		traceID := hyp.TraceStart(runID, phase, "")
		if traceID != "" {
			r.HyphaTraceID = traceID
			// Best-effort manifest update to persist trace id.
			_ = st.WriteRun(r)
		}
	}

	// Write brief.md for root dispatch (same as task.md).
	// WriteBrief creates the dispatch directory implicitly.
	if err := st.WriteBrief(runID, "root", []byte(task)); err != nil {
		return fmt.Errorf("run: write root brief.md: %w", err)
	}

	// Generate orchestrator settings (depth 0).
	settingsJSON, err := spawn.Settings("orchestrator", 0)
	if err != nil {
		return fmt.Errorf("run: generate settings: %w", err)
	}
	if err := st.WriteAdapterConfig(runID, "root", settingsJSON); err != nil {
		return fmt.Errorf("run: write settings.json: %w", err)
	}

	// Resolve role prompt path for orchestrator.
	rolePromptPath := spawn.RolePromptPath(runDir, "orchestrator")

	// Resolve orchestrator via tier.Resolve (orchestrator always uses reason tier).
	tierCfg, err := tier.Load(projectDir)
	if err != nil {
		return fmt.Errorf("run: load tier config: %w", err)
	}
	rootCandidate, err := tierCfg.Resolve("reason", runIDBucket(runID))
	if err != nil {
		return fmt.Errorf("run: resolve reason tier: %w", err)
	}

	// Write root dispatch record (status: running, parent: "").
	rootDispatch := &scratch.Dispatch{
		ID:             "root",
		Parent:         "",
		Role:           "orchestrator",
		Model:          rootCandidate.Model,
		Profile:        "orchestrator",
		Status:         "running",
		Depth:          0,
		StartedAt:      now,
		TimeoutMinutes: 0, // no timeout for run-initiated orchestrator
		Tier:           "reason",
		Provider:       rootCandidate.Provider,
		Adapter:        rootCandidate.Adapter,
	}
	if err := st.WriteDispatch(runID, rootDispatch); err != nil {
		return fmt.Errorf("run: write root dispatch: %w", err)
	}

	// Find tiller binary.
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("run: find executable: %w", err)
	}

	// briefPath and settingsPath are computed from runDir for the supervisor.
	briefPath := filepath.Join(runDir, "dispatches", "root", "brief.md")
	settingsPath := filepath.Join(runDir, "dispatches", "root", "settings.json")

	// Spawn the root dispatch via _supervise with the orchestrator args.
	if err := spawnRootSupervisor(binary, runDir, briefPath, settingsPath, rolePromptPath); err != nil {
		return fmt.Errorf("run: spawn root supervisor: %w", err)
	}

	fmt.Fprintf(os.Stderr, "run %s started (orchestrator dispatched as root)\n", runID)

	// Wait for root dispatch to reach terminal status (no cap — user-invoked).
	if err := waitForRoot(st, runID); err != nil {
		return fmt.Errorf("run: wait for root: %w", err)
	}

	// Finalize manifest.
	if err := finalizeManifest(st, runID, runDir, *fableBudget); err != nil {
		return fmt.Errorf("run: finalize manifest: %w", err)
	}

	// Print run id and final status.
	if runRec, err := st.ReadRun(runID); err == nil {
		fmt.Printf("run %s: %s\n", runID, runRec.Status)
	}

	return nil
}

// spawnRootSupervisor starts a detached _supervise process for the root dispatch.
// Unlike regular dispatch, the root uses the configured reason-tier model and
// no dispatch policy check.
// We directly invoke _supervise which will call Supervise() reading meta.json,
// but we need to override the ClaudeArgs for the root — in particular the
// role prompt path, which Supervise() builds from RolePromptPath(runDir, "orchestrator").
// Since Supervise already handles all of this via meta.json, we just SpawnDetached.
func spawnRootSupervisor(binary, runDir, briefPath, settingsPath, rolePromptPath string) error {
	_ = briefPath
	_ = settingsPath
	_ = rolePromptPath
	// SpawnDetached starts `tiller _supervise <runDir> root`.
	// Supervise() reads meta.json to get role/model/profile/depth,
	// reads brief.md and settings.json from the dispatch directory,
	// and calls RolePromptPath to find the role prompt.
	// All of these are already written above, so a standard SpawnDetached works.
	return spawn.SpawnDetached(binary, runDir, "root")
}

// waitForRoot polls root dispatch record until terminal, with no timeout cap.
func waitForRoot(st scratch.Store, runID string) error {
	const pollInterval = 500 * time.Millisecond
	for {
		d, err := st.ReadDispatch(runID, "root")
		if err == nil && d.IsTerminal() {
			return nil
		}
		time.Sleep(pollInterval)
	}
}

// finalizeManifest reads all dispatch records, kills any still-running dispatches
// (grace kill), and writes the final run status.
func finalizeManifest(st scratch.Store, runID, runDir string, fableBudget int) error {
	dispatches, err := st.ListDispatches(runID)
	if err != nil {
		return fmt.Errorf("list dispatches: %w", err)
	}

	// Check for any still-running dispatches (other than root which should be terminal).
	anyRunning := false
	for _, d := range dispatches {
		if d.Status == "running" {
			anyRunning = true
			if d.IsOrphanIn(runDir) {
				// Supervisor is dead (orphan): mark as stale.
				now := time.Now()
				d.Status = "stale"
				d.EndedAt = &now
				_ = st.WriteDispatch(runID, d)
			} else {
				// Grace kill: send SIGTERM to any tiller processes watching this run.
				graceFail(st, runDir, runID, d)
			}
		}
	}

	// Re-read root dispatch for session_id.
	rootDispatch, err := st.ReadDispatch(runID, "root")
	if err != nil {
		return fmt.Errorf("read root dispatch: %w", err)
	}

	finalStatus := "completed"
	if anyRunning || rootDispatch.Status == "failed" || rootDispatch.Status == "halted" {
		finalStatus = "failed"
	}

	now := time.Now()
	runRec, err := st.ReadRun(runID)
	if err != nil {
		// Reconstruct a minimal run record.
		runRec = &scratch.Run{
			ID:           runID,
			ReasonBudget: fableBudget,
		}
	}
	runRec.Status = finalStatus
	runRec.EndedAt = &now
	runRec.RootSessionID = rootDispatch.SessionID

	if err := st.WriteRun(runRec); err != nil {
		return err
	}

	// Close the hypha trace (soft-fail).
	if runRec.HyphaTraceID != "" {
		hyp := hyphae.New(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "tiller run [hypha]: "+format+"\n", args...)
		})
		hyp.TraceDone(runRec.HyphaTraceID, finalStatus)
	}

	return nil
}

// graceFail marks a running dispatch as failed (best-effort).
// This is called when the orchestrator finishes while children are still running.
func graceFail(st scratch.Store, runDir, runID string, d *scratch.Dispatch) {
	// Attempt to send SIGTERM to related processes (best effort).
	now := time.Now()
	d.Status = "failed"
	d.EndedAt = &now

	// Attempt a kill signal to any _supervise process for this dispatch
	// by searching /proc (Linux-specific, best effort).
	killSupervisorProcess(runDir, d.ID)

	_ = st.WriteDispatch(runID, d)
}

// killSupervisorProcess attempts to find and kill a tiller _supervise
// process for the given dispatch (Linux /proc, best effort).
// The cmdline identity match is delegated to procutil.CmdlineMatches so the
// matching logic is shared with the orphan-detection path.
func killSupervisorProcess(runDir, dispatchID string) {
	procDir := "/proc"
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cmdlineData, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if procutil.CmdlineMatches(cmdlineData, runDir, dispatchID) {
			// Parse PID.
			var pid int
			if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err == nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	}
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}
