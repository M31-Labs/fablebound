package cli

import (
	"fmt"

	"m31labs.dev/fablebound/internal/spawn"
)

// runSupervise is the handler for `fablebound _supervise <run-dir> <dispatch-id>`.
// This is an internal command run as a detached setsid process by `fablebound dispatch`.
func runSupervise(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("_supervise: usage: _supervise <run-dir> <dispatch-id>")
	}

	runDir := args[0]
	dispatchID := args[1]

	return spawn.Supervise(spawn.SuperviseArgs{
		RunDir:     runDir,
		DispatchID: dispatchID,
		// TimeoutMinutes is read from meta.json by Supervise().
		TimeoutMinutes: 0,
	})
}
