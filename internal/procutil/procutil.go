// Package procutil provides process-identity helpers for supervisor liveness
// checks. The primary entry point is SupervisorAlive, which combines a PID
// existence check with a cmdline identity check so that a recycled PID whose
// owner is an unrelated process is not mistaken for a live supervisor.
package procutil

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CmdlineMatches reports whether the NUL-separated cmdline bytes contain the
// identity token "_supervise <runDir> <dispatchID>". The token uses a single
// space between components, matching the argv that SpawnDetached passes to
// exec.Command: exec.Command(binary, "_supervise", runDir, dispatchID).
//
// This function is pure (no I/O) and is exported so that it can be unit-tested
// directly with crafted inputs, including the PID-reuse bug case.
func CmdlineMatches(cmdlineBytes []byte, runDir, dispatchID string) bool {
	// /proc/<pid>/cmdline is NUL-separated; replace NULs with spaces to get a
	// space-joined argv string, consistent with killSupervisorProcess in cli/run.go.
	cmdline := strings.ReplaceAll(string(cmdlineBytes), "\x00", " ")
	token := "_supervise " + runDir + " " + dispatchID
	return strings.Contains(cmdline, token)
}

// SupervisorAlive returns true only when ALL three conditions hold:
//  1. pid > 0
//  2. The process is alive (kill -0 does not return ESRCH)
//  3. /proc/<pid>/cmdline contains the identity token for this supervisor
//
// Condition 3 is the fix for PID-reuse: a recycled PID whose /proc entry
// belongs to an unrelated process will fail the identity check and SupervisorAlive
// returns false (the dispatch is treated as orphaned), even though the PID
// appears alive.
//
// On non-Linux systems or when /proc is unavailable, only conditions 1 and 2
// are checked (graceful degradation to the previous kill-based behavior).
func SupervisorAlive(pid int, runDir, dispatchID string) bool {
	if pid <= 0 {
		return false
	}

	// Condition 2: process existence via kill -0.
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return false
	}

	// Condition 3: identity check via /proc (Linux only).
	cmdlinePath := filepath.Join("/proc", pidStr(pid), "cmdline")
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		// /proc unavailable (non-Linux) or transient read error:
		// fall back to kill-only behavior — process is considered alive.
		return true
	}

	return CmdlineMatches(data, runDir, dispatchID)
}

// pidStr converts a PID to its string representation without importing strconv
// to keep the dependency footprint minimal.
func pidStr(pid int) string {
	// Use fmt-free integer-to-string via the standard approach.
	if pid == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for pid > 0 {
		buf = append(buf, byte('0'+pid%10))
		pid /= 10
	}
	// Reverse.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
