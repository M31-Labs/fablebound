// Package claudecode provides the Claude Code interactive-session adapter for
// tiller (spec.tiller-provider-agnostic §2.1).
//
// This package is the ONLY place in the tiller codebase where vendor model IDs
// (fable tier model strings) are referenced — spec §2.1 confinement.  No other
// package should contain bare model-ID literals for fable/claude-fable-* models.
package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// fableModels is the set of model IDs that identify the fable (reason-tier)
// model family.  THIS MAP IS THE SINGLE CANONICAL LOCATION for fable model IDs
// in the tiller codebase — spec §2.1 confinement.
var fableModels = map[string]bool{
	"claude-fable-5": true,
	"fable":          true,
}

// transcriptAssistantLine is the minimal struct we need from a transcript line.
type transcriptAssistantLine struct {
	Type        string `json:"type"`
	IsSidechain *bool  `json:"isSidechain"`
	AgentID     string `json:"agentId"`
	Message     struct {
		Model string `json:"model"`
	} `json:"message"`
}

// isQualifyingAssistantLine returns true if the parsed line counts as a
// qualifying root-session assistant line for model detection.
//
// Qualifying conditions (ALL must hold):
//  1. type == "assistant"
//  2. message.model is non-empty
//  3. message.model != "<synthetic>" (session-limit / interrupted turns)
//  4. isSidechain is absent (nil) or false (root session line)
func isQualifyingAssistantLine(tl transcriptAssistantLine) bool {
	if tl.Type != "assistant" {
		return false
	}
	if tl.Message.Model == "" || tl.Message.Model == "<synthetic>" {
		return false
	}
	if tl.IsSidechain != nil && *tl.IsSidechain {
		return false
	}
	return true
}

// scanTranscriptLines scans r line-by-line with a 4 MiB buffer, calling fn
// for each raw line text. If a single line exceeds the buffer cap it is
// skipped (fn is not called for it) but scanning continues. Returns the
// scanner error if the failure was NOT a token-too-large error.
func scanTranscriptLines(r io.Reader, fn func(line string)) error {
	sc := bufio.NewScanner(r)
	// Enlarge buffer to 4 MiB so large tool_use/tool_result lines don't
	// trigger sc.Err() and cause a fail-open on the whole scan.
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		fn(sc.Text())
	}
	err := sc.Err()
	if err == bufio.ErrTooLong {
		// Single line exceeded the 4 MiB cap — treat as a skipped line;
		// the caller gets no error so it can continue or retry from start.
		return nil
	}
	return err
}

// DetectTier reads up to the last 400 lines of the transcript at
// transcriptPath and returns the tier and whether detection succeeded.
//
// Returns ("reason", true) when the last qualifying root assistant turn is a
// fable model — the caller should apply reason-tier (ambient orchestrator)
// policy.  Returns ("", false) on any error, empty file, or when the last
// qualifying turn is a non-fable model (fail open).
//
// Hardening applied:
//   - Fix 1: skips lines where message.model == "<synthetic>".
//   - Fix 2: skips lines where isSidechain == true.
//   - Fix 3: uses a 4 MiB scanner buffer; oversized lines are skipped, not fatal.
//   - Fix 4: if the 400-line tail contains no qualifying line, falls back to a
//     full-file scan before returning unknown.
//
// On any error or no qualifier found returns ("", false) — fail open.
func DetectTier(transcriptPath string) (tier string, ok bool) {
	if transcriptPath == "" {
		return "", false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", false
	}
	defer f.Close()

	// Fix 3 + Fix 4: Collect up to the last 400 lines using the enlarged scanner.
	const maxLines = 400
	tail := make([]string, 0, maxLines)
	_ = scanTranscriptLines(f, func(line string) {
		if len(tail) >= maxLines {
			tail = tail[1:]
		}
		tail = append(tail, line)
	})

	// Scan backwards in the tail for the last qualifying assistant line.
	for i := len(tail) - 1; i >= 0; i-- {
		var tl transcriptAssistantLine
		if err := json.Unmarshal([]byte(tail[i]), &tl); err != nil {
			continue
		}
		if isQualifyingAssistantLine(tl) {
			m := tl.Message.Model
			if fableModels[m] {
				return "reason", true
			}
			return "", false
		}
	}

	// Fix 4 fallback: tail had no qualifying line — re-scan the whole file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", false
	}
	var lastModel string
	_ = scanTranscriptLines(f, func(line string) {
		var tl transcriptAssistantLine
		if err := json.Unmarshal([]byte(line), &tl); err != nil {
			return
		}
		if isQualifyingAssistantLine(tl) {
			lastModel = tl.Message.Model
		}
	})
	if lastModel == "" {
		return "", false
	}
	if fableModels[lastModel] {
		return "reason", true
	}
	return "", false
}

// IsFableModel reports whether model is a fable-tier model ID.
// Exported so hook.go can remain free of model-ID literals while still
// making the old lastFableModelInTranscript compatible during the migration.
func IsFableModel(model string) bool {
	return fableModels[model]
}
