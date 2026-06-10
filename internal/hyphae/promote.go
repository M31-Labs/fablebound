package hyphae

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/fablebound/internal/run"
)

// SporeOptions controls spore composition and submission.
type SporeOptions struct {
	Space  string // --space passthrough (empty → use default HyphaSpace)
	As     string // --as passthrough (empty → omit)
	DryRun bool   // compose only; do not submit
}

// Promote composes <runDir>/spore.md per spec §7 and optionally submits it.
// Returns the path to the composed spore.md.
func Promote(runDir string, opts SporeOptions, log Logger) (string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}

	manifest, err := run.ReadManifest(runDir)
	if err != nil {
		return "", fmt.Errorf("promote: read manifest: %w", err)
	}

	tree, err := run.BuildTree(runDir)
	if err != nil {
		return "", fmt.Errorf("promote: build tree: %w", err)
	}

	// Compose spore content per spec §7.
	var sb strings.Builder

	// --- Task ---
	sb.WriteString("## Task\n\n")
	sb.WriteString(strings.TrimSpace(manifest.Task))
	sb.WriteString("\n\n")

	// --- Outcome ---
	sb.WriteString("## Outcome\n\n")
	sb.WriteString(fmt.Sprintf("Run `%s` — status: **%s**", manifest.RunID, manifest.Status))
	if manifest.EndedAt != nil && !manifest.CreatedAt.IsZero() {
		dur := manifest.EndedAt.Sub(manifest.CreatedAt).Round(time.Second)
		sb.WriteString(fmt.Sprintf(", duration: %s", dur))
	}
	sb.WriteString("\n\n")

	// --- Dispatch tree ---
	sb.WriteString("## Dispatch Tree\n\n")
	treeText := buildTreeOneLiners(tree, runDir)
	sb.WriteString(treeText)
	sb.WriteString("\n")

	// --- Report excerpts ---
	sb.WriteString("## Report Excerpts\n\n")
	excerpts := buildReportExcerpts(tree, runDir)
	if excerpts == "" {
		sb.WriteString("_(no reports available)_\n")
	} else {
		sb.WriteString(excerpts)
	}
	sb.WriteString("\n")

	// --- Lessons (empty, operator-editable) ---
	sb.WriteString("## Lessons\n\n")

	sporePath := filepath.Join(runDir, "spore.md")
	if err := os.WriteFile(sporePath, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("promote: write spore.md: %w", err)
	}

	log("spore composed: %s", sporePath)

	if opts.DryRun {
		return sporePath, nil
	}

	// Submit via hypha.
	space := opts.Space
	if space == "" {
		space = HyphaSpace
	}
	hyp := New(log)
	out, err := hyp.SporeSubmit(sporePath, space, opts.As)
	if err != nil {
		// Soft-fail: log and return path; caller can decide.
		log("hypha spore submit failed: %v", err)
		return sporePath, fmt.Errorf("promote: spore submit: %w", err)
	}
	if out != "" {
		log("hypha spore submit: %s", out)
	}

	return sporePath, nil
}

// buildTreeOneLiners renders the dispatch tree as one-liner bullet points.
func buildTreeOneLiners(root *run.Node, runDir string) string {
	var sb strings.Builder
	writeTreeNode(&sb, root, 0, runDir)
	return sb.String()
}

func writeTreeNode(sb *strings.Builder, n *run.Node, depth int, runDir string) {
	if n.Meta != nil {
		indent := strings.Repeat("  ", depth)
		model := n.Meta.Model
		if model == "" {
			model = "?"
		}
		cost := ""
		if n.Meta.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.4f", n.Meta.CostUSD)
		}
		report := ""
		if n.Meta.Status == "completed" {
			reportPath := filepath.Join(runDir, "dispatches", n.Meta.ID, "report.md")
			if _, err := os.Stat(reportPath); err == nil {
				report = fmt.Sprintf(" → dispatches/%s/report.md", n.Meta.ID)
			}
		}
		sb.WriteString(fmt.Sprintf("%s- `%s` %s(%s) [%s%s]%s\n",
			indent, n.Meta.ID, n.Meta.Role, model, n.Meta.Status, cost, report))
	}
	for _, child := range n.Children {
		d := depth
		if n.Meta != nil {
			d = depth + 1
		}
		writeTreeNode(sb, child, d, runDir)
	}
}

// buildReportExcerpts reads report.md for each dispatch and returns a
// truncated excerpt section.
func buildReportExcerpts(root *run.Node, runDir string) string {
	const maxExcerptBytes = 512

	var sb strings.Builder
	collectExcerpts(root, runDir, maxExcerptBytes, &sb)
	return sb.String()
}

func collectExcerpts(n *run.Node, runDir string, maxBytes int, sb *strings.Builder) {
	if n.Meta != nil {
		reportPath := filepath.Join(runDir, "dispatches", n.Meta.ID, "report.md")
		data, err := os.ReadFile(reportPath)
		if err == nil && len(data) > 0 {
			excerpt := strings.TrimSpace(string(data))
			if len(excerpt) > maxBytes {
				excerpt = excerpt[:maxBytes] + "…"
			}
			sb.WriteString(fmt.Sprintf("### %s (%s)\n\n", n.Meta.ID, n.Meta.Role))
			sb.WriteString(excerpt)
			sb.WriteString("\n\n")
		}
	}
	for _, child := range n.Children {
		collectExcerpts(child, runDir, maxBytes, sb)
	}
}
