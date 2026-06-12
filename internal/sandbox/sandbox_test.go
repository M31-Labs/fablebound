package sandbox

import (
	"context"
	"testing"
)

func TestRecordValidate(t *testing.T) {
	rec := Record{
		Mode:      ModeContainer,
		Profile:   "execution",
		Status:    StatusRequested,
		Runner:    "bubblewrap",
		Workspace: WorkspaceOverlay,
		Network:   NetworkDisabled,
		Mounts: []Mount{{
			Source: "/workspace",
			Target: "/workspace",
			Access: "read-write",
		}},
		Horizon: []HorizonManifest{{
			Path:          "exec.cap.json",
			SHA256:        "abc123",
			Capability:    "kernel.process.exec.deny",
			DangerMode:    "block",
			DangerScope:   "kernel.process",
			Reversibility: "reversible",
		}},
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !rec.HorizonEnabled() {
		t.Fatal("HorizonEnabled = false, want true")
	}
}

func TestRecordValidateRejectsUnknownMode(t *testing.T) {
	rec := Record{Mode: Mode("vm")}
	if err := rec.Validate(); err == nil {
		t.Fatal("Validate: nil, want error")
	}
}

func TestRecordValidateAllowsEmptyMetadata(t *testing.T) {
	var rec Record
	if err := rec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRecordValidateRejectsUnknownMetadata(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
	}{
		{
			name: "status",
			rec:  Record{Status: Status("pending")},
		},
		{
			name: "workspace",
			rec:  Record{Workspace: WorkspaceMode("bind")},
		},
		{
			name: "network",
			rec:  Record{Network: NetworkMode("isolated")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.rec.Validate(); err == nil {
				t.Fatal("Validate: nil, want error")
			}
		})
	}
}

func TestRecordValidateRejectsIncompleteMount(t *testing.T) {
	rec := Record{
		Mode:   ModeContainer,
		Mounts: []Mount{{Source: "/workspace"}},
	}
	if err := rec.Validate(); err == nil {
		t.Fatal("Validate: nil, want error")
	}
}

func TestProcessRunnerRecordsAdvisoryRequestedSandbox(t *testing.T) {
	rec, err := ProcessRunner{}.Activate(context.Background(), Spec{Profile: "execution"})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if rec.Mode != ModeProcess {
		t.Errorf("Mode=%q, want %q", rec.Mode, ModeProcess)
	}
	if rec.Status != StatusRequested {
		t.Errorf("Status=%q, want %q", rec.Status, StatusRequested)
	}
	if rec.Runner != "process" {
		t.Errorf("Runner=%q, want process", rec.Runner)
	}
	if EffectiveEnforcement("degraded", rec) != "degraded" {
		t.Fatal("advisory process record promoted degraded enforcement")
	}
}

func TestPlanAddsRequestedProcessRecordForDegradedAdapters(t *testing.T) {
	rec := Plan("execution", "degraded")
	if rec == nil {
		t.Fatal("Plan returned nil, want requested process record")
	}
	if rec.Profile != "execution" {
		t.Errorf("Profile=%q, want execution", rec.Profile)
	}
	if rec.Status != StatusRequested {
		t.Errorf("Status=%q, want requested", rec.Status)
	}
	if Plan("execution", "full") != nil {
		t.Fatal("Plan returned record for full adapter")
	}
}

func TestEffectiveEnforcementPromotionRequiresConstrainingActiveSandbox(t *testing.T) {
	tests := []struct {
		name string
		rec  *Record
		want string
	}{
		{
			name: "nil",
			rec:  nil,
			want: "degraded",
		},
		{
			name: "requested process",
			rec: &Record{
				Mode:   ModeProcess,
				Status: StatusRequested,
				Runner: "process",
			},
			want: "degraded",
		},
		{
			name: "unavailable container",
			rec: &Record{
				Mode:   ModeContainer,
				Status: StatusUnavailable,
				Runner: "bubblewrap",
			},
			want: "degraded",
		},
		{
			name: "active noop process",
			rec: &Record{
				Mode:   ModeProcess,
				Status: StatusActive,
				Runner: "noop",
			},
			want: "degraded",
		},
		{
			name: "active process metadata",
			rec: &Record{
				Mode:   ModeProcess,
				Status: StatusActive,
				Runner: "process",
			},
			want: "degraded",
		},
		{
			name: "active constraining container",
			rec: &Record{
				Mode:   ModeContainer,
				Status: StatusActive,
				Runner: "bubblewrap",
			},
			want: "sandboxed",
		},
		{
			name: "active constraining process",
			rec: &Record{
				Mode:      ModeProcess,
				Status:    StatusActive,
				Runner:    "bubblewrap",
				Workspace: WorkspaceOverlay,
			},
			want: "sandboxed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveEnforcement("degraded", tt.rec)
			if got != tt.want {
				t.Fatalf("EffectiveEnforcement = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffectiveEnforcementPreservesNonDegradedAdapterValues(t *testing.T) {
	rec := &Record{Mode: ModeContainer, Status: StatusActive, Runner: "bubblewrap"}
	for _, raw := range []string{"full", "sandboxed", ""} {
		if got := EffectiveEnforcement(raw, rec); got != raw {
			t.Errorf("EffectiveEnforcement(%q)=%q, want original", raw, got)
		}
	}
}
