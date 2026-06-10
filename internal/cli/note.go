package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// runNote is the handler for `tiller note add [-|"text"]`.
// Writes a timestamped markdown note to notes/<utc-stamp>-<role>.md.
// Role comes from TILLER_ROLE env; "user" when outside a run.
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
	role := os.Getenv("TILLER_ROLE")
	if role == "" {
		role = "user"
	}

	// Resolve run directory and open store.
	st, runID, err := fsstore.Resolve()
	if err != nil {
		return fmt.Errorf("note add: %w", err)
	}
	if runID == "" {
		return fmt.Errorf("note add: TILLER_RUN_DIR is not set")
	}

	// Append note via the Store.
	ref, err := st.AppendNote(runID, role, []byte(text))
	if err != nil {
		return fmt.Errorf("note add: %w", err)
	}

	// Compute the full path for output.
	runDir := os.Getenv("TILLER_RUN_DIR")
	notePath := runDir + "/notes/" + ref.Filename

	fmt.Fprintf(os.Stderr, "note written: %s\n", notePath)
	fmt.Println(notePath)
	return nil
}
