package sandbox

import "testing"

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
