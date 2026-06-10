// Package agents provides the default ambient subagent definition files for
// tiller's ambient mode. The tiller-* persona files are installed into
// ~/.claude/agents/ (or ./.claude/agents/ for project scope) by
// `tiller install` so that the fable orchestrator can delegate to
// cheaper models automatically via the built-in Agent/Task tool.
package agents

import (
	"embed"
	"io/fs"
)

//go:embed defaults/*.md
var embeddedFS embed.FS

// EmbeddedDefaults returns an FS view of the defaults/ subtree.
// Each file is a tiller-*.md Claude Code subagent definition.
func EmbeddedDefaults() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "defaults")
	if err != nil {
		panic("embedded agent defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}

// AgentFileNames returns the list of embedded agent definition filenames
// (without directory prefix), e.g. ["tiller-architect.md", ...].
func AgentFileNames() []string {
	entries, err := embeddedFS.ReadDir("defaults")
	if err != nil {
		panic("embedded agent defaults dir unreadable: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}
