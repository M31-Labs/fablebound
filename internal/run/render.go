package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RenderTree renders the dispatch tree from runDir, appending
// "→ dispatches/<id>/report.md" for completed dispatches where the file exists.
// Format:
//
//	root orchestrator(fable) [completed $0.31]
//	  └─ d01 investigator(sonnet) [completed $0.04] → dispatches/d01/report.md
func RenderTree(runDir string) (string, error) {
	root, err := BuildTree(runDir)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	renderTreeNode(&sb, root, runDir, "", true, true)
	return sb.String(), nil
}

func renderTreeNode(sb *strings.Builder, n *Node, runDir, prefix string, isRoot bool, isLast bool) {
	if n.Meta != nil {
		var connector string
		if isRoot {
			connector = ""
		} else {
			connector = "└─ "
		}
		label := formatMetaWithReport(n.Meta, runDir)
		sb.WriteString(prefix + connector + label + "\n")
	}

	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix = prefix + "     "
		} else {
			childPrefix = prefix + "│    "
		}
	}

	for i, child := range n.Children {
		last := i == len(n.Children)-1
		renderTreeNode(sb, child, runDir, childPrefix+"  ", false, last)
	}
}

func formatMetaWithReport(m *Meta, runDir string) string {
	if m == nil {
		return "(unknown)"
	}

	model := m.Model
	if model == "" {
		model = "?"
	}

	var cost string
	if m.CostUSD > 0 {
		cost = fmt.Sprintf(" $%.4f", m.CostUSD)
	}

	base := fmt.Sprintf("%s %s(%s) [%s%s]", m.ID, m.Role, model, m.Status, cost)

	// Append → dispatches/<id>/report.md if the file exists.
	if runDir != "" && m.Status == "completed" {
		reportPath := filepath.Join(runDir, "dispatches", m.ID, "report.md")
		if _, err := os.Stat(reportPath); err == nil {
			// Use relative path for display.
			rel := filepath.Join("dispatches", m.ID, "report.md")
			base += " → " + rel
		}
	}

	return base
}

// RunSummary is the derived structure emitted by `runs show --json`.
type RunSummary struct {
	RunID       string            `json:"run_id"`
	Task        string            `json:"task"`
	Status      string            `json:"status"`
	CreatedAt   string            `json:"created_at,omitempty"`
	EndedAt     string            `json:"ended_at,omitempty"`
	FableBudget int               `json:"fable_budget"`
	PolicySHAs  map[string]string `json:"policy_shas,omitempty"`
	Dispatches  []*DispatchSummary `json:"dispatches"`
}

// DispatchSummary is one node in the derived dispatch tree for --json output.
type DispatchSummary struct {
	ID       string             `json:"id"`
	Parent   string             `json:"parent,omitempty"`
	Role     string             `json:"role"`
	Model    string             `json:"model"`
	Profile  string             `json:"profile,omitempty"`
	Status   string             `json:"status"`
	Depth    int                `json:"depth"`
	CostUSD  float64            `json:"cost_usd,omitempty"`
	NumTurns int                `json:"num_turns,omitempty"`
	Report   string             `json:"report,omitempty"`  // relative path if exists
	Children []*DispatchSummary `json:"children,omitempty"`
}

// BuildRunSummary builds a RunSummary from a run directory.
func BuildRunSummary(runDir string) (*RunSummary, error) {
	manifest, err := ReadManifest(runDir)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	root, err := BuildTree(runDir)
	if err != nil {
		return nil, fmt.Errorf("build tree: %w", err)
	}

	dispatches := buildDispatchSummaries(root, runDir)

	summary := &RunSummary{
		RunID:       manifest.RunID,
		Task:        manifest.Task,
		Status:      manifest.Status,
		FableBudget: manifest.FableBudget,
		PolicySHAs:  manifest.PolicySHAs,
		Dispatches:  dispatches,
	}
	if !manifest.CreatedAt.IsZero() {
		summary.CreatedAt = manifest.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if manifest.EndedAt != nil {
		summary.EndedAt = manifest.EndedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	return summary, nil
}

func buildDispatchSummaries(n *Node, runDir string) []*DispatchSummary {
	if n.Meta == nil {
		// Synthetic container — flatten to just children's summaries.
		var out []*DispatchSummary
		for _, child := range n.Children {
			out = append(out, nodeToDispatchSummary(child, runDir))
		}
		return out
	}
	return []*DispatchSummary{nodeToDispatchSummary(n, runDir)}
}

func nodeToDispatchSummary(n *Node, runDir string) *DispatchSummary {
	if n.Meta == nil {
		return &DispatchSummary{}
	}
	m := n.Meta

	ds := &DispatchSummary{
		ID:       m.ID,
		Parent:   m.Parent,
		Role:     m.Role,
		Model:    m.Model,
		Profile:  m.Profile,
		Status:   m.Status,
		Depth:    m.Depth,
		CostUSD:  m.CostUSD,
		NumTurns: m.NumTurns,
	}

	// Set report path if the file exists.
	if runDir != "" {
		reportPath := filepath.Join(runDir, "dispatches", m.ID, "report.md")
		if _, err := os.Stat(reportPath); err == nil {
			ds.Report = filepath.Join("dispatches", m.ID, "report.md")
		}
	}

	for _, child := range n.Children {
		ds.Children = append(ds.Children, nodeToDispatchSummary(child, runDir))
	}

	return ds
}

// ListItem is a row in `runs list` output.
type ListItem struct {
	RunID         string
	Status        string
	TaskFirstLine string
	DispatchCount int
	TotalCostUSD  float64
}

// ListRuns scans the runs base directory and returns summary rows.
func ListRuns(runsBase string) ([]ListItem, error) {
	entries, err := os.ReadDir(runsBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var items []ListItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(runsBase, e.Name())
		manifest, err := ReadManifest(runDir)
		if err != nil {
			// Skip unreadable runs.
			continue
		}

		taskFirstLine := firstLine(manifest.Task)

		metas, _ := ScanMetas(runDir)
		dispatchCount := len(metas)
		var totalCost float64
		for _, m := range metas {
			totalCost += m.CostUSD
		}

		items = append(items, ListItem{
			RunID:         manifest.RunID,
			Status:        manifest.Status,
			TaskFirstLine: taskFirstLine,
			DispatchCount: dispatchCount,
			TotalCostUSD:  totalCost,
		})
	}
	return items, nil
}

// FirstLine returns the first non-empty line of s (exported for cli package).
func FirstLine(s string) string {
	return firstLine(s)
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}

// MarshalJSON marshals v to indented JSON bytes.
func marshalJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
