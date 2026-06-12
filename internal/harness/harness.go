// Package harness describes backend capabilities for agent harness adapters.
package harness

import "fmt"

// ModelDetection records how reliably a backend can identify the actual model
// selected for an agent run.
type ModelDetection string

const (
	ModelDetectionNone       ModelDetection = "none"
	ModelDetectionTranscript ModelDetection = "transcript"
	ModelDetectionPayload    ModelDetection = "payload"
	ModelDetectionConfigured ModelDetection = "configured"
)

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
	if c.ModelDetection != "" &&
		c.ModelDetection != ModelDetectionNone &&
		c.ModelDetection != ModelDetectionTranscript &&
		c.ModelDetection != ModelDetectionPayload &&
		c.ModelDetection != ModelDetectionConfigured {
		return fmt.Errorf("unknown model detection %q", c.ModelDetection)
	}
	return nil
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
