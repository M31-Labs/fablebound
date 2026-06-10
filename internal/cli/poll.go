package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/fablebound/internal/run"
)

// runPoll is the handler for `fablebound poll <dispatch-id>`.
// Prints a one-liner of the dispatch status; always exits 0.
func runPoll(args []string) error {
	fs := flag.NewFlagSet("poll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("poll: usage: poll <dispatch-id>")
	}

	dispatchID := fs.Arg(0)

	runDir, err := run.CurrentRunDir()
	if err != nil {
		return fmt.Errorf("poll: %w", err)
	}

	m, err := run.ReadMeta(runDir, dispatchID)
	if err != nil {
		return fmt.Errorf("poll: read meta for %s: %w", dispatchID, err)
	}

	reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
	printMetaOneLiner(m, reportPath)
	return nil
}

// runAwait is the handler for `fablebound await <dispatch-id> [--timeout 8m]`.
// Polls until terminal status or timeout; on timeout exits 0 printing "running".
func runAwait(args []string) error {
	fs := flag.NewFlagSet("await", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeoutFlag := fs.String("timeout", "8m", "maximum time to wait (e.g. 8m, 30s)")

	// Parse flags, allowing flags after positional args.
	// Filter out positional args first so flag.Parse can see all flags.
	var positional []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			// If the flag takes a value (not -flag=value form), consume next arg.
			// For simplicity, just append the next arg too if it doesn't start with -.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		} else {
			positional = append(positional, a)
		}
	}

	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) < 1 {
		return fmt.Errorf("await: usage: await <dispatch-id> [--timeout 8m]")
	}

	dispatchID := positional[0]

	runDir, err := run.CurrentRunDir()
	if err != nil {
		return fmt.Errorf("await: %w", err)
	}

	dur, err := parseDuration(*timeoutFlag)
	if err != nil {
		dur = 8 * time.Minute
	}

	deadline := time.Now().Add(dur)
	pollInterval := 200 * time.Millisecond

	for {
		m, err := run.ReadMeta(runDir, dispatchID)
		if err == nil {
			// Check for orphan (supervisor dead) — treat as stale, exit 3.
			if m.IsOrphan() {
				reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
				fmt.Printf("%s stale %s\n", dispatchID, reportPath)
				return &StaledError{DispatchID: dispatchID}
			}
			if m.IsTerminal() {
				reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
				printMetaOneLiner(m, reportPath)
				return nil
			}
			if time.Now().After(deadline) {
				reportPath := filepath.Join(runDir, "dispatches", dispatchID, "report.md")
				printMetaOneLiner(m, reportPath)
				return nil
			}
		} else {
			if time.Now().After(deadline) {
				fmt.Printf("%s unknown (meta unreadable)\n", dispatchID)
				return nil
			}
		}

		time.Sleep(pollInterval)
	}
}

// StaledError is returned by await when a dispatch is detected as orphaned
// (supervisor PID is dead). It causes exit code 3 per the spec.
type StaledError struct {
	DispatchID string
}

func (e *StaledError) Error() string {
	return fmt.Sprintf("dispatch %s is stale: supervisor process is no longer running", e.DispatchID)
}

// printMetaOneLiner prints "<id> <status> <report-path>" to stdout.
func printMetaOneLiner(m *run.Meta, reportPath string) {
	fmt.Printf("%s %s %s\n", m.ID, m.Status, reportPath)
}
