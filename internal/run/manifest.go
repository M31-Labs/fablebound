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
	Task          string            `json:"task"`         // first line of task.md
	Workspace     string            `json:"workspace"`    // absolute path to workspace root
	Status        string            `json:"status"`       // created|running|completed|failed|halted
	FableBudget   int               `json:"fable_budget"` // max insight (fable) dispatches; default 2
	CreatedAt     time.Time         `json:"created_at"`
	EndedAt       *time.Time        `json:"ended_at,omitempty"`
	RootSessionID string            `json:"root_session_id,omitempty"`
	PolicySHAs    map[string]string `json:"policy_shas,omitempty"`    // kind→sha256
	HyphaTraceID  string            `json:"hypha_trace_id,omitempty"` // set if hypha trace was opened
}

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
func ReadManifest(runDir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(runDir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
