// Package harness describes backend capabilities for agent harness adapters.
package harness

import (
	"fmt"
	"strings"
)

// DefaultMaxDepth is the default maximum dispatch depth when max_depth is absent.
const DefaultMaxDepth = 2

// ModelDetection records how reliably a backend can identify the actual model
// selected for an agent run.
type ModelDetection string

const (
	ModelDetectionNone       ModelDetection = "none"
	ModelDetectionTranscript ModelDetection = "transcript"
	ModelDetectionPayload    ModelDetection = "payload"
	ModelDetectionConfigured ModelDetection = "configured"
)

func validateModelDetection(source ModelDetection) error {
	if source != "" &&
		source != ModelDetectionNone &&
		source != ModelDetectionTranscript &&
		source != ModelDetectionPayload &&
		source != ModelDetectionConfigured {
		return fmt.Errorf("unknown model detection %q", source)
	}
	return nil
}

// ModelEvidence records provider-agnostic evidence for a selected model.
type ModelEvidence struct {
	Model     string         `json:"model,omitempty"`
	Effort    string         `json:"effort,omitempty"`
	Detection ModelDetection `json:"model_detection,omitempty"`
}

// Normalized returns evidence with whitespace-only variance removed.
func (e ModelEvidence) Normalized() ModelEvidence {
	e.Model = strings.TrimSpace(e.Model)
	e.Effort = strings.TrimSpace(e.Effort)
	return e
}

// Validate checks that model evidence uses a known detection source.
func (e ModelEvidence) Validate() error {
	return validateModelDetection(e.Detection)
}

// Capabilities records the affordances a harness backend can provide.
type Capabilities struct {
	Interactive     bool           `json:"interactive"`
	PreToolGate     bool           `json:"pre_tool_gate"`
	PostToolTrace   bool           `json:"post_tool_trace"`
	SessionContext  bool           `json:"session_context"`
	SubagentContext bool           `json:"subagent_context"`
	NativeSpawn     bool           `json:"native_spawn"`
	NativeWait      bool           `json:"native_wait"`
	NativeResume    bool           `json:"native_resume"`
	AgentEndSignal  bool           `json:"agent_end_signal"`
	ModelDetection  ModelDetection `json:"model_detection,omitempty"`
}

// Validate checks that capability metadata uses known enum values.
func (c Capabilities) Validate() error {
	return validateModelDetection(c.ModelDetection)
}

// HarnessConnection identifies a harness backend and the capabilities available
// through the current connection.
type HarnessConnection struct {
	Backend      string       `json:"backend"`
	Capabilities Capabilities `json:"capabilities"`
}

// Validate checks that the connection is usable as durable metadata.
func (c HarnessConnection) Validate() error {
	if c.Backend == "" {
		return fmt.Errorf("backend is required")
	}
	if err := c.Capabilities.Validate(); err != nil {
		return err
	}
	return nil
}
