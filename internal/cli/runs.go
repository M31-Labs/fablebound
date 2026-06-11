package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/storeutil"
)

// runRuns is the handler for `tiller runs list|show|gc|export`.
func runRuns(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("runs: subcommand required: list|show|gc|export")
	}

	switch args[0] {
	case "list":
		return runRunsList(args[1:])
	case "show":
		return runRunsShow(args[1:])
	case "gc":
		return runRunsGC(args[1:])
	case "export":
		return runRunsExport(args[1:])
	default:
		return fmt.Errorf("runs: unknown subcommand %q (want list|show|gc|export)", args[0])
	}
}

// runRunsList implements `tiller runs list`.
func runRunsList(args []string) error {
	fs := flag.NewFlagSet("runs list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("runs list: getwd: %w", err)
	}

	runsBase := filepath.Join(workspace, ".tiller", "runs")
	st := fsstore.Open(runsBase)

	items, err := st.ListRuns()
	if err != nil {
		return fmt.Errorf("runs list: %w", err)
	}

	if len(items) == 0 {
		fmt.Println("(no runs)")
		return nil
	}

	// Print as aligned table: id | status | task (first line) | dispatches | Σcost
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tTASK\tDISPATCHES\tΣCOST")
	fmt.Fprintln(w, "--\t------\t----\t----------\t-----")
	for _, item := range items {
		task := item.TaskFirstLine
		if len(task) > 60 {
			task = task[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t$%.4f\n",
			item.RunID, item.Status, task, item.DispatchCount, item.TotalCostUSD)
	}
	return w.Flush()
}

// runRunsShow implements `tiller runs show <id> [--json]`.
// --json may appear before or after the run id.
func runRunsShow(args []string) error {
	fs := flag.NewFlagSet("runs show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var jsonOut = fs.Bool("json", false, "emit JSON instead of human-readable output")

	// Filter out the run id (non-flag arg) and re-parse so --json works anywhere.
	var filteredArgs []string
	var id string
	for _, a := range args {
		if a == "--json" || a == "-json" {
			filteredArgs = append(filteredArgs, a)
		} else if len(a) > 0 && a[0] != '-' && id == "" {
			id = a
		} else {
			filteredArgs = append(filteredArgs, a)
		}
	}

	if err := fs.Parse(filteredArgs); err != nil {
		return err
	}

	if id == "" {
		return fmt.Errorf("runs show: run id required")
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("runs show: getwd: %w", err)
	}

	runsBase := filepath.Join(workspace, ".tiller", "runs")
	runDir := filepath.Join(runsBase, id)

	if _, err := os.Stat(runDir); err != nil {
		return fmt.Errorf("runs show: run %q not found: %w", id, err)
	}

	st := fsstore.Open(runsBase)

	if *jsonOut {
		return runRunsShowJSON(st, id)
	}

	return runRunsShowText(st, id)
}

// runRunsShowText renders a human-readable runs show.
func runRunsShowText(st scratch.Store, id string) error {
	runRec, err := st.ReadRun(id)
	if err != nil {
		return fmt.Errorf("read run: %w", err)
	}

	// Manifest summary.
	fmt.Printf("run:         %s\n", runRec.ID)
	fmt.Printf("status:      %s\n", runRec.Status)
	fmt.Printf("task:        %s\n", firstLine(runRec.Task))
	fmt.Printf("workspace:   %s\n", runRec.Workspace)
	fmt.Printf("reason_budget:%d\n", runRec.ReasonBudget)
	if !runRec.CreatedAt.IsZero() {
		fmt.Printf("created:     %s\n", runRec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if runRec.EndedAt != nil {
		fmt.Printf("ended:       %s\n", runRec.EndedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}

	// Policy hashes.
	if len(runRec.PolicySHAs) > 0 {
		fmt.Println("\npolicies:")
		for kind, sha := range runRec.PolicySHAs {
			fmt.Printf("  %s: %s\n", kind, sha)
		}
	}

	// Dispatch tree via the Store.
	fmt.Println("\ndispatches:")
	tree, err := st.RenderTree(id)
	if err != nil {
		return fmt.Errorf("render tree: %w", err)
	}
	fmt.Print(tree)

	return nil
}

// runRunsShowJSON emits the derived structure as JSON.
func runRunsShowJSON(st scratch.Store, id string) error {
	data, err := st.BuildRunSummaryJSON(id)
	if err != nil {
		return fmt.Errorf("build summary: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// runRunsGC implements `tiller runs gc --keep N [--dry-run]`.
// It deletes the oldest TERMINAL runs beyond the N most-recent ones.
// Running runs are never deleted. --dry-run lists victims only.
func runRunsGC(args []string) error {
	fs := flag.NewFlagSet("runs gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		keep   = fs.Int("keep", 20, "number of most-recent runs to keep")
		dryRun = fs.Bool("dry-run", false, "list victims without deleting")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *keep < 0 {
		return fmt.Errorf("runs gc: --keep must be >= 0")
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("runs gc: getwd: %w", err)
	}

	runsBase := filepath.Join(workspace, ".tiller", "runs")
	st := fsstore.Open(runsBase)

	type gcItem struct {
		runID     string
		runDir    string
		status    string
		createdAt string // sortable ISO string
	}

	// List all runs then fetch each for full record (createdAt for sort, status for filter).
	listItems, err := st.ListRuns()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to gc
		}
		return fmt.Errorf("runs gc: list runs: %w", err)
	}

	var items []gcItem
	for _, li := range listItems {
		rd := filepath.Join(runsBase, li.RunID)
		runRec, err := st.ReadRun(li.RunID)
		if err != nil {
			continue // skip unreadable runs
		}
		items = append(items, gcItem{
			runID:     li.RunID,
			runDir:    rd,
			status:    runRec.Status,
			createdAt: runRec.CreatedAt.UTC().Format("20060102-150405"),
		})
	}

	// Sort by createdAt ascending (oldest first).
	sort.Slice(items, func(i, j int) bool {
		return items[i].createdAt < items[j].createdAt
	})

	// Separate terminal from running runs.
	var terminal []gcItem
	for _, it := range items {
		switch it.status {
		case "completed", "failed", "halted":
			terminal = append(terminal, it)
			// "running" and anything else: keep always
		}
	}

	// Determine victims: oldest terminal runs beyond keep count.
	if len(terminal) <= *keep {
		if *dryRun {
			fmt.Println("(no runs to gc)")
		}
		return nil
	}

	victims := terminal[:len(terminal)-*keep]

	for _, v := range victims {
		if *dryRun {
			fmt.Printf("would delete: %s (%s)\n", v.runID, v.status)
			continue
		}
		if err := os.RemoveAll(v.runDir); err != nil {
			fmt.Fprintf(os.Stderr, "runs gc: remove %s: %v\n", v.runID, err)
		} else {
			fmt.Printf("deleted: %s\n", v.runID)
		}
	}

	return nil
}

// runRunsExport implements `tiller runs export <id> --dir <d> [--store pg] [--store-dsn DSN]`.
//
// Reads the run from the resolved store (typically pgstore) and materialises
// the v1 file layout in --dir so that file-based tools (arbiter replay, etc.)
// keep working verbatim.
func runRunsExport(args []string) error {
	fs := flag.NewFlagSet("runs export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dir      = fs.String("dir", "", "destination directory (required)")
		storeKnd = fs.String("store", "", "storage backend: fs|pg|tee (default: TILLER_STORE env or fs)")
		dsnFlag  = fs.String("store-dsn", "", "PostgreSQL DSN (default: TILLER_STORE_DSN env)")
	)

	// Separate the run id positional arg from flag args.
	var filteredArgs []string
	var id string
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' && id == "" {
			id = a
		} else {
			filteredArgs = append(filteredArgs, a)
		}
	}
	if err := fs.Parse(filteredArgs); err != nil {
		return err
	}

	if id == "" {
		return fmt.Errorf("runs export: run id required")
	}
	if *dir == "" {
		return fmt.Errorf("runs export: --dir is required")
	}

	// Resolve the store.
	// Export is a top-level CLI command; TILLER_RUN_DIR is not set, so the
	// hot-path guard does not fire and TILLER_STORE / --store is honoured.
	opts := &storeutil.Options{
		StoreKind: *storeKnd,
		DSN:       *dsnFlag,
	}
	st, _, closer, err := storeutil.Resolve(opts)
	if err != nil {
		return fmt.Errorf("runs export: open store: %w", err)
	}
	if closer != nil {
		defer closer()
	}

	if err := scratch.ExportRun(st, id, *dir); err != nil {
		return fmt.Errorf("runs export: %w", err)
	}

	fmt.Printf("exported %s → %s\n", id, *dir)
	return nil
}
