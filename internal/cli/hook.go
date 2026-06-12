package cli

import (
	"flag"
	"os"

	"m31labs.dev/tiller/internal/hook"
)

// runHook implements `tiller hook` — the PreToolUse/PostToolUse handler.
//
// Exit codes:
//
//	0 — success (including PostToolUse always, and non-tiller sessions)
//	2 — internal error (returned as a non-DenialError so cli.Main exits 2)
func runHook(args []string) error {
	fset := flag.NewFlagSet("hook", flag.ContinueOnError)
	backend := fset.String("backend", "claude-code", "ambient backend: claude-code, codex, or opencode")
	if err := fset.Parse(args); err != nil {
		return err
	}
	workspaceDir := hook.WorkspaceDir(os.Getenv("TILLER_RUN_DIR"))
	return hook.RunWithBackend(os.Stdin, os.Stdout, workspaceDir, *backend)
}
