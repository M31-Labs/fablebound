// Package cli implements the fablebound command-line interface.
// Exit-code contract (§9):
//
//	0 — ok (including wait-timeout with status running)
//	2 — internal error (hook fail-closed, unrecognised subcommand, flag parse error)
//	3 — policy denial (stderr = "RULE: reason")
package cli

import (
	"fmt"
	"os"
)

// DenialError wraps a policy-denial reason. Main exits with code 3.
type DenialError struct {
	Rule   string
	Reason string
}

func (e *DenialError) Error() string {
	if e.Rule != "" {
		return e.Rule + ": " + e.Reason
	}
	return e.Reason
}

// subcommand is a named handler.
type subcommand struct {
	name    string
	handler func(args []string) error
}

var subcommands = []subcommand{
	{"init", runInit},
	{"run", runRun},
	{"dispatch", runDispatch},
	{"poll", runPoll},
	{"await", runAwait},
	{"note", runNote},
	{"runs", runRuns},
	{"promote", runPromote},
	{"policy", runPolicy},
	{"hook", runHook},
	{"_supervise", runSupervise},
	{"version", runVersion},
}

// Main is the entry point called from cmd/fablebound/main.go.
func Main(args []string) {
	if len(args) < 2 {
		printUsage()
		os.Exit(2)
	}

	sub := args[1]
	for _, sc := range subcommands {
		if sc.name == sub {
			if err := sc.handler(args[2:]); err != nil {
				if de, ok := err.(*DenialError); ok {
					fmt.Fprintf(os.Stderr, "RULE: %s\n", de.Error())
					os.Exit(3)
				}
				if _, ok := err.(*StaledError); ok {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(3)
				}
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(2)
			}
			os.Exit(0)
		}
	}

	fmt.Fprintf(os.Stderr, "fablebound: unknown subcommand %q\n", sub)
	printUsage()
	os.Exit(2)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: fablebound <subcommand> [args]

Subcommands:
  init                   materialize .fablebound/{policy,roles}, gitignore runs/
  run "<task>"           start a governed RLM run
  dispatch               dispatch a child agent (governed by dispatch.arb)
  poll <id>              print dispatch status
  await <id>             wait for dispatch to reach terminal status
  note add [-|"text"]    append a timestamped note
  runs list|show <id>    list or inspect runs
  promote <run-id>       distill run into a hyphae spore
  policy vet             compile+typecheck both policies
  hook                   internal: PreToolUse/PostToolUse gate (stdin JSON)
  _supervise <run> <id>  internal: detached child supervisor
  version                print version

Exit codes: 0 ok; 2 internal error; 3 policy denial
`)
}

func runVersion(_ []string) error {
	fmt.Printf("fablebound %s\n", Version)
	return nil
}
