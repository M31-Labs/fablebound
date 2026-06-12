package harness

import (
	"encoding/json"
	"testing"
)

func TestHarnessConnectionValidate(t *testing.T) {
	conn := HarnessConnection{
		Backend: "codex",
		Capabilities: Capabilities{
			Interactive:     true,
			PreToolGate:     true,
			PostToolTrace:   true,
			SessionContext:  true,
			SubagentContext: true,
			NativeSpawn:     true,
			NativeWait:      true,
			NativeResume:    true,
			AgentEndSignal:  true,
			ModelDetection:  ModelDetectionPayload,
		},
	}
	if err := conn.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	data, err := json.Marshal(conn)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got HarnessConnection
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Backend != conn.Backend || got.Capabilities.ModelDetection != ModelDetectionPayload {
		t.Fatalf("roundtrip mismatch: got %#v want %#v", got, conn)
	}
}

func TestModelDetectionSourcesValidate(t *testing.T) {
	for _, source := range []ModelDetection{
		ModelDetectionNone,
		ModelDetectionTranscript,
		ModelDetectionPayload,
		ModelDetectionConfigured,
	} {
		t.Run(string(source), func(t *testing.T) {
			conn := HarnessConnection{
				Backend: "codex",
				Capabilities: Capabilities{
					ModelDetection: source,
				},
			}
			if err := conn.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestModelEvidenceValidateRoundtrip(t *testing.T) {
	evidence := ModelEvidence{
		Model:     " gpt-5.5 ",
		Effort:    " xhigh ",
		Detection: ModelDetectionPayload,
	}.Normalized()
	if err := evidence.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if evidence.Model != "gpt-5.5" || evidence.Effort != "xhigh" {
		t.Fatalf("normalized mismatch: %#v", evidence)
	}

	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ModelEvidence
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != evidence {
		t.Fatalf("roundtrip mismatch: got %#v want %#v", got, evidence)
	}
}

func TestHarnessConnectionValidateRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		conn HarnessConnection
	}{
		{
			name: "missing backend",
			conn: HarnessConnection{},
		},
		{
			name: "unknown model detection",
			conn: HarnessConnection{
				Backend: "codex",
				Capabilities: Capabilities{
					ModelDetection: "probe",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.conn.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}
}

func TestModelEvidenceValidateRejectsInvalidDetection(t *testing.T) {
	evidence := ModelEvidence{
		Model:     "gpt-5.5",
		Effort:    "xhigh",
		Detection: "probe",
	}
	if err := evidence.Validate(); err == nil {
		t.Fatal("Validate succeeded, want error")
	}
}
