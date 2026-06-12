package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"m31labs.dev/tiller/internal/procutil"
)

// Meta is the per-dispatch record written to dispatches/<id>/meta.json.
type Meta struct {
	ID             string      `json:"id"`
	Parent         string      `json:"parent,omitempty"` // parent dispatch id; "" for root
	Role           string      `json:"role"`
	Model          string      `json:"model"`
	Tier           string      `json:"tier,omitempty"` // reason|scrutiny|execute; empty on v1 records
	Profile        string      `json:"profile"`        // settings/toolgate class
	Status         string      `json:"status"`         // running|completed|failed|halted|stale
	Depth          int         `json:"depth"`
	SupervisorPID  int         `json:"supervisor_pid,omitempty"` // PID of the _supervise process
	MaxTurns       int         `json:"max_turns,omitempty"`
	TimeoutMinutes int         `json:"timeout_minutes,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	EndedAt        *time.Time  `json:"ended_at,omitempty"`
	Exit           int         `json:"exit,omitempty"`
	CostUSD        float64     `json:"cost_usd,omitempty"`
	NumTurns       int         `json:"num_turns,omitempty"`
	SessionID      string      `json:"session_id,omitempty"`
	TokenUsage     *TokenUsage `json:"token_usage,omitempty"`
}

// TokenUsage is provider-neutral token accounting metadata for a dispatch.
type TokenUsage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
	TotalTokens              int64 `json:"total_tokens,omitempty"`
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
//
// Deprecated: prefer IsOrphanIn(runDir) which adds a cmdline identity check
// that prevents a recycled PID from being mistaken for a live supervisor.
// IsOrphan falls back to the kill-only check (no PID-reuse protection).
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

// IsOrphanIn returns true if this is a "running" dispatch whose supervisor
// process is no longer alive or cannot be verified as the correct supervisor.
// runDir is the absolute path to the run directory and is used to build the
// cmdline identity token "_supervise <runDir> <id>" that guards against PID
// reuse (a recycled PID whose owner is an unrelated process is treated as
// orphaned). SupervisorPID == 0 means the PID was never recorded; those
// dispatches are not treated as orphans.
func (m *Meta) IsOrphanIn(runDir string) bool {
	if m.Status != "running" {
		return false
	}
	if m.SupervisorPID <= 0 {
		return false
	}
	return !procutil.SupervisorAlive(m.SupervisorPID, runDir, m.ID)
}

// EffectiveStatus returns "stale" if the dispatch is an orphan (using the
// identity-checked liveness test), otherwise the recorded Status. This is
// used by display code to show up-to-date state without mutating the on-disk
// meta. runDir must be the absolute path to the run directory.
func (m *Meta) EffectiveStatus(runDir string) string {
	if m.IsOrphanIn(runDir) {
		return "stale"
	}
	return m.Status
}

// IsReasonTier returns true if the dispatch used the reason tier.
// Prefers the Tier field; falls back to the v1 model field (model=="fable").
func (m *Meta) IsReasonTier() bool {
	if m.Tier != "" {
		return m.Tier == "reason"
	}
	// v1 backward compat: derive from model string.
	return m.Model == "fable"
}

// IsFableModel returns true if the dispatch used a fable-class model.
// Deprecated: prefer IsReasonTier for v2 code.
func (m *Meta) IsFableModel() bool {
	return m.IsReasonTier()
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

// ReasonCount returns the number of dispatches using the reason tier.
// Checks tier=="reason" for v2 records; falls back to model=="fable" for v1.
func ReasonCount(runDir string) (int, error) {
	metas, err := ScanMetas(runDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range metas {
		if m.IsReasonTier() {
			n++
		}
	}
	return n, nil
}

// FableCount returns the number of dispatches using a fable-class model.
// Deprecated: prefer ReasonCount for v2 code.
func FableCount(runDir string) (int, error) {
	return ReasonCount(runDir)
}
