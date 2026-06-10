package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/fablebound/internal/run"
)

// runNote is the handler for `fablebound note add [-|"text"]`.
// Writes a timestamped markdown note to notes/<utc-stamp>-<role>.md.
// Role comes from FABLEBOUND_ROLE env; "user" when outside a run.
func runNote(args []string) error {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Require subcommand "add".
	if fs.NArg() < 1 || fs.Arg(0) != "add" {
		return fmt.Errorf("note: usage: note add [-|\"text\"]")
	}

	var text string
	if fs.NArg() < 2 {
		return fmt.Errorf("note add: text or '-' required")
	}

	textArg := strings.Join(fs.Args()[1:], " ")
	if textArg == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("note add: read stdin: %w", err)
		}
		text = string(data)
	} else {
		text = textArg
	}

	// Role from env; "user" when outside a run.
	role := os.Getenv("FABLEBOUND_ROLE")
	if role == "" {
		role = "user"
	}

	// Resolve run directory.
	runDir, err := run.CurrentRunDir()
	if err != nil {
		return fmt.Errorf("note add: %w", err)
	}

	notesDir := filepath.Join(runDir, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return fmt.Errorf("note add: mkdir notes: %w", err)
	}

	// Filename: <utc-stamp>-<role>.md
	// Stamp format: 20060102-150405.000000000 (UTC, nanoseconds for uniqueness)
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	// Replace dots with dashes for filename safety.
	stamp = strings.ReplaceAll(stamp, ".", "-")
	filename := stamp + "-" + role + ".md"
	notePath := filepath.Join(notesDir, filename)

	if err := os.WriteFile(notePath, []byte(text), 0o644); err != nil {
		return fmt.Errorf("note add: write %s: %w", notePath, err)
	}

	fmt.Fprintf(os.Stderr, "note written: %s\n", notePath)
	fmt.Println(notePath)
	return nil
}
