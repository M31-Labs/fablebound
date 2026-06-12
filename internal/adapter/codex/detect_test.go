package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/harness"
	"m31labs.dev/tiller/internal/tier"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func testAmbient() *tier.AmbientConfig {
	return &tier.AmbientConfig{
		Detector:    "codex-jsonl-transcript",
		GovernTiers: []string{"reason"},
		Models: map[string][]string{
			"reason":  {"5.5 xhigh", "gpt-5.5 xhigh"},
			"execute": {"5.5 medium", "gpt-5.5 medium"},
		},
	}
}

func TestNormalizeModelEffort(t *testing.T) {
	got := NormalizeModelEffort("gpt-5.5", "xhigh")
	if got != "gpt-5.5 xhigh" {
		t.Fatalf("got %q, want gpt-5.5 xhigh", got)
	}

	got = NormalizeModelEffort("gpt-5.5 xhigh", "xhigh")
	if got != "gpt-5.5 xhigh" {
		t.Fatalf("must not duplicate effort, got %q", got)
	}
}

func TestDetectTierWithConfig_TranscriptEffortGoverns(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"xhigh"}}`,
	)

	tierName, ok := DetectTierWithConfig("", path, testAmbient())
	if !ok || tierName != "reason" {
		t.Fatalf("got (%q, %v), want (reason, true)", tierName, ok)
	}
}

func TestDetectTierWithConfig_EventModelTranscriptEffort(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"turn_context","payload":{"effort":"xhigh"}}`,
	)

	tierName, ok := DetectTierWithConfig("gpt-5.5", path, testAmbient())
	if !ok || tierName != "reason" {
		t.Fatalf("got (%q, %v), want (reason, true)", tierName, ok)
	}
}

func TestDetectTierWithEvidenceConfig_EventModelEffortGovernsWithoutTranscript(t *testing.T) {
	tierName, ok := DetectTierWithEvidenceConfig(harness.ModelEvidence{
		Model:     "gpt-5.5",
		Effort:    "xhigh",
		Detection: harness.ModelDetectionPayload,
	}, "", testAmbient())
	if !ok || tierName != "reason" {
		t.Fatalf("got (%q, %v), want (reason, true)", tierName, ok)
	}
}

func TestDetectTierWithEvidenceConfig_TranscriptOverridesPayload(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	)

	tierName, ok := DetectTierWithEvidenceConfig(harness.ModelEvidence{
		Model:     "gpt-5.5",
		Effort:    "xhigh",
		Detection: harness.ModelDetectionPayload,
	}, path, testAmbient())
	if ok || tierName != "" {
		t.Fatalf("got (%q, %v), want empty passthrough", tierName, ok)
	}
}

func TestDetectTierWithConfig_LatestExecutePassthrough(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"xhigh"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"medium"}}`,
	)

	tierName, ok := DetectTierWithConfig("", path, testAmbient())
	if ok || tierName != "" {
		t.Fatalf("got (%q, %v), want empty passthrough", tierName, ok)
	}
}

func TestDetectTierWithConfig_CollaborationModeEffort(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","collaboration_mode":{"settings":{"reasoning_effort":"xhigh"}}}}`,
	)

	tierName, ok := DetectTierWithConfig("", path, testAmbient())
	if !ok || tierName != "reason" {
		t.Fatalf("got (%q, %v), want (reason, true)", tierName, ok)
	}
}

func TestLatestTokenUsageUsesBoundedTail(t *testing.T) {
	lines := []string{
		`{"type":"turn_end","payload":{"usage":{"output_tokens":1}}}`,
	}
	for i := 0; i < 410; i++ {
		lines = append(lines, `{"type":"event","payload":{"i":`+strconv.Itoa(i)+`}}`)
	}
	path := writeTranscript(t, lines...)

	if usage := LatestTokenUsage(path); usage != nil {
		t.Fatalf("old usage outside tail should not be returned: %#v", usage)
	}

	lines = append(lines,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","effort":"xhigh"}}`,
		`{"type":"turn_end","payload":{"usage":{"input_tokens":22,"output_tokens":654}}}`,
	)
	path = writeTranscript(t, lines...)
	usage := LatestTokenUsage(path)
	if usage == nil {
		t.Fatal("LatestTokenUsage returned nil")
	}
	if usage.InputTokens != 22 || usage.OutputTokens != 654 {
		t.Fatalf("usage mismatch: %#v", usage)
	}
}
