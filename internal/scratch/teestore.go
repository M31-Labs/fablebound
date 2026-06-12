package scratch

import (
	"io"
	"log"
	"sync"
	"time"

	"m31labs.dev/tiller/internal/auditlog"
)

// TeeStore is a scratch.Store that writes synchronously to fs (authoritative)
// and mirrors all writes asynchronously to pg (never delays or fails the caller).
//
// Design (spec §5.1, plan P3.4):
//   - Every write goes to fs first, synchronously; error semantics are identical
//     to fs alone. If the fs write fails, the pg mirror is NOT attempted.
//   - pg mirror writes are queued on a bounded channel (capacity mirrorQueueCap)
//     and drained by a single background goroutine. If the queue is full, the
//     mirror op is silently dropped (logged at debug level) — the caller is
//     never blocked.
//   - ALL reads are served from fs.
//   - Close drains the mirror queue before returning (bounded drain with no
//     explicit timeout — the queue is small and writes are fast).
//
// fs is the reconciliation reference. If fs and pg diverge, fs wins.
type TeeStore struct {
	fs Store
	pg Store

	mu      sync.Mutex
	mirrorQ chan mirrorOp
	wg      sync.WaitGroup
	closed  bool
}

const mirrorQueueCap = 512

// mirrorOp is a function that performs one mirror write to pg.
type mirrorOp func()

// NewTeeStore constructs a TeeStore wrapping fs (authoritative) and pg (mirror).
// The caller must call Close when done to drain the mirror queue.
func NewTeeStore(fs, pg Store) *TeeStore {
	t := &TeeStore{
		fs:      fs,
		pg:      pg,
		mirrorQ: make(chan mirrorOp, mirrorQueueCap),
	}
	t.wg.Add(1)
	go t.mirrorLoop()
	return t
}

// mirrorLoop drains the mirror queue and executes each op.
func (t *TeeStore) mirrorLoop() {
	defer t.wg.Done()
	for op := range t.mirrorQ {
		op()
	}
}

// enqueue adds a mirror op to the queue. If the queue is full it logs and drops.
// MUST NOT block the caller.
func (t *TeeStore) enqueue(op mirrorOp) {
	select {
	case t.mirrorQ <- op:
	default:
		log.Printf("teestore: mirror queue full, dropping op")
	}
}

// Close drains the mirror queue and waits for the goroutine to exit.
// After Close, the TeeStore must not be used.
func (t *TeeStore) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	close(t.mirrorQ)
	t.wg.Wait()
	return nil
}

// ── Run lifecycle ─────────────────────────────────────────────────────────────

func (t *TeeStore) CreateRun(r *Run) (string, error) {
	runID, err := t.fs.CreateRun(r)
	if err != nil {
		return "", err
	}
	rCopy := *r
	rCopy.ID = runID
	t.enqueue(func() {
		if _, err := t.pg.CreateRun(&rCopy); err != nil {
			log.Printf("teestore: mirror CreateRun %s: %v", runID, err)
		}
	})
	return runID, nil
}

func (t *TeeStore) ReadRun(runID string) (*Run, error) {
	return t.fs.ReadRun(runID)
}

