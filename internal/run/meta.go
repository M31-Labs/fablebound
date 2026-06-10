package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Meta is the per-dispatch record written to dispatches/<id>/meta.json.
type Meta struct {
	ID             string     `json:"id"`
	Parent         string     `json:"parent,omitempty"` // parent dispatch id; "" for root
	Role           string     `json:"role"`
	Model          string     `json:"model"`
	Profile        string     `json:"profile"` // settings/toolgate class
	Status         string     `json:"status"`  // running|completed|failed|halted|stale
	Depth          int        `json:"depth"`
	SupervisorPID  int        `json:"supervisor_pid,omitempty"` // PID of the _supervise process
	MaxTurns       int        `json:"max_turns,omitempty"`
	TimeoutMinutes int        `json:"timeout_minutes,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	Exit           int        `json:"exit,omitempty"`
	CostUSD        float64    `json:"cost_usd,omitempty"`
	NumTurns       int        `json:"num_turns,omitempty"`
	SessionID      string     `json:"session_id,omitempty"`
}

// IsTerminal returns true if the meta status is a terminal state.
func (m *Meta) IsTerminal() bool {
	switch m.Status {
	case "completed", "failed", "halted", "stale":
		return true
	}
	return false
}

// IsOrphan returns true if this is a "running" dispatch whose supervisor
// process no longer exists. SupervisorPID == 0 means the PID was never
// recorded (older dispatches); those are not treated as orphans.
func (m *Meta) IsOrphan() bool {
	if m.Status != "running" {
		return false
	}
	if m.SupervisorPID <= 0 {
		return false
	}
	// kill -0 checks whether the process exists without sending a signal.
	err := syscall.Kill(m.SupervisorPID, 0)
	// ESRCH = no such process → orphan.
	return err == syscall.ESRCH
}

// EffectiveStatus returns "stale" if the dispatch is an orphan, otherwise
// the recorded Status.  This is used by display code to show up-to-date state
// without mutating the on-disk meta.
func (m *Meta) EffectiveStatus() string {
	if m.IsOrphan() {
		return "stale"
	}
	return m.Status
}

// IsFableModel returns true if the dispatch used a fable-class model.
func (m *Meta) IsFableModel() bool {
	return m.Model == "fable"
}

// metaPath returns the path to meta.json for a dispatch inside a run directory.
func metaPath(runDir, dispatchID string) string {
	return filepath.Join(runDir, "dispatches", dispatchID, "meta.json")
}

// WriteMeta writes meta to dispatches/<dispatchID>/meta.json with an exclusive
// flock.  The dispatch directory must already exist.
func WriteMeta(runDir string, meta *Meta) error {
	path := metaPath(runDir, meta.ID)
	return flockWrite(path, func(f *os.File) error {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(meta)
	})
}

// ReadMeta reads and parses dispatches/<dispatchID>/meta.json from runDir.
func ReadMeta(runDir, dispatchID string) (*Meta, error) {
	data, err := os.ReadFile(metaPath(runDir, dispatchID))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ScanMetas reads all dispatch meta.json files from a run directory and
// returns a slice of successfully parsed metas (skips missing/corrupt files).
func ScanMetas(runDir string) ([]*Meta, error) {
	dispDir := filepath.Join(runDir, "dispatches")
	entries, err := os.ReadDir(dispDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var metas []*Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := ReadMeta(runDir, e.Name())
		if err != nil {
			// Skip unreadable/corrupt metas (partial write in progress).
			continue
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// ActiveCount returns the number of running dispatches in the run.
func ActiveCount(runDir string) (int, error) {
	metas, err := ScanMetas(runDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range metas {
		if m.Status == "running" {
			n++
		}
	}
	return n, nil
}

// FableCount returns the number of dispatches using a fable-class model.
func FableCount(runDir string) (int, error) {
	metas, err := ScanMetas(runDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range metas {
		if m.IsFableModel() {
			n++
		}
	}
	return n, nil
}
