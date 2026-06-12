// Package agents provides the default ambient subagent definition files for
// tiller's ambient mode. The tiller-* persona files are installed into the
// selected backend's agents directory by `tiller install` so that the root
// reason-tier session can delegate to execution, investigation, and review
// personas automatically.
package agents

import (
	"embed"
	"io/fs"
)

//go:embed defaults/*.md codex/*.toml opencode/*.md
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

// EmbeddedCodexDefaults returns an FS view of the codex/ subtree.
// Each file is a tiller-*.toml Codex custom agent definition.
func EmbeddedCodexDefaults() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "codex")
	if err != nil {
		panic("embedded codex agent defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}

// CodexAgentFileNames returns embedded Codex custom agent filenames.
func CodexAgentFileNames() []string {
	entries, err := embeddedFS.ReadDir("codex")
	if err != nil {
		panic("embedded codex agent defaults dir unreadable: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// EmbeddedOpenCodeDefaults returns an FS view of the opencode/ subtree.
// Each file is an OpenCode markdown agent definition.
func EmbeddedOpenCodeDefaults() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "opencode")
	if err != nil {
		panic("embedded OpenCode agent defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}

// OpenCodeAgentFileNames returns embedded OpenCode custom agent filenames.
func OpenCodeAgentFileNames() []string {
	entries, err := embeddedFS.ReadDir("opencode")
	if err != nil {
		panic("embedded OpenCode agent defaults dir unreadable: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}
