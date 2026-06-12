// Package cli implements the tiller command-line interface.
// Exit-code contract (§9):
//
//	0 — ok (including wait-timeout with status running)
//	2 — internal error (hook fail-closed, unrecognised subcommand, flag parse error)
//	3 — policy denial (stderr = "RULE: reason")
package cli

import (
	"fmt"
	"os"

	"m31labs.dev/tiller/internal/adapter"
	"m31labs.dev/tiller/internal/adapter/claudeheadless"
	"m31labs.dev/tiller/internal/adapter/command"
	"m31labs.dev/tiller/internal/tier"
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

// buildRegistry constructs the adapter registry used by the dispatch handler.
// claude-headless and command adapters are registered; additional adapters can
// be added here as providers expand.
// binary is the tiller executable path (empty = resolve at Run time via os.Executable).
// tierCfg provides [adapter.<name>] config for the command adapter; if nil it
// is loaded from the project directory at dispatch time.
func buildRegistry(binary string, tierCfg *tier.Config) *adapter.Registry {
	reg := adapter.NewRegistry()
	reg.Register(claudeheadless.New(binary))
	if tierCfg != nil {
		reg.Register(command.New(tierCfg))
	} else {
		// Register with a nil-config command adapter; it will error at Run time
		// if actually invoked without config. This keeps the adapter name in the
		// registry for --queue dispatches and pool preview without requiring a
		// project directory at CLI startup.
		reg.Register(command.New(emptyTierConfig()))
	}
	return reg
}

// emptyTierConfig returns a zero Config that will cause the command adapter
// to return "no [adapter.*] section found" at Run time if invoked unconfigured.
func emptyTierConfig() *tier.Config {
	cfg, _ := tier.Load("") // embedded defaults only; no adapter sections
	return cfg
}

// Main is the entry point called from cmd/tiller/main.go.
func Main(args []string) {
	if len(args) < 2 {
		printUsage()
		os.Exit(2)
	}

	// Build the adapter registry for this invocation.
	// binary is resolved at Run time by claudeheadless (os.Executable).
	// Tier config is loaded from cwd (project dir); ignore errors here —
	// a misconfigured models.toml will fail at dispatch time with a clear message.
	cwd, _ := os.Getwd()
	tierCfg, _ := tier.Load(cwd)
	reg := buildRegistry("", tierCfg)

	// Subcommands that need the registry are wired here via closure; all others
	// remain as plain function references. Route resolution selects the adapter
	// by name from the registry inside makeDispatchHandler.
	subcommands := []subcommand{
		{"init", runInit},
		{"run", runRun},
		{"dispatch", makeDispatchHandler(reg)},
		{"pool", runPool},
		{"poll", runPoll},
		{"await", runAwait},
		{"note", runNote},
		{"runs", runRuns},
		{"promote", runPromote},
		{"policy", runPolicy},
		{"store", runStore},
		{"hook", runHook},
		{"ambient", runAmbient},
		{"codex", runCodex},
		{"install", runInstall},
		{"uninstall", runUninstall},
		{"_supervise", runSupervise},
		{"version", runVersion},
	}

	// Allow --version and -v as aliases for the "version" subcommand.
	sub := args[1]
	if sub == "--version" || sub == "-v" {
		sub = "version"
	}
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

	fmt.Fprintf(os.Stderr, "tiller: unknown subcommand %q\n", sub)
	printUsage()
	os.Exit(2)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: tiller <subcommand> [args]

Subcommands:
  init                   materialize .tiller/{policy,roles}, gitignore runs/
  run "<task>"           start a governed RLM run
  dispatch               dispatch a child agent (governed by dispatch.arb)
  pool                   run executor pool (host-managed daemon; drains pending dispatches)
  poll <id>              print dispatch status
  await <id>             wait for dispatch to reach terminal status
  note add [-|"text"]    append a timestamped note
  runs list|show <id>    list or inspect runs
  promote <run-id>       distill run into a hyphae spore
  policy vet             compile+typecheck all policies
  store init|status      bootstrap or inspect the PostgreSQL scratch store
  hook [--backend b]     internal: backend hook gate (stdin JSON)
  ambient disable|enable|status
                         temporarily bypass or restore ambient hook enforcement
  codex doctor           verify project-local Codex ambient install
  install [--backend b] [--print] [--project|--global]
                         install backend config and tiller-* personas; no flags prompts for project config
  uninstall [--backend b] [--print] [--project]
                         remove backend config and tiller-* personas installed by tiller
  _supervise <run> <id>  internal: detached child supervisor
  version                print version

Exit codes: 0 ok; 2 internal error; 3 policy denial
`)
}

func runVersion(_ []string) error {
	fmt.Printf("tiller %s\n", Version)
	return nil
}
