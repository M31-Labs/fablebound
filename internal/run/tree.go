package run

import (
	"fmt"
	"strings"
)

// Node represents a node in the dispatch tree.
type Node struct {
	Meta     *Meta
	Children []*Node
}

// BuildTree constructs the dispatch tree from all metas in runDir.
// The returned root node represents the "root" dispatch (or a synthetic
// sentinel if no root dispatch exists).  Returns an error only on I/O failures;
// a missing root dispatch is not an error.
func BuildTree(runDir string) (*Node, error) {
	metas, err := ScanMetas(runDir)
	if err != nil {
		return nil, err
	}

	// Index by id.
	byID := make(map[string]*Node, len(metas))
	for _, m := range metas {
		byID[m.ID] = &Node{Meta: m}
	}

	// Wire parent → children.
	var roots []*Node
	for _, n := range byID {
		if n.Meta.Parent == "" {
			roots = append(roots, n)
		} else if parent, ok := byID[n.Meta.Parent]; ok {
			parent.Children = append(parent.Children, n)
		} else {
			// Orphan (parent missing): treat as root.
			roots = append(roots, n)
		}
	}

	// Sort children by id for deterministic output.
	sortNodes(roots)
	for _, n := range byID {
		sortNodes(n.Children)
	}

	if len(roots) == 0 {
		// Empty run — return an empty synthetic root.
		return &Node{}, nil
	}

	// If there is exactly one root, return it directly.
	if len(roots) == 1 {
		return roots[0], nil
	}

	// Multiple roots: wrap in synthetic container.
	return &Node{Children: roots}, nil
}

// sortNodes sorts a slice of nodes by dispatch id.
func sortNodes(nodes []*Node) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodeID(nodes[j]) < nodeID(nodes[j-1]); j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
}

func nodeID(n *Node) string {
	if n.Meta != nil {
		return n.Meta.ID
	}
	return ""
}

// Render returns a human-readable text representation of the tree.
// Each line is indented proportionally to its depth.
// Example:
//
//	root orchestrator(fable) [running]
//	  └─ d01 investigator(sonnet) [completed $0.04]
//	       └─ d02 worker(sonnet) [running]
//	  └─ d03 investigator(haiku) [completed $0.02]
func Render(n *Node) string {
	var sb strings.Builder
	renderNode(&sb, n, "", true, true)
	return sb.String()
}

func renderNode(sb *strings.Builder, n *Node, prefix string, isRoot bool, isLast bool) {
	if n.Meta != nil {
		var connector string
		if isRoot {
			connector = ""
		} else {
			connector = "└─ "
		}
		label := formatMeta(n.Meta)
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
		renderNode(sb, child, childPrefix+"  ", false, last)
	}
}

func formatMeta(m *Meta) string {
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

	return fmt.Sprintf("%s %s(%s) [%s%s]", m.ID, m.Role, model, m.Status, cost)
}
