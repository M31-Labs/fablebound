package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"m31labs.dev/tiller/internal/scratch/pgstore"
)

// runStore is the handler for `tiller store <subcommand>`.
func runStore(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("store: subcommand required: init|status")
	}
	switch args[0] {
	case "init":
		return runStoreInit(args[1:])
	case "status":
		return runStoreStatus(args[1:])
	default:
		return fmt.Errorf("store: unknown subcommand %q (want init|status)", args[0])
	}
}

// dsnFromFlags parses --dsn flag, falling back to TILLER_STORE_DSN env var.
func dsnFromFlags(args []string, subname string) (string, error) {
	fs := flag.NewFlagSet("store "+subname, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsn := fs.String("dsn", "", "PostgreSQL DSN (default: $TILLER_STORE_DSN)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *dsn == "" {
		*dsn = os.Getenv("TILLER_STORE_DSN")
	}
	if *dsn == "" {
		return "", fmt.Errorf("store %s: no DSN — set TILLER_STORE_DSN or pass --dsn", subname)
	}
	return *dsn, nil
}

// runStoreInit implements `tiller store init`.
// Connects to the PostgreSQL store, applies the schema idempotently,
// and prints the resulting schema version.
func runStoreInit(args []string) error {
	dsn, err := dsnFromFlags(args, "init")
	if err != nil {
		return err
	}

	ctx := context.Background()
	v, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		return fmt.Errorf("store init: %w", err)
	}
	fmt.Printf("store initialized — schema version %d\n", v)
	return nil
}

// runStoreStatus implements `tiller store status`.
// Connects and reports the schema version + row counts per table.
// Prints "uninitialized" when the schema has not yet been applied.
func runStoreStatus(args []string) error {
	dsn, err := dsnFromFlags(args, "status")
	if err != nil {
		return err
	}

	ctx := context.Background()
	st, err := pgstore.QueryStatus(ctx, dsn)
	if err != nil {
		return fmt.Errorf("store status: %w", err)
	}

	if st.Version == 0 {
		fmt.Println("store: uninitialized (run `tiller store init` to apply schema)")
		return nil
	}

	fmt.Printf("store: schema version %d\n\n", st.Version)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TABLE\tROWS")

	tables := make([]string, 0, len(st.RowCounts))
	for t := range st.RowCounts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Fprintf(tw, "%s\t%d\n", t, st.RowCounts[t])
	}
	tw.Flush()
	return nil
}
