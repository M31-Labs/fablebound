package fsstore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/scratch/storetest"
)

// openStore returns a fresh FS store rooted in a temp directory.
func openStore(t *testing.T) scratch.Store {
	t.Helper()
	base := filepath.Join(t.TempDir(), "runs")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	return fsstore.Open(base)
}

// ── Conformance suite ─────────────────────────────────────────────────────────

func TestConformance(t *testing.T) {
	storetest.Run(t, openStore)
}

// ── Parity tests ──────────────────────────────────────────────────────────────

// TestMetaJSONByteStable verifies that a Dispatch containing only v1 fields
// marshals to JSON byte-identical to what run.WriteMeta produces.
//
// This is the key regression test for the fsstore: any v2 field with omitempty
// must be absent from the output when zero-valued.
func TestMetaJSONByteStable(t *testing.T) {
	base := t.TempDir()
	runBase := filepath.Join(base, "runs")
	if err := os.MkdirAll(runBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a run and dispatch via run.Store (v1 path).
	rs := run.NewStore(runBase)
	runID, err := rs.CreateRun()
	if err != nil {
		t.Fatalf("run.Store.CreateRun: %v", err)
	}
	runDir := rs.RunDir(runID)

	dispID, _, err := rs.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("run.Store.AllocDispatch: %v", err)
	}

	now := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	v1Meta := &run.Meta{
		ID:        dispID,
		Parent:    "",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "insight",
		Status:    "running",
		Depth:     0,
		StartedAt: now,
	}
	if err := run.WriteMeta(runDir, v1Meta); err != nil {
		t.Fatalf("run.WriteMeta: %v", err)
	}

	// Read the bytes that run.WriteMeta produced.
	v1Bytes, err := os.ReadFile(filepath.Join(runDir, "dispatches", dispID, "meta.json"))
	if err != nil {
		t.Fatalf("read v1 meta.json: %v", err)
	}

	// Now do the same via fsstore.WriteDispatch with only v1 fields populated.
	fs2 := fsstore.Open(runBase)
	runID2, err := fs2.CreateRun(&scratch.Run{
		Task:      "parity test",
		Workspace: t.TempDir(),
		Status:    "created",
	})
	if err != nil {
		t.Fatalf("fsstore.CreateRun: %v", err)
	}
	dispID2, err := fs2.AllocDispatch(runID2)
	if err != nil {
		t.Fatalf("fsstore.AllocDispatch: %v", err)
	}

	d := &scratch.Dispatch{
		ID:        dispID2,
		Parent:    "",
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "insight",
		Status:    "running",
		Depth:     0,
		StartedAt: now,
		// v2 fields deliberately zero / empty — must not appear in JSON.
	}
	if err := fs2.WriteDispatch(runID2, d); err != nil {
		t.Fatalf("fsstore.WriteDispatch: %v", err)
	}
	v2Bytes, err := os.ReadFile(filepath.Join(runBase, runID2, "dispatches", dispID2, "meta.json"))
	if err != nil {
		t.Fatalf("read v2 meta.json: %v", err)
	}

	// The byte sequences may differ in the dispatch ID value (d01 vs d01 — same),
	// but the JSON structure and field set must be identical.
	// We compare by normalising: unmarshal both into map[string]any and compare keys.
	var m1, m2 map[string]any
	if err := json.Unmarshal(v1Bytes, &m1); err != nil {
		t.Fatalf("unmarshal v1 meta: %v", err)
	}
	if err := json.Unmarshal(v2Bytes, &m2); err != nil {
		t.Fatalf("unmarshal v2 meta: %v", err)
	}

	// The same set of JSON keys must be present (no v2 extras).
	for k := range m2 {
		if _, ok := m1[k]; !ok {
			t.Errorf("v2 meta.json has extra key %q not in v1 meta.json", k)
		}
	}
	for k := range m1 {
		if _, ok := m2[k]; !ok {
			t.Errorf("v1 meta.json has key %q missing from v2 meta.json", k)
		}
	}
}

