package cli

import (
	"fmt"
	"os"

	"m31labs.dev/fablebound/internal/policy"
)

// runPolicy implements `fablebound policy <subcommand>`.
func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fablebound policy vet")
	}
	switch args[0] {
	case "vet":
		return policyVet()
	default:
		return fmt.Errorf("unknown policy subcommand %q (want: vet)", args[0])
	}
}

// policyVet compiles and schema-typechecks both policies, printing their
// sha256 hashes on success. Exits 2 on any compile error (naming the file).
func policyVet() error {
	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	allOK := true
	for _, kind := range []string{"dispatch", "toolgate"} {
		loaded, err := policy.Load(kind, projectDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy vet: %v\n", err)
			allOK = false
			continue
		}
		fmt.Printf("%s  %s (%s)\n", loaded.SHA256, kind+".arb", loaded.Path)
	}
	if !allOK {
		return fmt.Errorf("policy vet failed")
	}
	return nil
}
