package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Manifest is the run-level record written to manifest.json.
type Manifest struct {
	RunID         string            `json:"run_id"`
	Task          string            `json:"task"`                // first line of task.md
	Workspace     string            `json:"workspace"`           // absolute path to workspace root
	Status        string            `json:"status"`              // created|running|completed|failed|halted
	ReasonBudget  int               `json:"reason_budget"`       // max reason-tier dispatches; default 2 (was fable_budget in v1)
	MaxDepth      int               `json:"max_depth,omitempty"` // max dispatch depth; 0 means absent -> default 2
	CreatedAt     time.Time         `json:"created_at"`
	EndedAt       *time.Time        `json:"ended_at,omitempty"`
	RootSessionID string            `json:"root_session_id,omitempty"`
	PolicySHAs    map[string]string `json:"policy_shas,omitempty"`    // kind→sha256
	HyphaTraceID  string            `json:"hypha_trace_id,omitempty"` // set if hypha trace was opened
	// Store is the store mode used by the parent `tiller run` invocation.
	// Valid values: "fs" | "pg" | "tee". Omitted (empty) means "fs" (default).
	// NEVER store the DSN here — DSN flows via TILLER_STORE_DSN env only.
	Store string `json:"store,omitempty"`
}

// DefaultMaxDepth is the default maximum dispatch depth when max_depth is absent.
const DefaultMaxDepth = 2

// manifestPath returns the path to manifest.json inside a run directory.
func manifestPath(runDir string) string {
	return filepath.Join(runDir, "manifest.json")
}

// WriteManifest writes m to runDir/manifest.json with an exclusive flock.
func WriteManifest(runDir string, m *Manifest) error {
	path := manifestPath(runDir)
	return flockWrite(path, func(f *os.File) error {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	})
}

// ReadManifest reads and parses manifest.json from runDir.
// It handles both the v2 "reason_budget" key and the legacy v1 "fable_budget" key
// (the latter is read from a raw map and promoted when reason_budget is absent/zero).
// When max_depth is absent or zero the DefaultMaxDepth is applied.
func ReadManifest(runDir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(runDir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	// Legacy fallback: if reason_budget is absent (zero) and fable_budget is present,
	// use fable_budget value. This handles v1 manifest.json files transparently.
	if m.ReasonBudget == 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err == nil {
			if v, ok := raw["fable_budget"]; ok {
				var n int
				if err := json.Unmarshal(v, &n); err == nil && n > 0 {
					m.ReasonBudget = n
				}
			}
		}
	}
	// Default max_depth when absent (zero).
	if m.MaxDepth == 0 {
		m.MaxDepth = DefaultMaxDepth
	}
	return &m, nil
}
