package run_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/run"
)

// ── ID tests ──────────────────────────────────────────────────────────────────

func TestNewRunID_Format(t *testing.T) {
	id := run.NewRunID()
	// Format: YYYYMMDD-HHMMSS-xxxx  (total 20 chars)
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		t.Fatalf("expected 3 dash-separated parts, got %d: %q", len(parts), id)
	}
	if len(parts[0]) != 8 {
		t.Errorf("date part len=%d want 8: %q", len(parts[0]), parts[0])
	}
	if len(parts[1]) != 6 {
		t.Errorf("time part len=%d want 6: %q", len(parts[1]), parts[1])
	}
	if len(parts[2]) != 4 {
		t.Errorf("suffix len=%d want 4: %q", len(parts[2]), parts[2])
	}
	// Suffix must be lowercase base36.
	for _, c := range parts[2] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("non-base36 char %q in suffix %q", c, parts[2])
		}
	}
}

func TestNewDispatchID(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{{0, "d01"}, {1, "d02"}, {9, "d10"}, {98, "d99"}}
	for _, c := range cases {
		got := run.NewDispatchID(c.n)
		if got != c.want {
			t.Errorf("NewDispatchID(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// ── Store tests ───────────────────────────────────────────────────────────────

func TestCreateRun(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)

	id, err := s.CreateRun()
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Verify expected sub-directories were created.
	runDir := s.RunDir(id)
	for _, sub := range []string{"audit", "notes", "dispatches"} {
		if _, err := os.Stat(filepath.Join(runDir, sub)); err != nil {
			t.Errorf("expected dir %s: %v", sub, err)
		}
	}
}

func TestCurrentRunDir_FromEnv(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	t.Setenv("TILLER_RUN_DIR", runDir)
	got, err := run.CurrentRunDir()
	if err != nil {
		t.Fatalf("CurrentRunDir: %v", err)
	}
	if got != runDir {
		t.Errorf("got %q want %q", got, runDir)
	}

	gotID, err := run.CurrentRunID()
	if err != nil {
		t.Fatal(err)
	}
	if gotID != id {
		t.Errorf("CurrentRunID got %q want %q", gotID, id)
	}
}

func TestCurrentRunDir_Unset(t *testing.T) {
	t.Setenv("TILLER_RUN_DIR", "")
	_, err := run.CurrentRunDir()
	if err == nil {
		t.Fatal("expected error when TILLER_RUN_DIR is unset")
	}
}

// ── Manifest tests ────────────────────────────────────────────────────────────

func TestManifestRoundtrip(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	now := time.Now().UTC().Truncate(time.Second)
	m := &run.Manifest{
		RunID:       id,
		Task:        "test task",
		Workspace:   "/tmp/ws",
		Status:      "created",
		FableBudget: 2,
		CreatedAt:   now,
		PolicySHAs:  map[string]string{"dispatch": "abc123", "toolgate": "def456"},
	}

	if err := run.WriteManifest(runDir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	got, err := run.ReadManifest(runDir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.RunID != m.RunID || got.Task != m.Task || got.Status != m.Status {
		t.Errorf("manifest mismatch: got %+v", got)
	}
	if got.PolicySHAs["dispatch"] != "abc123" {
		t.Errorf("policy sha mismatch: %v", got.PolicySHAs)
	}
}

// TestManifestLegacyFableBudget verifies that a raw v1 manifest.json containing
// "fable_budget" (and no "reason_budget") is read back with the budget promoted
// to the FableBudget field via the legacy fallback in ReadManifest.
func TestManifestLegacyFableBudget(t *testing.T) {
	dir := t.TempDir()
	// Write a minimal v1 manifest with only fable_budget (no reason_budget key).
	raw := `{"run_id":"test-run","task":"t","workspace":"/tmp","status":"created","fable_budget":3,"created_at":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := run.ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.FableBudget != 3 {
		t.Errorf("legacy fable_budget not promoted: got FableBudget=%d, want 3", m.FableBudget)
	}
}

// ── Meta tests ────────────────────────────────────────────────────────────────

func mkMeta(runDir string, s *run.Store, runID string, id, parent, role, model, status string, depth int) error {
	dispDir := filepath.Join(runDir, "dispatches", id)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		return err
	}
	m := &run.Meta{
		ID:        id,
		Parent:    parent,
		Role:      role,
		Model:     model,
		Status:    status,
		Depth:     depth,
		StartedAt: time.Now().UTC(),
	}
	return run.WriteMeta(runDir, m)
}

func TestMetaRoundtrip(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	if err := mkMeta(runDir, s, id, "d01", "", "orchestrator", "fable", "running", 0); err != nil {
		t.Fatal(err)
	}

	m, err := run.ReadMeta(runDir, "d01")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if m.ID != "d01" || m.Role != "orchestrator" || m.Model != "fable" {
		t.Errorf("unexpected meta: %+v", m)
	}
}

func TestScanMetasAndCounters(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	// d01: root, fable, running
	// d02: child of d01, sonnet, completed
	// d03: child of root, haiku, running
	if err := mkMeta(runDir, s, id, "d01", "", "orchestrator", "fable", "running", 0); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d02", "d01", "investigator", "sonnet", "completed", 1); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d03", "", "investigator", "haiku", "running", 1); err != nil {
		t.Fatal(err)
	}

	active, err := run.ActiveCount(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Errorf("ActiveCount=%d want 2", active)
	}

	fable, err := run.FableCount(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if fable != 1 {
		t.Errorf("FableCount=%d want 1", fable)
	}
}

// ── Tree tests ────────────────────────────────────────────────────────────────

func TestBuildTree_LinearChain(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	// root → d01 → d02, root → d03
	if err := mkMeta(runDir, s, id, "root", "", "orchestrator", "fable", "completed", 0); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d01", "root", "investigator", "sonnet", "completed", 1); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d02", "d01", "worker", "sonnet", "completed", 2); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d03", "root", "investigator", "haiku", "completed", 1); err != nil {
		t.Fatal(err)
	}

	root, err := run.BuildTree(runDir)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	if root.Meta == nil || root.Meta.ID != "root" {
		t.Fatalf("expected root node, got %+v", root.Meta)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root should have 2 children, got %d", len(root.Children))
	}

	// d01 comes before d03 lexicographically (both start with 'd', '0' < '0', "d01" < "d03").
	var d01, d03 *run.Node
	for _, c := range root.Children {
		switch c.Meta.ID {
		case "d01":
			d01 = c
		case "d03":
			d03 = c
		}
	}
	if d01 == nil {
		t.Fatal("d01 not found in root children")
	}
	if d03 == nil {
		t.Fatal("d03 not found in root children")
	}
	if len(d01.Children) != 1 || d01.Children[0].Meta.ID != "d02" {
		t.Errorf("d01 should have child d02, got %+v", d01.Children)
	}
	if len(d03.Children) != 0 {
		t.Errorf("d03 should have no children")
	}
}

func TestRender(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	if err := mkMeta(runDir, s, id, "root", "", "orchestrator", "fable", "completed", 0); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d01", "root", "investigator", "sonnet", "completed", 1); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d02", "d01", "worker", "sonnet", "completed", 2); err != nil {
		t.Fatal(err)
	}
	if err := mkMeta(runDir, s, id, "d03", "root", "investigator", "haiku", "completed", 1); err != nil {
		t.Fatal(err)
	}

	root, err := run.BuildTree(runDir)
	if err != nil {
		t.Fatal(err)
	}
	rendered := run.Render(root)

	// The render must contain all 4 ids.
	for _, want := range []string{"root", "d01", "d02", "d03"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered tree missing %q:\n%s", want, rendered)
		}
	}
}

// ── Orphan detection tests ────────────────────────────────────────────────────

// TestIsOrphan_NoSupervisorPID verifies that a meta with SupervisorPID==0 is
// never treated as an orphan (older dispatches that predated PID recording).
func TestIsOrphan_NoSupervisorPID(t *testing.T) {
	m := &run.Meta{
		ID:            "d01",
		Status:        "running",
		SupervisorPID: 0,
	}
	if m.IsOrphan() {
		t.Error("meta with SupervisorPID=0 should not be an orphan")
	}
}

// TestIsOrphan_TerminalStatus verifies that non-running dispatches are not orphans.
func TestIsOrphan_TerminalStatus(t *testing.T) {
	for _, status := range []string{"completed", "failed", "halted", "stale"} {
		m := &run.Meta{
			ID:            "d01",
			Status:        status,
			SupervisorPID: 99999,
		}
		if m.IsOrphan() {
			t.Errorf("meta with status=%q should not be an orphan", status)
		}
	}
}

// TestIsOrphan_AliveProcess verifies that a running dispatch with a live PID
// is not considered an orphan. Uses the current process's PID (guaranteed alive).
func TestIsOrphan_AliveProcess(t *testing.T) {
	m := &run.Meta{
		ID:            "d01",
		Status:        "running",
		SupervisorPID: os.Getpid(),
	}
	if m.IsOrphan() {
		t.Error("dispatch with live PID (self) should not be an orphan")
	}
}

// TestIsOrphan_KilledSupervisor verifies that a running dispatch whose supervisor
// has been killed is detected as an orphan (the kill -9 acceptance test per plan T3.3).
func TestIsOrphan_KilledSupervisor(t *testing.T) {
	// Start a real short-lived process we can then kill.
	// Use `sleep 60` so it stays alive until we kill it.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	// The process is alive — not an orphan yet.
	m := &run.Meta{
		ID:            "d99",
		Status:        "running",
		SupervisorPID: pid,
	}
	if m.IsOrphan() {
		t.Fatal("dispatch should not be orphan while supervisor is alive")
	}

	// Kill it with -9.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill supervisor: %v", err)
	}
	_ = cmd.Wait() // reap to avoid zombie

	// Now it should be an orphan.
	if !m.IsOrphan() {
		t.Error("dispatch should be orphan after supervisor killed with -9")
	}
}

// TestEffectiveStatus_Orphan verifies EffectiveStatus returns "stale" for orphans.
func TestEffectiveStatus_Orphan(t *testing.T) {
	// Start and kill a process.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	m := &run.Meta{
		ID:            "d99",
		Status:        "running",
		SupervisorPID: pid,
	}
	if got := m.EffectiveStatus(); got != "stale" {
		t.Errorf("EffectiveStatus after kill = %q, want stale", got)
	}
}

// TestEffectiveStatus_Normal verifies EffectiveStatus returns the recorded
// status when the supervisor is alive or no PID is recorded.
func TestEffectiveStatus_Normal(t *testing.T) {
	m := &run.Meta{
		ID:     "d01",
		Status: "completed",
	}
	if got := m.EffectiveStatus(); got != "completed" {
		t.Errorf("EffectiveStatus for completed = %q, want completed", got)
	}

	m2 := &run.Meta{
		ID:            "d01",
		Status:        "running",
		SupervisorPID: os.Getpid(),
	}
	if got := m2.EffectiveStatus(); got != "running" {
		t.Errorf("EffectiveStatus for running+alive = %q, want running", got)
	}
}

// ── Dispatch-alloc atomicity test (Fix 2) ────────────────────────────────────

// TestAllocDispatch_NoDuplicateIDs verifies that N concurrent AllocDispatch
// calls on the same run produce N distinct dispatch IDs and directories with
// no clobber.  This is the regression test for the dispatch-ID allocation race.
func TestAllocDispatch_NoDuplicateIDs(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)

	id, err := s.CreateRun()
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	const n = 8
	type result struct {
		dispatchID  string
		dispatchDir string
		err         error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			did, ddir, err := s.AllocDispatch(id)
			results[i] = result{did, ddir, err}
		}()
	}
	wg.Wait()

	// All must succeed.
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: AllocDispatch error: %v", i, r.err)
		}
	}

	// All dispatch IDs must be unique.
	seen := make(map[string]int, n)
	for i, r := range results {
		if r.dispatchID == "" {
			continue
		}
		if prev, dup := seen[r.dispatchID]; dup {
			t.Errorf("duplicate dispatch ID %q from goroutines %d and %d", r.dispatchID, prev, i)
		}
		seen[r.dispatchID] = i
	}
	if len(seen) != n {
		t.Errorf("expected %d unique dispatch IDs, got %d", n, len(seen))
	}

	// All dispatch directories must exist on disk.
	for _, r := range results {
		if r.dispatchDir == "" {
			continue
		}
		if _, statErr := os.Stat(r.dispatchDir); statErr != nil {
			t.Errorf("dispatch dir %q missing after AllocDispatch: %v", r.dispatchDir, statErr)
		}
	}
}

// ── Flock concurrency test ────────────────────────────────────────────────────

// TestConcurrentMetaWrite spawns 10 goroutines all writing to the same
// meta.json. After all finish, the file must contain valid JSON.
func TestConcurrentMetaWrite(t *testing.T) {
	base := t.TempDir()
	s := run.NewStore(base)
	id, err := s.CreateRun()
	if err != nil {
		t.Fatal(err)
	}
	runDir := s.RunDir(id)

	// Create the dispatch directory.
	if err := os.MkdirAll(filepath.Join(runDir, "dispatches", "d01"), 0o755); err != nil {
		t.Fatal(err)
	}

	const numWriters = 10
	var wg sync.WaitGroup
	wg.Add(numWriters)
	errCh := make(chan error, numWriters)

	for i := 0; i < numWriters; i++ {
		i := i
		go func() {
			defer wg.Done()
			m := &run.Meta{
				ID:        "d01",
				Role:      "worker",
				Model:     "sonnet",
				Status:    fmt.Sprintf("status-%d", i),
				Depth:     1,
				StartedAt: time.Now().UTC(),
			}
			if err := run.WriteMeta(runDir, m); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent write error: %v", err)
	}

	// The file must contain valid JSON.
	data, err := os.ReadFile(filepath.Join(runDir, "dispatches", "d01", "meta.json"))
	if err != nil {
		t.Fatalf("reading meta.json: %v", err)
	}
	var m run.Meta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("meta.json is not valid JSON after concurrent writes: %v\ncontents: %s", err, data)
	}
	if m.ID != "d01" {
		t.Errorf("meta.json ID=%q want d01", m.ID)
	}
}
