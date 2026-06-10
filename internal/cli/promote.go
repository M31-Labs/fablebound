package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/fablebound/internal/hyphae"
)

// runPromote is the handler for `fablebound promote <run-id> [--space] [--as] [--dry-run]`.
func runPromote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		space  = fs.String("space", "", "hyphae space URI (default: "+hyphae.HyphaSpace+")")
		as     = fs.String("as", "", "hyphae identity for --as passthrough")
		dryRun = fs.Bool("dry-run", false, "compose spore.md only; do not submit")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	runID := fs.Arg(0)
	if runID == "" {
		return fmt.Errorf("promote: run-id is required")
	}

	// Resolve run directory.
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("promote: getwd: %w", err)
	}

	runsBase := filepath.Join(workspace, ".fablebound", "runs")
	runDir := filepath.Join(runsBase, runID)

	if _, err := os.Stat(runDir); err != nil {
		return fmt.Errorf("promote: run %q not found: %w", runID, err)
	}

	opts := hyphae.SporeOptions{
		Space:  *space,
		As:     *as,
		DryRun: *dryRun,
	}

	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "fablebound promote: "+format+"\n", args...)
	}

	sporePath, err := hyphae.Promote(runDir, opts, log)
	if err != nil && *dryRun {
		// On dry-run, even if something went wrong after writing, print path.
		fmt.Println(sporePath)
		return err
	}
	if err != nil {
		// Non-dry-run: submit failed — print path anyway (spore.md was written).
		fmt.Println(sporePath)
		return err
	}

	fmt.Println(sporePath)
	return nil
}
