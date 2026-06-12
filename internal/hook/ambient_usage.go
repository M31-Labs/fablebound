package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/adapter/claudecode"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func appendClaudeAmbientUsageLedger(full HookEventFull) {
	usage := claudeAmbientTokenUsage(full)
	if usage == nil {
		return
	}
	runDir := os.Getenv("TILLER_RUN_DIR")
	if runDir == "" {
		return
	}
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	now := time.Now().UTC()
	eventID, usageRef := ambientUsageLedgerEventID("claude-code", full.TranscriptPath, usage)
	if ledgerEventExists(st, runID, eventID) {
		return
	}

	ev := scratch.LedgerEvent{
		ID:         eventID,
		Backend:    "claude-code",
		Kind:       "claude.ambient_usage",
		Status:     "observed",
		At:         now,
		TokenUsage: usage,
		Summary:    "Claude ambient root token usage observed",
		Refs:       ambientUsageRefs(full, usageRef),
	}
	_ = st.AppendLedgerEvent(runID, ev)
	refreshAmbientStatusSnapshot(runDir, now)
}

func claudeAmbientTokenUsage(full HookEventFull) *scratch.TokenUsage {
	for _, usage := range []*scratch.TokenUsage{full.TokenUsage, full.Usage} {
		if usage != nil && !usage.Empty() {
			cp := *usage
			return &cp
		}
	}
	if usage := claudecode.LatestTokenUsage(full.TranscriptPath); usage != nil {
		return usage
	}
	return nil
}

func ledgerEventExists(st scratch.Store, runID, eventID string) bool {
	events, err := st.ListLedgerEvents(runID)
	if err != nil {
		return false
	}
	for _, ev := range events {
		if ev.ID == eventID {
			return true
		}
	}
	return false
}

func ambientUsageRefs(full HookEventFull, usageRef string) []string {
	refs := []string{usageRef}
	if full.ToolName != "" {
		refs = append(refs, "tool:"+full.ToolName)
	}
	if full.TranscriptPath != "" {
		refs = append(refs, "transcript:"+full.TranscriptPath)
	}
	return refs
}

func ambientUsageLedgerEventID(backend, transcriptPath string, usage *scratch.TokenUsage) (eventID, usageRef string) {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		backend,
		transcriptPath,
		tokenUsageFingerprint(usage),
	}, "\x00")))
	hash := hex.EncodeToString(sum[:8])
	return backend + "-usage-" + hash, "usage:" + hash
}

func tokenUsageFingerprint(usage *scratch.TokenUsage) string {
	if usage == nil {
		return ""
	}
	return strings.Join([]string{
		int64Fingerprint(usage.InputTokens),
		int64Fingerprint(usage.OutputTokens),
		int64Fingerprint(usage.CacheCreationInputTokens),
		int64Fingerprint(usage.CacheReadInputTokens),
		int64Fingerprint(usage.ReasoningTokens),
		int64Fingerprint(usage.TotalTokens),
	}, ":")
}

func int64Fingerprint(v int64) string {
	return strconv.FormatInt(v, 10)
}
