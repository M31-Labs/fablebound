package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"m31labs.dev/tiller/internal/run"
)

// runRuns is the handler for `tiller runs list|show|gc`.
func runRuns(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("runs: subcommand required: list|show|gc")
	}

	switch args[0] {
	case "list":
		return runRunsList(args[1:])
	case "show":
		return runRunsShow(args[1:])
	case "gc":
		return runRunsGC(args[1:])
	default:
		return fmt.Errorf("runs: unknown subcommand %q (want list|show|gc)", args[0])
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
	items, err := run.ListRuns(runsBase)
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

	if *jsonOut {
		return runRunsShowJSON(runDir)
	}

	return runRunsShowText(runDir)
}

// runRunsShowText renders a human-readable runs show.
func runRunsShowText(runDir string) error {
	manifest, err := run.ReadManifest(runDir)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	// Manifest summary.
	fmt.Printf("run:         %s\n", manifest.RunID)
	fmt.Printf("status:      %s\n", manifest.Status)
	fmt.Printf("task:        %s\n", run.FirstLine(manifest.Task))
	fmt.Printf("workspace:   %s\n", manifest.Workspace)
	fmt.Printf("fable_budget:%d\n", manifest.FableBudget)
	if !manifest.CreatedAt.IsZero() {
		fmt.Printf("created:     %s\n", manifest.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if manifest.EndedAt != nil {
		fmt.Printf("ended:       %s\n", manifest.EndedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}

	// Policy hashes.
	if len(manifest.PolicySHAs) > 0 {
		fmt.Println("\npolicies:")
		for kind, sha := range manifest.PolicySHAs {
			fmt.Printf("  %s: %s\n", kind, sha)
		}
	}

	// Dispatch tree.
	fmt.Println("\ndispatches:")
	tree, err := run.RenderTree(runDir)
	if err != nil {
		return fmt.Errorf("render tree: %w", err)
	}
	fmt.Print(tree)

	return nil
}

// runRunsShowJSON emits the derived structure as JSON.
func runRunsShowJSON(runDir string) error {
	summary, err := run.BuildRunSummary(runDir)
	if err != nil {
		return fmt.Errorf("build summary: %w", err)
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
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
	entries, err := os.ReadDir(runsBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to gc
		}
		return fmt.Errorf("runs gc: read runs dir: %w", err)
	}

	type gcItem struct {
		runID     string
		runDir    string
		status    string
		createdAt string // sortable ISO string
	}

	var items []gcItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rd := filepath.Join(runsBase, e.Name())
		manifest, err := run.ReadManifest(rd)
		if err != nil {
			continue // skip unreadable runs
		}
		items = append(items, gcItem{
			runID:     manifest.RunID,
			runDir:    rd,
			status:    manifest.Status,
			createdAt: manifest.CreatedAt.UTC().Format("20060102-150405"),
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
