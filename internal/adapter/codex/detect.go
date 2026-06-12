// Package codex provides the Codex interactive-session adapter for tiller.
//
// Backend-specific transcript parsing is confined here. Codex hook payloads
// can include model and reasoning effort, and transcripts can later refine that
// evidence. This adapter normalizes model + effort into the configured ambient
// aliases, for example "gpt-5.5 xhigh".
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"

	"m31labs.dev/tiller/internal/harness"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/tier"
)

func defaultAmbientConfig() *tier.AmbientConfig {
	cfg, err := tier.Load("")
	if err != nil {
		return nil
	}
	return cfg.AmbientConfig("codex")
}

type turnContextLine struct {
	Type    string `json:"type"`
	Payload struct {
		Model             string `json:"model"`
		Effort            string `json:"effort"`
		CollaborationMode struct {
			ReasoningEffort string `json:"reasoning_effort"`
			Settings        struct {
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaboration_mode"`
	} `json:"payload"`
}

func scanTranscriptLines(r io.Reader, fn func(line string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		fn(sc.Text())
	}
	err := sc.Err()
	if err == bufio.ErrTooLong {
		return nil
	}
	return err
}

func tailLines(f *os.File, maxLines int) ([]string, error) {
	const chunkSize = 256 * 1024
	const maxLineBytes = 4 * 1 << 20

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}

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
		linesFound += bytes.Count(buf, []byte{'\n'})
		chunks = append(chunks, chunk{buf})
	}

	totalSize := 0
	for _, c := range chunks {
		totalSize += len(c.data)
	}
	assembled := make([]byte, 0, totalSize)
	for _, chunk := range slices.Backward(chunks) {
		assembled = append(assembled, chunk.data...)
	}
	assembled = bytes.ReplaceAll(assembled, []byte("\r\n"), []byte("\n"))

	parts := bytes.Split(assembled, []byte("\n"))
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > maxLines {
		parts = parts[len(parts)-maxLines:]
	}

	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > maxLineBytes {
			continue
		}
		result = append(result, string(p))
	}
	return result, nil
}

func modelEffortFromTurn(line string) (model, effort string, ok bool) {
	var tl turnContextLine
	if err := json.Unmarshal([]byte(line), &tl); err != nil {
		return "", "", false
	}
	if tl.Type != "turn_context" {
		return "", "", false
	}
	model = strings.TrimSpace(tl.Payload.Model)
	effort = strings.TrimSpace(tl.Payload.Effort)
	if effort == "" {
		effort = strings.TrimSpace(tl.Payload.CollaborationMode.ReasoningEffort)
	}
	if effort == "" {
		effort = strings.TrimSpace(tl.Payload.CollaborationMode.Settings.ReasoningEffort)
	}
	return model, effort, model != "" || effort != ""
}

func latestModelEffort(transcriptPath string) (model, effort string, ok bool) {
	if transcriptPath == "" {
		return "", "", false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	const maxLines = 400
	tail, err := tailLines(f, maxLines)
	if err != nil {
		return "", "", false
	}
	for _, line := range slices.Backward(tail) {
		model, effort, ok := modelEffortFromTurn(line)
		if ok {
			return model, effort, true
		}
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", false
	}
	var lastModel, lastEffort string
	_ = scanTranscriptLines(f, func(line string) {
		model, effort, ok := modelEffortFromTurn(line)
		if ok {
			lastModel = model
			lastEffort = effort
		}
	})
	if lastModel == "" && lastEffort == "" {
		return "", "", false
	}
	return lastModel, lastEffort, true
}

// LatestTokenUsage returns the latest token usage object visible in the tail of
// a Codex JSONL transcript. It is intentionally best-effort and bounded to the
// same tail window used for hot-path model detection.
func LatestTokenUsage(transcriptPath string) *scratch.TokenUsage {
	if transcriptPath == "" {
		return nil
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	const maxLines = 400
	tail, err := tailLines(f, maxLines)
	if err != nil {
		return nil
	}
	for _, line := range slices.Backward(tail) {
		if usage := tokenUsageFromLine(line); usage != nil {
			return usage
		}
	}
	return nil
}

func tokenUsageFromLine(line string) *scratch.TokenUsage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}
	for _, key := range []string{"token_usage", "usage"} {
		if usage := decodeTokenUsage(raw[key]); usage != nil {
			return usage
		}
	}
	for _, key := range []string{"payload", "message"} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw[key], &nested); err != nil {
			continue
		}
		for _, usageKey := range []string{"token_usage", "usage"} {
			if usage := decodeTokenUsage(nested[usageKey]); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func decodeTokenUsage(data json.RawMessage) *scratch.TokenUsage {
	if len(data) == 0 {
		return nil
	}
	var usage scratch.TokenUsage
	if err := json.Unmarshal(data, &usage); err != nil {
		return nil
	}
	if usage.Empty() {
		return nil
	}
	return &usage
}

// NormalizeModelEffort combines a Codex model and reasoning effort into the
// alias shape used by models.toml. If effort is empty, the model is returned.
func NormalizeModelEffort(model, effort string) string {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" || effort == "" {
		return model
	}
	if strings.HasSuffix(model, " "+effort) {
		return model
	}
	return model + " " + effort
}

// DetectTier reads the Codex transcript and returns the governed tier for the
// latest root turn_context. The eventModel argument is used as a fallback when
// the hook payload knows the model but the transcript line only supplies effort.
func DetectTier(eventModel, transcriptPath string) (tierName string, ok bool) {
	return DetectTierWithConfig(eventModel, transcriptPath, defaultAmbientConfig())
}

// DetectTierWithEvidence reads Codex payload/transcript model evidence and
// returns the governed tier.
func DetectTierWithEvidence(event harness.ModelEvidence, transcriptPath string) (tierName string, ok bool) {
	return DetectTierWithEvidenceConfig(event, transcriptPath, defaultAmbientConfig())
}

// DetectTierWithConfig is DetectTier with an explicit ambient backend config.
// It returns ok=false on errors or when the latest model/effort does not map to
// a governed tier.
func DetectTierWithConfig(eventModel, transcriptPath string, ambient *tier.AmbientConfig) (tierName string, ok bool) {
	return DetectTierWithEvidenceConfig(harness.ModelEvidence{
		Model:     eventModel,
		Detection: harness.ModelDetectionPayload,
	}, transcriptPath, ambient)
}

// DetectTierWithEvidenceConfig is DetectTierWithEvidence with an explicit
// ambient backend config. Transcript turn_context evidence, when present, wins
// over payload evidence so model switches still take effect.
func DetectTierWithEvidenceConfig(event harness.ModelEvidence, transcriptPath string, ambient *tier.AmbientConfig) (tierName string, ok bool) {
	if ambient == nil {
		return "", false
	}
	event = event.Normalized()
	model := event.Model
	effort := event.Effort
	if transcriptModel, transcriptEffort, found := latestModelEffort(transcriptPath); found {
		if transcriptModel != "" {
			model = transcriptModel
		}
		effort = transcriptEffort
	}

	if normalized := NormalizeModelEffort(model, effort); normalized != "" {
		tierName = ambient.ModelTier(normalized)
		if ambient.GovernsTier(tierName) {
			return tierName, true
		}
	}

	tierName = ambient.ModelTier(model)
	if ambient.GovernsTier(tierName) {
		return tierName, true
	}
	return "", false
}
