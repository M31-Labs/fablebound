package scratch

import (
	"io"

	"m31labs.dev/tiller/internal/auditlog"
)

// Store is the provider-agnostic interface for all tiller run artifacts.
// It corresponds to spec.tiller-provider-agnostic §3.1 and the normative
// interface block in plan.tiller-provider-agnostic-implementation §P1.1.
//
// Single-writer discipline (spec §3.3):
//   - CreateRun / WriteRun — the run opener (cli/run.go)
//   - AllocDispatch / WriteDispatch — the dispatch requester; claimant transitions
//   - WriteBrief — the dispatch requester
//   - WriteReport — the executing agent / adapter
//   - AppendNote — the authoring agent
//   - WriteAdapterConfig — the claimant
//   - AppendTraceEvent — the agent's own adapter (PostToolUse hook)
//   - AuditSink — the deciding gate (toolgate / dispatch evaluator)
//
// Materialize is an identity on fsstore (run dir already IS the materialized form).
// It is needed for future store implementations (pgstore) where the run directory
// does not exist on disk until materialized.
type Store interface {
	// ── Run lifecycle ──────────────────────────────────────────────────────────

	// CreateRun initialises a new run (directory tree + manifest.json) from r.
	// r.ID may be pre-set; if empty a fresh ID is generated.
	// Returns the assigned run ID.
	CreateRun(r *Run) (runID string, err error)

	// ReadRun fetches the run record for runID.
	ReadRun(runID string) (*Run, error)

	// WriteRun persists a run record (status updates, budget changes, finalize).
	WriteRun(r *Run) error

	// ListRuns returns summary rows for all runs in the store.
	ListRuns() ([]RunSummary, error)

	// ── Dispatch records ───────────────────────────────────────────────────────

	// AllocDispatch atomically allocates the next dNN dispatch ID under runID,
	// creates its directory, and returns the dispatch ID.
	// Uses both an in-process mutex and an exclusive flock for safety.
	AllocDispatch(runID string) (dispatchID string, err error)

	// ReadDispatch fetches the dispatch record.
	ReadDispatch(runID, dispatchID string) (*Dispatch, error)

	// WriteDispatch persists a dispatch record (status transitions, exit codes, …).
	WriteDispatch(runID string, d *Dispatch) error

	// ListDispatches returns all dispatch records for a run, in directory order.
	ListDispatches(runID string) ([]*Dispatch, error)

	// DispatchFacts returns the aggregate active/reason counters for dispatch.arb.
	// Subsumes run.ActiveCount + run.FableCount.
	DispatchFacts(runID string) (Facts, error)

	// ── Document records ───────────────────────────────────────────────────────

	// WriteBrief writes the brief document for a dispatch (brief.md).
	WriteBrief(runID, dispatchID string, body []byte) error

	// ReadBrief reads the brief document for a dispatch.
	ReadBrief(runID, dispatchID string) ([]byte, error)

	// WriteReport writes the report document for a dispatch (report.md).
	WriteReport(runID, dispatchID string, body []byte) error

	// ReadReport reads the report document for a dispatch.
	ReadReport(runID, dispatchID string) ([]byte, error)

	// AppendNote appends a note document to notes/ and returns its reference.
	AppendNote(runID, author string, body []byte) (NoteRef, error)

	// ListNotes returns all note references for a run, ordered by filename.
	ListNotes(runID string) ([]NoteRef, error)

	// ── Adapter config ─────────────────────────────────────────────────────────

	// WriteAdapterConfig writes the adapter config (settings.json) for a dispatch.
	WriteAdapterConfig(runID, dispatchID string, cfg []byte) error

	// ── Append-only trace streams ──────────────────────────────────────────────

	// AppendTraceEvent appends one event to the appropriate JSONL trace file.
	// Kind "tool" → tool_trace.jsonl; kind "read" → context_trace.jsonl.
	// Failures must never block the caller (best-effort).
	AppendTraceEvent(runID, dispatchID string, ev TraceEvent) error

	// AuditSink opens (or creates) the per-run audit JSONL file for the given
	// kind ("dispatch" | "toolgate") and returns an audit.Sink and a Closer.
	// The caller MUST call Close when done.
	AuditSink(runID, kind string) (*auditlog.Sink, io.Closer, error)

	// ── Materialization ────────────────────────────────────────────────────────

	// Materialize ensures all run artifacts for the given dispatch are present
	// on disk at dir. On fsstore this is a no-op (the run dir IS the materialized
	// form). On pgstore it will export records to a local temp tree.
	Materialize(runID, dispatchID, dir string) error
}
