// Package roles provides the default role prompt files for tiller agents.
// Each role is a markdown file loaded via go:embed. Projects may override
// individual roles by placing .tiller/roles/<role>.md in the project dir.
package roles

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed defaults/*.md
var embeddedFS embed.FS

// KnownRoles is the canonical list of tiller role names.
var KnownRoles = []string{
	"orchestrator",
	"chief-architect",
	"deep-report",
	"investigator",
	"worker",
	"debugger",
	"reviewer",
}

// Load returns the prompt content for the given role name. It prefers
// .tiller/roles/<role>.md in projectDir over the embedded default.
func Load(role, projectDir string) ([]byte, error) {
	if projectDir != "" {
		candidate := filepath.Join(projectDir, ".tiller", "roles", role+".md")
		if data, err := os.ReadFile(candidate); err == nil {
			return data, nil
		}
	}
	data, err := embeddedFS.ReadFile("defaults/" + role + ".md")
	if err != nil {
		return nil, fmt.Errorf("role %q: embedded default not found: %w", role, err)
	}
	return data, nil
}

// EmbeddedDefaults returns an FS view of just the defaults/ subtree.
// Used by tiller init to materialize role prompts.
func EmbeddedDefaults() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "defaults")
	if err != nil {
		panic("embedded role defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}
