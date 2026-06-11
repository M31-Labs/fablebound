// Package tier resolves model candidates for a named tiller tier.
// Tiers (reason, scrutiny, execute) each map to an ordered list of
// Candidate values. Resolve picks by bucket index (for canary bucketing).
//
// Configuration is loaded from the embedded defaults/models.toml, with an
// optional per-project override at .tiller/models.toml. Override files
// replace the tiers they define; unmentioned tiers keep the embedded defaults.
//
// In addition to [tiers.<name>] sections, models.toml supports
// [adapter.<name>] sections for configuring command-backed adapters:
//
//	[adapter.echo-agent]
//	argv    = ["/usr/local/bin/echo-agent", "--brief", "{brief}", "--out", "{report}"]
//	report  = "stdout"           # "stdout" or a file path template
//	timeout = "5m"               # Go duration string; 0 = no timeout
//
// Ambient backends use [ambient.<name>] sections. They map provider-specific
// model identifiers observed in an interactive session to tiller's tier labels:
//
//	[ambient.claude-code]
//	detector = "claude-jsonl-transcript"
//	govern_tiers = ["reason"]
//	reason_models = ["fable", "claude-fable-5"]
package tier

import (
	"embed"
	"fmt"
	"io/fs"
	"maps"
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

// AdapterConfig holds the configuration for a command-backed adapter instance.
// It is keyed by the provider name used in a Candidate (e.g. "echo-agent" in
// "command:echo-agent/-").
type AdapterConfig struct {
	// Argv is the command and arguments to execute. {brief} and {report} are
	// placeholder tokens that are substituted at dispatch time.
	Argv []string

	// Report specifies how the adapter collects its output:
	//   "stdout" — capture the subprocess stdout
	//   anything else — read the named file after subprocess exit
	// Default is "stdout".
	Report string

	// Timeout is the maximum execution duration for the subprocess.
	// Zero means no timeout.
	Timeout string // Go duration string
}

// AmbientConfig describes how an interactive backend should be classified and
// governed. It is intentionally data-only: backend-specific transcript parsing
// lives in the adapter package, while model identity and governed tiers live in
// models.toml.
type AmbientConfig struct {
	// Detector is the backend-specific detector implementation name, e.g.
	// "claude-jsonl-transcript". Unknown detector names are ignored by callers
	// that cannot implement them.
	Detector string

	// GovernTiers lists model tiers that should be governed by ambient policy.
	// The default Claude Code config governs only reason-tier root sessions.
	GovernTiers []string

	// Models maps tier names to provider-specific model aliases observed in the
	// ambient backend's event stream.
	Models map[string][]string
}

// ModelTier maps a provider-specific model string to a tiller tier for this
// ambient backend. It returns "" for an empty model and "other" for a non-empty
// model that is not listed in the backend config.
func (a *AmbientConfig) ModelTier(model string) string {
	if model == "" {
		return ""
	}
	if a == nil {
		return "other"
	}
	for tierName, models := range a.Models {
		for _, candidate := range models {
			if model == candidate {
				return tierName
			}
		}
	}
	return "other"
}

// GovernsTier reports whether ambient policy should be applied to tierName for
// this backend.
func (a *AmbientConfig) GovernsTier(tierName string) bool {
	if a == nil || tierName == "" || tierName == "other" {
		return false
	}
	for _, governed := range a.GovernTiers {
		if governed == tierName {
			return true
		}
	}
	return false
}

// PreferredModel returns the first configured model alias for tierName. This is
// used when rendering Claude Code subagent frontmatter during install.
func (a *AmbientConfig) PreferredModel(tierName string) string {
	if a == nil {
		return ""
	}
	models := a.Models[tierName]
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

// Config holds the resolved tier → candidates mapping and adapter configs.
type Config struct {
	tiers    map[string][]Candidate
	adapters map[string]*AdapterConfig // keyed by provider name
	ambient  map[string]*AmbientConfig // keyed by ambient backend name
}

// AdapterConfig returns the named adapter configuration, or nil if no
// [adapter.<name>] section exists for that name.
func (c *Config) AdapterConfig(name string) *AdapterConfig {
	if c.adapters == nil {
		return nil
	}
	return c.adapters[name]
}

// AmbientConfig returns the named ambient backend configuration, or nil if no
// [ambient.<name>] section exists for that name.
func (c *Config) AmbientConfig(name string) *AmbientConfig {
	if c.ambient == nil {
		return nil
	}
	return c.ambient[name]
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
			maps.Copy(cfg.tiers, override.tiers)
			maps.Copy(cfg.adapters, override.adapters)
			maps.Copy(cfg.ambient, override.ambient)
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