func (t *TeeStore) WriteRun(r *Run) error {
	if err := t.fs.WriteRun(r); err != nil {
		return err
	}
	rCopy := *r
	t.enqueue(func() {
		if err := t.pg.WriteRun(&rCopy); err != nil {
			log.Printf("teestore: mirror WriteRun %s: %v", rCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) ListRuns() ([]RunSummary, error) {
	return t.fs.ListRuns()
}

// ── Dispatch records ──────────────────────────────────────────────────────────

func (t *TeeStore) AllocDispatch(runID string) (string, error) {
	dispatchID, err := t.fs.AllocDispatch(runID)
	if err != nil {
		return "", err
	}
	t.enqueue(func() {
		if _, err := t.pg.AllocDispatch(runID); err != nil {
			log.Printf("teestore: mirror AllocDispatch %s: %v", runID, err)
		}
	})
	return dispatchID, nil
}

func (t *TeeStore) ReadDispatch(runID, dispatchID string) (*Dispatch, error) {
	return t.fs.ReadDispatch(runID, dispatchID)
}

func (t *TeeStore) WriteDispatch(runID string, d *Dispatch) error {
	if err := t.fs.WriteDispatch(runID, d); err != nil {
		return err
	}
	dCopy := *d
	t.enqueue(func() {
		if err := t.pg.WriteDispatch(runID, &dCopy); err != nil {
			log.Printf("teestore: mirror WriteDispatch %s/%s: %v", runID, dCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) ListDispatches(runID string) ([]*Dispatch, error) {
	return t.fs.ListDispatches(runID)
}

func (t *TeeStore) DispatchFacts(runID string) (Facts, error) {
	return t.fs.DispatchFacts(runID)
}

// ── Agent / checkpoint lifecycle records ─────────────────────────────────────

func (t *TeeStore) CreateAgentRun(runID string, ar *AgentRun) error {
	if err := t.fs.CreateAgentRun(runID, ar); err != nil {
		return err
	}
	arCopy := cloneAgentRun(ar)
	t.enqueue(func() {
		if err := t.pg.CreateAgentRun(runID, arCopy); err != nil {
			log.Printf("teestore: mirror CreateAgentRun %s/%s: %v", runID, arCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) WriteAgentRun(runID string, ar *AgentRun) error {
	if err := t.fs.WriteAgentRun(runID, ar); err != nil {
		return err
	}
	arCopy := cloneAgentRun(ar)
	t.enqueue(func() {
		if err := t.pg.WriteAgentRun(runID, arCopy); err != nil {
			log.Printf("teestore: mirror WriteAgentRun %s/%s: %v", runID, arCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) ListAgentRuns(runID string) ([]*AgentRun, error) {
	return t.fs.ListAgentRuns(runID)
}

func (t *TeeStore) AppendCheckpointCandidate(runID string, c CheckpointCandidate) error {
	if err := t.fs.AppendCheckpointCandidate(runID, c); err != nil {
		return err
	}
	cCopy := cloneCheckpointCandidate(c)
	t.enqueue(func() {
		if err := t.pg.AppendCheckpointCandidate(runID, cCopy); err != nil {
			log.Printf("teestore: mirror AppendCheckpointCandidate %s/%s: %v", runID, cCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) ListCheckpointCandidates(runID string) ([]CheckpointCandidate, error) {
	return t.fs.ListCheckpointCandidates(runID)
}

func (t *TeeStore) AppendLedgerEvent(runID string, ev LedgerEvent) error {
	if err := t.fs.AppendLedgerEvent(runID, ev); err != nil {
		return err
	}
	evCopy := cloneLedgerEvent(ev)
	t.enqueue(func() {
		if err := t.pg.AppendLedgerEvent(runID, evCopy); err != nil {
			log.Printf("teestore: mirror AppendLedgerEvent %s/%s: %v", runID, evCopy.ID, err)
		}
	})
	return nil
}

func (t *TeeStore) ListLedgerEvents(runID string) ([]LedgerEvent, error) {
	return t.fs.ListLedgerEvents(runID)
}

// ── Document records ──────────────────────────────────────────────────────────

func (t *TeeStore) WriteBrief(runID, dispatchID string, body []byte) error {
	if err := t.fs.WriteBrief(runID, dispatchID, body); err != nil {
		return err
	}
	bodyCopy := append([]byte(nil), body...)
	t.enqueue(func() {
		if err := t.pg.WriteBrief(runID, dispatchID, bodyCopy); err != nil {
			log.Printf("teestore: mirror WriteBrief %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

func (t *TeeStore) ReadBrief(runID, dispatchID string) ([]byte, error) {
	return t.fs.ReadBrief(runID, dispatchID)
}

func (t *TeeStore) WriteReport(runID, dispatchID string, body []byte) error {
	if err := t.fs.WriteReport(runID, dispatchID, body); err != nil {
		return err
	}
	bodyCopy := append([]byte(nil), body...)
	t.enqueue(func() {
		if err := t.pg.WriteReport(runID, dispatchID, bodyCopy); err != nil {
			log.Printf("teestore: mirror WriteReport %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

func (t *TeeStore) ReadReport(runID, dispatchID string) ([]byte, error) {
	return t.fs.ReadReport(runID, dispatchID)
}

func (t *TeeStore) AppendNote(runID, author string, body []byte) (NoteRef, error) {
	ref, err := t.fs.AppendNote(runID, author, body)
	if err != nil {
		return NoteRef{}, err
	}
	bodyCopy := append([]byte(nil), body...)
	t.enqueue(func() {
		if _, err := t.pg.AppendNote(runID, author, bodyCopy); err != nil {
			log.Printf("teestore: mirror AppendNote %s/%s: %v", runID, author, err)
		}
	})
	return ref, nil
}

func (t *TeeStore) ListNotes(runID string) ([]NoteRef, error) {
	return t.fs.ListNotes(runID)
}

// ── Adapter config ─────────────────────────────────────────────────────────────

func (t *TeeStore) WriteAdapterConfig(runID, dispatchID string, cfg []byte) error {
	if err := t.fs.WriteAdapterConfig(runID, dispatchID, cfg); err != nil {
		return err
	}
	cfgCopy := append([]byte(nil), cfg...)
	t.enqueue(func() {
		if err := t.pg.WriteAdapterConfig(runID, dispatchID, cfgCopy); err != nil {
			log.Printf("teestore: mirror WriteAdapterConfig %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

func (t *TeeStore) ReadAdapterConfig(runID, dispatchID string) ([]byte, error) {
	return t.fs.ReadAdapterConfig(runID, dispatchID)
}

// ── Trace events ──────────────────────────────────────────────────────────────

func (t *TeeStore) AppendTraceEvent(runID, dispatchID string, ev TraceEvent) error {
	if err := t.fs.AppendTraceEvent(runID, dispatchID, ev); err != nil {
		return err
	}
	evCopy := ev
	t.enqueue(func() {
		if err := t.pg.AppendTraceEvent(runID, dispatchID, evCopy); err != nil {
			log.Printf("teestore: mirror AppendTraceEvent %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

// AuditSink returns the fs audit sink. The tee audit mirror happens at export
// time (tiller runs export reads the JSONL files and imports them to pg).
// We do NOT attempt to mirror individual audit events in real-time — the Sink
// interface is file-backed and the pg mirror is materialised by export.
func (t *TeeStore) AuditSink(runID, kind string) (*auditlog.Sink, io.Closer, error) {
	return t.fs.AuditSink(runID, kind)
}

// ── Materialize ───────────────────────────────────────────────────────────────

// Materialize delegates to fs (authoritative).
func (t *TeeStore) Materialize(runID, dispatchID, dir string) error {
	return t.fs.Materialize(runID, dispatchID, dir)
}

// ── Display / tree helpers ────────────────────────────────────────────────────

func (t *TeeStore) RenderTree(runID string) (string, error) {
	return t.fs.RenderTree(runID)
}

func (t *TeeStore) BuildRunSummaryJSON(runID string) ([]byte, error) {
	return t.fs.BuildRunSummaryJSON(runID)
}

func (t *TeeStore) BuildDispatchTree(runID string) (*DispatchNode, error) {
	return t.fs.BuildDispatchTree(runID)
}

// ── Claim / lease semantics (P4.1) ────────────────────────────────────────────

// ClaimDispatch performs the CAS on fs (authoritative). A win on fs is then
// mirrored to pg asynchronously. The caller receives the fs result immediately.
// Claim decisions are always fs-authoritative: a pg mirror lag cannot cause a
// double-claim because ClaimDispatch is called exactly once per claimant and
// the fs O_CREAT|O_EXCL sentinel provides the serialisation.
func (t *TeeStore) ClaimDispatch(runID, dispatchID, executor string, lease time.Duration) (bool, error) {
	won, err := t.fs.ClaimDispatch(runID, dispatchID, executor, lease)
	if err != nil || !won {
		return won, err
	}
	t.enqueue(func() {
		if _, err := t.pg.ClaimDispatch(runID, dispatchID, executor, lease); err != nil {
			log.Printf("teestore: mirror ClaimDispatch %s/%s: %v", runID, dispatchID, err)
		}
	})
	return true, nil
}

func (t *TeeStore) RenewLease(runID, dispatchID, executor string, lease time.Duration) error {
	if err := t.fs.RenewLease(runID, dispatchID, executor, lease); err != nil {
		return err
	}
	t.enqueue(func() {
		if err := t.pg.RenewLease(runID, dispatchID, executor, lease); err != nil {
			log.Printf("teestore: mirror RenewLease %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

func (t *TeeStore) ReleaseDispatch(runID, dispatchID, executor, terminalStatus string) error {
	if err := t.fs.ReleaseDispatch(runID, dispatchID, executor, terminalStatus); err != nil {
		return err
	}
	t.enqueue(func() {
		if err := t.pg.ReleaseDispatch(runID, dispatchID, executor, terminalStatus); err != nil {
			log.Printf("teestore: mirror ReleaseDispatch %s/%s: %v", runID, dispatchID, err)
		}
	})
	return nil
}

func (t *TeeStore) ExpireLeases(runID string) ([]string, error) {
	return t.fs.ExpireLeases(runID)
}

func (t *TeeStore) ListPendingDispatches(runID string) ([]*Dispatch, error) {
	return t.fs.ListPendingDispatches(runID)
}

func cloneAgentRun(ar *AgentRun) *AgentRun {
	if ar == nil {
		return nil
	}
	out := *ar
	out.ClaimedPaths = append([]string(nil), ar.ClaimedPaths...)
	out.ChangedFiles = append([]string(nil), ar.ChangedFiles...)
	out.Verification = append([]string(nil), ar.Verification...)
	out.Caveats = append([]string(nil), ar.Caveats...)
	out.Refs = append([]string(nil), ar.Refs...)
	return &out
}

func cloneCheckpointCandidate(c CheckpointCandidate) CheckpointCandidate {
	c.ClaimedPaths = append([]string(nil), c.ClaimedPaths...)
	c.ChangedFiles = append([]string(nil), c.ChangedFiles...)
	c.Verification = append([]string(nil), c.Verification...)
	c.Caveats = append([]string(nil), c.Caveats...)
	c.Refs = append([]string(nil), c.Refs...)
	return c
}

func cloneLedgerEvent(ev LedgerEvent) LedgerEvent {
	ev.Refs = append([]string(nil), ev.Refs...)
	return ev
}

// ── interface compliance ──────────────────────────────────────────────────────

// Ensure TeeStore implements scratch.Store at compile time.
var _ Store = (*TeeStore)(nil)
