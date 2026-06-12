// Package sandbox defines Tiller's runtime isolation metadata.
//
// The types here are deliberately declarative. A dispatch record may request or
// describe a sandbox before any runner exists for that mode. Code that needs a
// hard guarantee must check Status == "active" and the recorded runner details.
package sandbox

import (
	"context"
	"fmt"
)

// Mode identifies the isolation mechanism selected for a dispatch.
type Mode string

const (
	ModeNone      Mode = "none"
	ModeProcess   Mode = "process"
	ModeContainer Mode = "container"
)

// Status records whether the requested sandbox was actually active.
type Status string

const (
	StatusRequested   Status = "requested"
	StatusActive      Status = "active"
	StatusBypassed    Status = "bypassed"
	StatusUnavailable Status = "unavailable"
)

// WorkspaceMode describes how the workspace is exposed inside the sandbox.
type WorkspaceMode string

const (
	WorkspaceReadOnly WorkspaceMode = "read-only"
	WorkspaceOverlay  WorkspaceMode = "overlay"
	WorkspaceWritable WorkspaceMode = "writable"
)

// NetworkMode describes network access inside the sandbox.
type NetworkMode string

const (
	NetworkInherit  NetworkMode = "inherit"
	NetworkDisabled NetworkMode = "disabled"
	NetworkLoopback NetworkMode = "loopback"
	NetworkEgress   NetworkMode = "egress"
)

// Mount is a filesystem mount visible to the sandboxed process.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Access string `json:"access,omitempty"` // read-only|read-write
}

// Limits records resource limits requested for the dispatch.
type Limits struct {
	CPUs           string `json:"cpus,omitempty"`
	MemoryBytes    int64  `json:"memory_bytes,omitempty"`
	PIDs           int    `json:"pids,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// HorizonManifest records a Horizon capability manifest that was selected for
// host-side observability or enforcement around the dispatch.
type HorizonManifest struct {
	Path          string `json:"path,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Package       string `json:"package,omitempty"`
	Capability    string `json:"capability,omitempty"`
	DangerMode    string `json:"danger_mode,omitempty"`
	DangerScope   string `json:"danger_scope,omitempty"`
	Reversibility string `json:"reversibility,omitempty"`
}

// Record is persisted on scratch.Dispatch so every child agent has a durable
// account of its requested and actual runtime isolation.
type Record struct {
	Mode      Mode              `json:"mode,omitempty"`
	Profile   string            `json:"profile,omitempty"`
	Status    Status            `json:"status,omitempty"`
	Runner    string            `json:"runner,omitempty"` // bubblewrap|oci|process|horizon-sidecar|...
	Image     string            `json:"image,omitempty"`
	Workspace WorkspaceMode     `json:"workspace,omitempty"`
	Network   NetworkMode       `json:"network,omitempty"`
	Mounts    []Mount           `json:"mounts,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Limits    Limits            `json:"limits,omitempty"`
	Horizon   []HorizonManifest `json:"horizon,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

// Spec describes a requested sandbox activation. The first implementation is
// intentionally advisory: it records intent without claiming hard isolation.
type Spec struct {
	Mode    Mode
	Profile string
	Horizon []HorizonManifest
}

// Runner activates or records the requested sandbox state for a dispatch.
type Runner interface {
	Activate(ctx context.Context, spec Spec) (*Record, error)
}

// ProcessRunner records process-level sandbox intent. It does not install a
// constraining runtime, so its records remain requested and do not promote
// degraded adapters to sandboxed enforcement.
type ProcessRunner struct{}

// Activate returns a deterministic advisory process sandbox record.
func (ProcessRunner) Activate(_ context.Context, spec Spec) (*Record, error) {
	mode := spec.Mode
	if mode == "" {
		mode = ModeProcess
	}
	rec := &Record{
		Mode:      mode,
		Profile:   spec.Profile,
		Status:    StatusRequested,
		Runner:    "process",
		Workspace: WorkspaceWritable,
		Network:   NetworkInherit,
		Horizon:   append([]HorizonManifest(nil), spec.Horizon...),
		Reason:    "process runner records sandbox intent only; no constraining sandbox is active",
	}
	return rec, rec.Validate()
}

// Plan returns the default sandbox intent for an adapter/profile pair. Today
// degraded adapters get an advisory process record; full/sandboxed adapters do
// not need extra metadata from this foundation slice.
func Plan(profile string, adapterEnforcement string) *Record {
	if adapterEnforcement != "degraded" {
		return nil
	}
	rec, _ := ProcessRunner{}.Activate(context.Background(), Spec{
		Mode:    ModeProcess,
		Profile: profile,
	})
	return rec
}

// EffectiveEnforcement combines adapter-reported enforcement with runtime
// sandbox metadata. It only promotes degraded adapters when a sandbox record is
// active and materially constraining; requested, unavailable, bypassed, noop,
// and advisory process records preserve the adapter's raw enforcement.
func EffectiveEnforcement(adapterEnforcement string, rec *Record) string {
	if adapterEnforcement != "degraded" {
		return adapterEnforcement
	}
	if IsConstrainingActive(rec) {
		return "sandboxed"
	}
	return adapterEnforcement
}

// IsConstrainingActive reports whether a record can be treated as active
// runtime isolation for enforcement purposes.
func IsConstrainingActive(rec *Record) bool {
	if rec == nil || rec.Status != StatusActive {
		return false
	}
	switch rec.Runner {
	case "", "noop", "no-op", "process":
		return false
	}
	switch rec.Mode {
	case ModeContainer:
		return true
	case ModeProcess:
		return rec.Workspace == WorkspaceReadOnly ||
			rec.Workspace == WorkspaceOverlay ||
			rec.Network == NetworkDisabled ||
			rec.Network == NetworkLoopback ||
			rec.Limits.CPUs != "" ||
			rec.Limits.MemoryBytes > 0 ||
			rec.Limits.PIDs > 0 ||
			rec.Limits.TimeoutSeconds > 0 ||
			rec.HorizonEnabled()
	default:
		return false
	}
}

// Validate checks that a record uses known enum values. Empty fields are allowed
// so older artifacts and partially planned dispatches remain valid.
func (r Record) Validate() error {
	if r.Mode != "" && r.Mode != ModeNone && r.Mode != ModeProcess && r.Mode != ModeContainer {
		return fmt.Errorf("unknown sandbox mode %q", r.Mode)
	}
	if r.Status != "" && r.Status != StatusRequested && r.Status != StatusActive &&
		r.Status != StatusBypassed && r.Status != StatusUnavailable {
		return fmt.Errorf("unknown sandbox status %q", r.Status)
	}
	if r.Workspace != "" && r.Workspace != WorkspaceReadOnly &&
		r.Workspace != WorkspaceOverlay && r.Workspace != WorkspaceWritable {
		return fmt.Errorf("unknown workspace mode %q", r.Workspace)
	}
	if r.Network != "" && r.Network != NetworkInherit && r.Network != NetworkDisabled &&
		r.Network != NetworkLoopback && r.Network != NetworkEgress {
		return fmt.Errorf("unknown network mode %q", r.Network)
	}
	for i, m := range r.Mounts {
		if m.Source == "" || m.Target == "" {
			return fmt.Errorf("mount %d requires source and target", i)
		}
	}
	return nil
}

// HorizonEnabled reports whether host-side Horizon capability manifests are
// attached to this sandbox record.
func (r Record) HorizonEnabled() bool {
	return len(r.Horizon) > 0
}
