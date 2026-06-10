package cli

import (
	"os"

	"m31labs.dev/fablebound/internal/hook"
)

// runHook implements `fablebound hook` — the PreToolUse/PostToolUse handler.
//
// Exit codes:
//
//	0 — success (including PostToolUse always, and non-fablebound sessions)
//	2 — internal error (returned as a non-DenialError so cli.Main exits 2)
func runHook(_ []string) error {
	workspaceDir := hook.WorkspaceDir(os.Getenv("FABLEBOUND_RUN_DIR"))
	return hook.Run(os.Stdin, os.Stdout, workspaceDir)
}
