package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"m31labs.dev/tiller/internal/pool"
	"m31labs.dev/tiller/internal/storeutil"
)

// runPool is the handler for `tiller pool`.
//
// The pool is a host-managed singleton daemon. It polls the store for pending
// dispatches, runs them through the adapter seam, and journals deliveries for
// deduplication across restarts. One pool process per host.
func runPool(args []string) error {
	fs := flag.NewFlagSet("pool", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		pollFlag       = fs.String("poll", "5s", "polling interval between dispatch sweeps (e.g. 5s, 10s)")
		maxConcurrent  = fs.Int("max-concurrent", 4, "maximum dispatches running simultaneously")
		journalFlag    = fs.String("journal", "", "path to delivery-log JSONL file (default: .tiller/pool-journal.jsonl)")
		storeFlag      = fs.String("store", "", "storage backend: fs|pg|tee (default: TILLER_STORE env or fs)")
		dsnFlag        = fs.String("store-dsn", "", "PostgreSQL DSN for pg/tee backends (default: TILLER_STORE_DSN env)")
		policyDir      = fs.String("policy-dir", "", "project directory for policy loading (default: cwd)")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if !storeKindValid(*storeFlag) {
		return fmt.Errorf("pool: --store must be fs, pg, or tee (got %q)", *storeFlag)
	}

	pollInterval, err := time.ParseDuration(*pollFlag)
	if err != nil {
		return fmt.Errorf("pool: --poll %q: %w", *pollFlag, err)
	}
	if pollInterval <= 0 {
		return fmt.Errorf("pool: --poll must be positive")
	}

	if *maxConcurrent <= 0 {
		return fmt.Errorf("pool: --max-concurrent must be positive")
	}

	// Resolve the scratch store.
	st, _, storeCloser, err := storeutil.Resolve(&storeutil.Options{
		StoreKind: *storeFlag,
		DSN:       *dsnFlag,
	})
	if err != nil {
		return fmt.Errorf("pool: open store: %w", err)
	}
	if storeCloser != nil {
		defer storeCloser() //nolint:errcheck
	}

	// Resolve the runs base directory for WorkDir computation.
	runsBase, err := resolveRunsBase()
	if err != nil {
		return fmt.Errorf("pool: resolve runs base: %w", err)
	}

	// Resolve the journal path.
	journalPath := *journalFlag
	if journalPath == "" {
		projectDir := *policyDir
		if projectDir == "" {
			projectDir, _ = os.Getwd()
		}
		journalPath = pool.DefaultJournalPath(projectDir)
	}
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o755); err != nil {
		return fmt.Errorf("pool: create journal directory: %w", err)
	}

	projectDir := *policyDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	reg := buildRegistry("") // resolves tiller binary at Run time

	p, err := pool.New(pool.Options{
		Store:           st,
		RunsBase:        runsBase,
		AdapterRegistry: reg,
		PollInterval:    pollInterval,
		MaxConcurrent:   *maxConcurrent,
		JournalPath:     journalPath,
		ProjectDir:      projectDir,
	})
	if err != nil {
		return fmt.Errorf("pool: %w", err)
	}

	fmt.Fprintf(os.Stderr, "tiller pool: starting (poll=%s max-concurrent=%d journal=%s)\n",
		pollInterval, *maxConcurrent, journalPath)

	return p.RunWithSignals()
}

// resolveRunsBase derives the runs/ directory path from environment or cwd.
func resolveRunsBase() (string, error) {
	if runDir := os.Getenv("TILLER_RUN_DIR"); runDir != "" {
		return filepath.Dir(runDir), nil
	}
	if base := os.Getenv("TILLER_RUN_BASE"); base != "" {
		return base, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return filepath.Join(cwd, ".tiller", "runs"), nil
}
