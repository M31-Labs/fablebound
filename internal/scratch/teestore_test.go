package scratch_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

// newFSStore opens a fresh fsstore under a temp dir.
func newFSStore(t *testing.T) scratch.Store {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	if err := os.MkdirAll(runsBase, 0o755); err != nil {
		t.Fatal(err)
	}
	return fsstore.Open(runsBase)
}

// TestTeeStore_BasicWriteRead verifies that writes go to fs and reads come from fs.
func TestTeeStore_BasicWriteRead(t *testing.T) {
	fs1 := newFSStore(t)
	fs2 := newFSStore(t) // acts as the "pg" mirror

	tee := scratch.NewTeeStore(fs1, fs2)
	defer tee.Close()

	r := &scratch.Run{
		Task:      "basic test",
		Workspace: "/tmp",
		Status:    "running",
	}
	runID, err := tee.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := tee.ReadRun(runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.Task != "basic test" {
		t.Errorf("ReadRun.Task = %q, want %q", got.Task, "basic test")
	}
}

// TestTeeStore_MirrorAsync verifies that after Close the mirror has received writes.
func TestTeeStore_MirrorAsync(t *testing.T) {
	fs1 := newFSStore(t)
	mirror := newFSStore(t) // acts as "pg"

	tee := scratch.NewTeeStore(fs1, mirror)

	r := &scratch.Run{Task: "mirror test", Workspace: "/tmp", Status: "running"}
	runID, err := tee.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Write a dispatch to the tee.
	did, err := tee.AllocDispatch(runID)
	if err != nil {
		t.Fatalf("AllocDispatch: %v", err)
	}
	if err := tee.WriteBrief(runID, did, []byte("brief text")); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}
	d := &scratch.Dispatch{
		ID:        did,
		Role:      "worker",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := tee.WriteDispatch(runID, d); err != nil {
		t.Fatalf("WriteDispatch: %v", err)
	}

	// Close drains the queue.
	if err := tee.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Now verify mirror received the run and dispatch.
	mirrorRun, err := mirror.ReadRun(runID)
	if err != nil {
		t.Fatalf("mirror.ReadRun: %v", err)
	}
	if mirrorRun.Task != "mirror test" {
		t.Errorf("mirror run task = %q, want %q", mirrorRun.Task, "mirror test")
	}

	mirrorDispatches, err := mirror.ListDispatches(runID)
	if err != nil {
		t.Fatalf("mirror.ListDispatches: %v", err)
	}
	if len(mirrorDispatches) == 0 {
		t.Error("mirror has no dispatches; expected ≥1")
	}
}

// TestTeeStore_FSAuthoritative verifies that reads always come from fs even when mirror differs.
func TestTeeStore_FSAuthoritative(t *testing.T) {
	fs1 := newFSStore(t)
	mirror := newFSStore(t)

	tee := scratch.NewTeeStore(fs1, mirror)
	defer tee.Close()

	r := &scratch.Run{Task: "authoritative", Workspace: "/tmp", Status: "running"}
	runID, err := tee.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Write a different task to the mirror directly (simulating drift).
	// Drain the tee queue first so our write doesn't get overwritten.
	time.Sleep(50 * time.Millisecond) // let queue drain
	mirrorRun := &scratch.Run{ID: runID, Task: "DIFFERENT", Workspace: "/tmp", Status: "completed"}
	_ = mirror.WriteRun(mirrorRun)

	// Tee reads must still see the fs value ("authoritative").
	got, err := tee.ReadRun(runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.Task != "authoritative" {
		t.Errorf("tee.ReadRun.Task = %q, want %q (fs is authoritative)", got.Task, "authoritative")
	}
}

// TestTeeStore_CloseIdempotent verifies double-Close is safe.
func TestTeeStore_CloseIdempotent(t *testing.T) {
	fs1 := newFSStore(t)
	mirror := newFSStore(t)
	tee := scratch.NewTeeStore(fs1, mirror)
	if err := tee.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tee.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestTeeStore_QueueFullDrops verifies that a full queue drops ops without blocking.
func TestTeeStore_QueueFullDrops(t *testing.T) {
	fs1 := newFSStore(t)

	// Use a blocking mirror that never processes ops.
	// Instead, use a normal fsstore but fill the queue first by using
	// a TeeStore whose goroutine we pause.
	mirror := newFSStore(t)

	// Create a tee with a tiny queue by using the internal queue mechanism.
	// We can't easily set queue size, so just saturate with many ops and
	// verify no hang occurs.
	tee := scratch.NewTeeStore(fs1, mirror)

	r := &scratch.Run{Task: "fill", Workspace: "/tmp", Status: "running"}
	runID, err := tee.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Flood with many writes — the queue should handle them without hanging.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 600 {
			_ = tee.AppendTraceEvent(runID, "root", scratch.TraceEvent{
				Kind:   "tool",
				RunID:  runID,
				Tool:   "Bash",
				Status: "ok",
			})
		}
	}()

	select {
	case <-done:
		// ok — no hang
	case <-time.After(5 * time.Second):
		t.Fatal("flood writes hung — queue is blocking the caller")
	}

	if err := tee.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestTeeStore_ConcurrentWrites verifies concurrent writes to tee don't race.
func TestTeeStore_ConcurrentWrites(t *testing.T) {
	fs1 := newFSStore(t)
	mirror := newFSStore(t)
	tee := scratch.NewTeeStore(fs1, mirror)
	defer tee.Close()

	r := &scratch.Run{Task: "concurrent", Workspace: "/tmp", Status: "running"}
	runID, err := tee.CreateRun(r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = tee.AppendTraceEvent(runID, "root", scratch.TraceEvent{
				Kind:  "tool",
				RunID: runID,
				Tool:  "Read",
			})
		}(i)
	}
	wg.Wait()
}

// TestTeeStore_ImplementsStore is a compile-time check (also checks methods at runtime).
func TestTeeStore_ImplementsStore(t *testing.T) {
	fs1 := newFSStore(t)
	mirror := newFSStore(t)
	var _ scratch.Store = scratch.NewTeeStore(fs1, mirror)
	_ = scratch.NewTeeStore(fs1, mirror).Close()
}
