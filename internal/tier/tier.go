// Package tier resolves model candidates for a named tiller tier.
// Tiers (reason, scrutiny, execute) each map to an ordered list of
// Candidate values. Resolve picks by bucket index (for canary bucketing).
//
// Configuration is loaded from the embedded defaults/models.toml, with an
// optional per-project override at .tiller/models.toml. Override files
// replace the tiers they define; unmentioned tiers keep the embedded defaults.
package tier

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed defaults/models.toml
var embeddedFS embed.FS

// Candidate is a resolved adapter + provider + model triple.
type Candidate struct {
	Adapter  string // e.g. "claude-headless"
	Provider string // e.g. "anthropic" or "-"
	Model    string // e.g. "sonnet" or a command name
}

// String returns the canonical "adapter:provider/model" form.
func (c Candidate) String() string {
	return c.Adapter + ":" + c.Provider + "/" + c.Model
}

// Config holds the resolved tier → candidates mapping.
type Config struct {
	tiers map[string][]Candidate
}

// Resolve returns the Candidate for the given tier name and bucket index.
// bucket is taken modulo len(candidates), enabling stable canary bucketing.
// Returns an error if the tier is unknown.
func (c *Config) Resolve(tierName string, bucket int) (Candidate, error) {
	cands, ok := c.tiers[tierName]
	if !ok {
		return Candidate{}, fmt.Errorf("tier %q not found", tierName)
	}
	idx := bucket % len(cands)
	if idx < 0 {
		idx += len(cands)
	}
	return cands[idx], nil
}

// Load parses the embedded default models.toml, then—if projectDir is non-empty
// and .tiller/models.toml exists there—parses the project file and applies its
// tier definitions on top (per-tier replacement, not candidate-list merge).
func Load(projectDir string) (*Config, error) {
	// Parse embedded defaults.
	defaultData, err := embeddedFS.ReadFile("defaults/models.toml")
	if err != nil {
		return nil, fmt.Errorf("read embedded models.toml: %w", err)
	}
	cfg, err := parse(defaultData)
	if err != nil {
		return nil, fmt.Errorf("parse embedded models.toml: %w", err)
	}

	// Apply project override if present.
	if projectDir != "" {
		overridePath := filepath.Join(projectDir, ".tiller", "models.toml")
		if data, err := os.ReadFile(overridePath); err == nil {
			override, err := parse(data)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", overridePath, err)
			}
			for name, cands := range override.tiers {
				cfg.tiers[name] = cands
			}
		}
	}

	return cfg, nil
}

// EmbeddedDefaultsFS returns the embedded defaults FS rooted at the defaults/
// subdirectory, for use by tiller init to materialize models.toml.
func EmbeddedDefaultsFS() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "defaults")
	if err != nil {
		panic("embedded tier defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}