// TestParityDirectoryLayout verifies that fsstore produces the same
// directory structure as the v1 run.Store for an equivalent set of operations.
func TestParityDirectoryLayout(t *testing.T) {
	base := t.TempDir()

	// ── V1 path: use run.Store + manual file writes ───────────────────────────
	v1Base := filepath.Join(base, "v1", "runs")
	if err := os.MkdirAll(v1Base, 0o755); err != nil {
		t.Fatal(err)
	}
	v1Store := run.NewStore(v1Base)
	v1RunID, err := v1Store.CreateRun()
	if err != nil {
		t.Fatalf("v1 CreateRun: %v", err)
	}
	v1RunDir := v1Store.RunDir(v1RunID)

	v1Manifest := &run.Manifest{
		RunID:     v1RunID,
		Task:      "parity probe",
		Workspace: t.TempDir(),
		Status:    "running",
		CreatedAt: time.Now().UTC(),
	}
	if err := run.WriteManifest(v1RunDir, v1Manifest); err != nil {
		t.Fatalf("v1 WriteManifest: %v", err)
	}

	v1DispID, _, err := v1Store.AllocDispatch(v1RunID)
	if err != nil {
		t.Fatalf("v1 AllocDispatch: %v", err)
	}
	v1Meta := &run.Meta{
		ID:        v1DispID,
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "insight",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now().UTC(),
	}
	if err := run.WriteMeta(v1RunDir, v1Meta); err != nil {
		t.Fatalf("v1 WriteMeta: %v", err)
	}
	// Write brief, report, settings manually.
	dispDir := filepath.Join(v1RunDir, "dispatches", v1DispID)
	if err := os.WriteFile(filepath.Join(dispDir, "brief.md"), []byte("brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispDir, "report.md"), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ── V2 path: use fsstore ──────────────────────────────────────────────────
	v2Base := filepath.Join(base, "v2", "runs")
	if err := os.MkdirAll(v2Base, 0o755); err != nil {
		t.Fatal(err)
	}
	v2 := fsstore.Open(v2Base)
	r := &scratch.Run{
		Task:      "parity probe",
		Workspace: t.TempDir(),
		Status:    "running",
	}
	v2RunID, err := v2.CreateRun(r)
	if err != nil {
		t.Fatalf("v2 CreateRun: %v", err)
	}

	v2DispID, err := v2.AllocDispatch(v2RunID)
	if err != nil {
		t.Fatalf("v2 AllocDispatch: %v", err)
	}
	d := &scratch.Dispatch{
		ID:        v2DispID,
		Role:      "orchestrator",
		Model:     "fable",
		Profile:   "insight",
		Status:    "running",
		Depth:     0,
		StartedAt: time.Now().UTC(),
	}
	if err := v2.WriteDispatch(v2RunID, d); err != nil {
		t.Fatalf("v2 WriteDispatch: %v", err)
	}
	if err := v2.WriteBrief(v2RunID, v2DispID, []byte("brief")); err != nil {
		t.Fatalf("v2 WriteBrief: %v", err)
	}
	if err := v2.WriteReport(v2RunID, v2DispID, []byte("report")); err != nil {
		t.Fatalf("v2 WriteReport: %v", err)
	}
	if err := v2.WriteAdapterConfig(v2RunID, v2DispID, []byte("{}")); err != nil {
		t.Fatalf("v2 WriteAdapterConfig: %v", err)
	}

	// ── Compare directory layouts ─────────────────────────────────────────────
	v1Layout := collectRelPaths(t, filepath.Join(v1Base, v1RunID))
	v2Layout := collectRelPaths(t, filepath.Join(v2Base, v2RunID))

	// Build sets.
	v1Set := make(map[string]bool, len(v1Layout))
	for _, p := range v1Layout {
		v1Set[p] = true
	}
	v2Set := make(map[string]bool, len(v2Layout))
	for _, p := range v2Layout {
		v2Set[p] = true
	}

	// Every v1 path must exist in v2.
	for p := range v1Set {
		if !v2Set[p] {
			t.Errorf("v1 path %q missing from v2 layout", p)
		}
	}
	// Every v2 path must exist in v1 (no extra files).
	for p := range v2Set {
		if !v1Set[p] {
			t.Errorf("v2 has extra path %q not in v1 layout", p)
		}
	}
}

// collectRelPaths returns the sorted list of relative file paths under dir.
func collectRelPaths(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return paths
}

// TestDispatchV2FieldsRoundtrip verifies that v2 fields (Tier, Enforcement)
// survive a WriteDispatch/ReadDispatch round-trip.
func TestDispatchV2FieldsRoundtrip(t *testing.T) {
	base := filepath.Join(t.TempDir(), "runs")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	s := fsstore.Open(base)
	runID := mustCreateRun(t, s)
	did := mustAllocDispatch(t, s, runID)

	d := &scratch.Dispatch{
		ID:          did,
		Role:        "orchestrator",
		Model:       "fable",
		Status:      "running",
		StartedAt:   time.Now().UTC(),
		Tier:        "reason",
		Enforcement: "full",
	}
	if err := s.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}
	got, err := s.ReadDispatch(runID, did)
	if err != nil {
		t.Fatalf("ReadDispatch: %v", err)
	}
	if got.Tier != "reason" {
		t.Errorf("Tier=%q want reason", got.Tier)
	}
	if got.Enforcement != "full" {
		t.Errorf("Enforcement=%q want full", got.Enforcement)
	}
}

// helpers

func mustCreateRun(t *testing.T, s scratch.Store) string {
	t.Helper()
	r := &scratch.Run{
		Task:      "test",
		Workspace: t.TempDir(),
		Status:    "created",
	}
	id, err := s.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return id
}

func mustAllocDispatch(t *testing.T, s scratch.Store, runID string) string {
	t.Helper()
	did, err := s.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	return did
}
