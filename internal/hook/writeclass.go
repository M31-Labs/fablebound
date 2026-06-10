package hook

import "strings"

// WriteClassifyContent inspects the content of a Write or Edit *.md operation
// and returns "code-heavy" if the document is dominated by fenced code blocks,
// or "doc" otherwise.
//
// Classification thresholds (deliberately generous — real specs/plans
// legitimately carry substantial code snippets; only code-DOMINANT documents
// are blocked):
//
//	fencedLines  > codeHeavyMinLines  (absolute threshold: 50 lines)
//	fencedLines  > codeDominanceRatio (fraction of total lines: 50%)
//
// Both conditions must hold.  A file with 200 prose lines and 60 fenced lines
// is 23% code → "doc".  A file with 10 prose lines and 120 fenced lines is
// 92% code → "code-heavy".
//
// Only lines inside triple-backtick fences count as fenced; the fence
// delimiter lines themselves are not counted as content lines.
const (
	codeHeavyMinLines    = 50  // minimum fenced lines to be considered code-heavy
	codeDominanceRatio   = 0.5 // fraction of total lines that must be fenced
)

// ClassifyWrite returns "code-heavy" or "doc" for the given document content.
// Used for the Write/Edit .md path in the ambient hook to enforce the
// orchestrator's prose-first mandate.
func ClassifyWrite(content string) string {
	total, fenced := countLines(content)
	if total == 0 {
		return "doc"
	}
	if fenced > codeHeavyMinLines && float64(fenced) > codeDominanceRatio*float64(total) {
		return "code-heavy"
	}
	return "doc"
}

// countLines counts total document lines and fenced-code lines.
// "Fenced lines" are lines between triple-backtick fence pairs (not counting
// the fence delimiter lines themselves).  Nested fences are not supported —
// the first ``` after an opening fence closes it.
func countLines(content string) (total, fenced int) {
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		// A fence delimiter line starts with "```" (possibly with a language tag).
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			inFence = !inFence
			// The fence delimiter itself is not counted as a content line.
			total++
			continue
		}
		total++
		if inFence {
			fenced++
		}
	}
	return total, fenced
}
