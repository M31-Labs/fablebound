// Package claudecode provides the Claude Code interactive-session adapter for
// tiller (spec.tiller-provider-agnostic §2.1).
//
// This package is the ONLY place in the tiller codebase where vendor model IDs
// (fable tier model strings) are referenced — spec §2.1 confinement.  No other
// package should contain bare model-ID literals for fable/claude-fable-* models.
package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
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

// tailLines reads up to maxLines complete lines from the end of f using a
// backward-chunk strategy, avoiding a full-file scan on large transcripts.
//
// Strategy:
//   - Read backward in chunkSize blocks from EOF.
//   - Accumulate raw bytes until we have maxLines complete lines or have read
//     the whole file.
//   - Lines longer than maxLineBytes are capped (the partial suffix is dropped
//     and the line is skipped rather than returned — same fail-open semantics as
//     scanTranscriptLines).
//   - CRLF line endings are normalised to LF.
//
// Returns the tail lines in file order (oldest first, newest last), ready for
// backward scan by the caller.
func tailLines(f *os.File, maxLines int) ([]string, error) {
	const chunkSize = 256 * 1024     // 256 KB per backward read
	const maxLineBytes = 4 * 1 << 20 // 4 MiB cap — matches scanTranscriptLines

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}

	// rawBuf accumulates bytes read so far (in reverse order of chunks, but
	// within each chunk the bytes are in file order).  We prepend each chunk.
	// To avoid O(n²) prepends we collect chunks in a slice and reverse later.
	type chunk struct{ data []byte }
	var chunks []chunk
	remaining := size
	linesFound := 0

	for remaining > 0 && linesFound < maxLines {
		readSize := min(int64(chunkSize), remaining)
		offset := remaining - readSize
		buf := make([]byte, readSize)
		n, err := f.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return nil, err
		}
		buf = buf[:n]
		remaining -= int64(n)

		// Count complete newlines in this chunk to know how many lines it adds.
		// We append chunks in reverse read order; final assembly reverses them.
		linesInChunk := bytes.Count(buf, []byte{'\n'})
		linesFound += linesInChunk
		chunks = append(chunks, chunk{buf})

		// If we already have enough lines we can stop reading earlier.
		// We may overshoot slightly — that's fine, we trim below.
	}

	// Assemble bytes in file order (chunks were collected latest-first).
	totalSize := 0
	for _, c := range chunks {
		totalSize += len(c.data)
	}
	assembled := make([]byte, 0, totalSize)
	for _, chunk := range slices.Backward(chunks) {
		assembled = append(assembled, chunk.data...)
	}

	// Normalise CRLF → LF.
	assembled = bytes.ReplaceAll(assembled, []byte("\r\n"), []byte("\n"))

	// Split into lines.  bytes.Split on \n gives an empty trailing element if
	// the file ends with a newline — we trim that.
	parts := bytes.Split(assembled, []byte("\n"))
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}

	// Keep only the last maxLines entries.
	if len(parts) > maxLines {
		parts = parts[len(parts)-maxLines:]
	}

	// Convert to strings, capping pathologically long lines.
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > maxLineBytes {
			// Skip oversized line (fail-open: same as scanTranscriptLines).
			continue
		}
		result = append(result, string(p))
	}
	return result, nil
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
//   - Fix 5: tail is assembled by reading backward from EOF in 256 KB chunks,
//     avoiding a full-file scan on the hot path.
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

	// Fix 5: Read backward from EOF to build the 400-line tail without
	// scanning the whole file.
	const maxLines = 400
	tail, err := tailLines(f, maxLines)
	if err != nil {
		// tailLines error (seek failure, etc.) — fail open.
		return "", false
	}

	// Scan backwards in the tail for the last qualifying assistant line.
	for _, t := range slices.Backward(tail) {
		var tl transcriptAssistantLine
		if err := json.Unmarshal([]byte(t), &tl); err != nil {
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

// ModelTier maps a model string to its tier label.
//   - "reason" — model is in the fable (reason-tier) family
//   - ""       — model string is empty (absent / not specified)
//   - "other"  — model is non-empty but not a fable model
//
// Vendor model IDs are confined to this package (spec §2.1); callers receive
// only the opaque tier string.
func ModelTier(model string) string {
	if model == "" {
		return ""
	}
	if fableModels[model] {
		return "reason"
	}
	return "other"
}
