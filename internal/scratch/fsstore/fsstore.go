// Package fsstore implements scratch.Store on the local filesystem.
//
// It produces byte-identical run-directory layouts to today's .tiller/runs/<id>/
// structure by delegating directly to the internal/run and internal/auditlog
// packages — no logic is duplicated. The fsstore is the reference implementation;
// all existing call sites continue to use internal/run directly until P1.4.
//
// Layout produced (identical to v1):
//
//	<base>/<runID>/
//	  manifest.json
//	  notes/
//	  audit/
//	    dispatch.jsonl
//	    toolgate.jsonl
//	  dispatches/
//	    <dNN>/
//	      meta.json
//	      brief.md
//	      report.md
//	      settings.json
//	      tool_trace.jsonl
//	      context_trace.jsonl
package fsstore

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/auditlog"
	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/scratch"
)

// FS implements scratch.Store on the local filesystem.
// baseDir is the absolute path to the runs/ directory (e.g. <workspace>/.tiller/runs).
type FS struct {
	baseDir string
	inner   *run.Store
}

// Open returns an FS store rooted at baseDir.
// baseDir must already exist or be creatable; Open does not create it.
func Open(baseDir string) *FS {
	return &FS{
		baseDir: baseDir,
		inner:   run.NewStore(baseDir),
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (fs *FS) runDir(runID string) string {
	return fs.inner.RunDir(runID)
}

func (fs *FS) dispatchDir(runID, dispatchID string) string {
	return fs.inner.DispatchDir(runID, dispatchID)
}

// runToManifest converts a scratch.Run to the internal run.Manifest shape.
func runToManifest(r *scratch.Run) *run.Manifest {
	return &run.Manifest{
		RunID:         r.ID,
		Task:          r.Task,
		Workspace:     r.Workspace,
		Status:        r.Status,
		FableBudget:   r.FableBudget,
		CreatedAt:     r.CreatedAt,
		EndedAt:       r.EndedAt,
		RootSessionID: r.RootSessionID,
		PolicySHAs:    r.PolicySHAs,
		HyphaTraceID:  r.HyphaTraceID,
		Store:         r.StoreMode,
	}
}

// manifestToRun converts a run.Manifest to scratch.Run.
func manifestToRun(m *run.Manifest) *scratch.Run {
	return &scratch.Run{
		ID:            m.RunID,
		Task:          m.Task,
		Workspace:     m.Workspace,
		Status:        m.Status,
		FableBudget:   m.FableBudget,
		CreatedAt:     m.CreatedAt,
		EndedAt:       m.EndedAt,
		RootSessionID: m.RootSessionID,
		PolicySHAs:    m.PolicySHAs,
		HyphaTraceID:  m.HyphaTraceID,
		StoreMode:     m.Store,
	}
}

// dispatchToMeta converts a scratch.Dispatch to the internal run.Meta shape.
// v2-only fields (Tier, Enforcement, ClaimedBy, LeaseUntil) are stored in meta.json
// via the scratch.Dispatch struct's JSON tags, not via run.Meta — they are written
// by writeDispatchRaw which marshals the full scratch.Dispatch directly.
func dispatchToMeta(d *scratch.Dispatch) *run.Meta {
	return &run.Meta{
		ID:             d.ID,
		Parent:         d.Parent,
		Role:           d.Role,
		Model:          d.Model,
		Profile:        d.Profile,
		Status:         d.Status,
		Depth:          d.Depth,
		SupervisorPID:  d.SupervisorPID,
		MaxTurns:       d.MaxTurns,
		TimeoutMinutes: d.TimeoutMinutes,
		StartedAt:      d.StartedAt,
		EndedAt:        d.EndedAt,
		Exit:           d.Exit,
		CostUSD:        d.CostUSD,
		NumTurns:       d.NumTurns,
		SessionID:      d.SessionID,
	}
}

// metaToDispatch converts a run.Meta to scratch.Dispatch.
func metaToDispatch(m *run.Meta) *scratch.Dispatch {
	return &scratch.Dispatch{
		ID:             m.ID,
		Parent:         m.Parent,
		Role:           m.Role,
		Model:          m.Model,
		Profile:        m.Profile,
		Status:         m.Status,
		Depth:          m.Depth,
		SupervisorPID:  m.SupervisorPID,
		MaxTurns:       m.MaxTurns,
		TimeoutMinutes: m.TimeoutMinutes,
		StartedAt:      m.StartedAt,
		EndedAt:        m.EndedAt,
		Exit:           m.Exit,
		CostUSD:        m.CostUSD,
		NumTurns:       m.NumTurns,
		SessionID:      m.SessionID,
	}
}

// ── Run lifecycle ─────────────────────────────────────────────────────────────

// CreateRun initialises a new run directory and writes manifest.json.
// If r.ID is empty a fresh run ID is generated.
func (fs *FS) CreateRun(r *scratch.Run) (string, error) {
	if err := fs.inner.EnsureBase(); err != nil {
		return "", fmt.Errorf("fsstore.CreateRun: ensure base: %w", err)
	}
	id := r.ID
	if id == "" {
		id = run.NewRunID()
	}
	if err := fs.inner.CreateRunWithID(id); err != nil {
		return "", fmt.Errorf("fsstore.CreateRun: %w", err)
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	m := runToManifest(r)
	if err := run.WriteManifest(fs.runDir(id), m); err != nil {
		return "", fmt.Errorf("fsstore.CreateRun: write manifest: %w", err)
	}
	return id, nil
}

// ReadRun fetches the run record for runID.
func (fs *FS) ReadRun(runID string) (*scratch.Run, error) {
	m, err := run.ReadManifest(fs.runDir(runID))
	if err != nil {
		return nil, fmt.Errorf("fsstore.ReadRun %s: %w", runID, err)
	}
	return manifestToRun(m), nil
}

// WriteRun persists run status, budget, and finalization changes.
func (fs *FS) WriteRun(r *scratch.Run) error {
	m := runToManifest(r)
	if err := run.WriteManifest(fs.runDir(r.ID), m); err != nil {
		return fmt.Errorf("fsstore.WriteRun %s: %w", r.ID, err)
	}
	return nil
}

// ListRuns scans the base directory and returns summary rows.
func (fs *FS) ListRuns() ([]scratch.RunSummary, error) {
	items, err := run.ListRuns(fs.baseDir)
	if err != nil {
		return nil, fmt.Errorf("fsstore.ListRuns: %w", err)
	}
	out := make([]scratch.RunSummary, len(items))
	for i, item := range items {
		out[i] = scratch.RunSummary{
			RunID:         item.RunID,
			Status:        item.Status,
			TaskFirstLine: item.TaskFirstLine,
			DispatchCount: item.DispatchCount,
			TotalCostUSD:  item.TotalCostUSD,
		}
	}
	return out, nil
}

// ── Dispatch records ──────────────────────────────────────────────────────────

// AllocDispatch atomically allocates the next dNN dispatch ID under runID.
func (fs *FS) AllocDispatch(runID string) (string, error) {
	id, _, err := fs.inner.AllocDispatch(runID)
	if err != nil {
		return "", fmt.Errorf("fsstore.AllocDispatch %s: %w", runID, err)
	}
	return id, nil
}

// ReadDispatch reads a dispatch record from meta.json.
// It reads the raw JSON into a scratch.Dispatch so that v2 fields (Tier, etc.)
// are preserved even if the underlying run.Meta doesn't know about them.
func (fs *FS) ReadDispatch(runID, dispatchID string) (*scratch.Dispatch, error) {
	d, err := readDispatchRaw(fs.runDir(runID), dispatchID)
	if err != nil {
		return nil, fmt.Errorf("fsstore.ReadDispatch %s/%s: %w", runID, dispatchID, err)
	}
	return d, nil
}

// WriteDispatch persists a dispatch record to meta.json.
// It marshals the full scratch.Dispatch (including v2 fields with omitempty)
// so that v1-only dispatches produce byte-identical output to run.WriteMeta.
func (fs *FS) WriteDispatch(runID string, d *scratch.Dispatch) error {
	if err := writeDispatchRaw(fs.runDir(runID), d); err != nil {
		return fmt.Errorf("fsstore.WriteDispatch %s/%s: %w", runID, d.ID, err)
	}
	return nil
}

// ListDispatches returns all dispatch records for a run.
func (fs *FS) ListDispatches(runID string) ([]*scratch.Dispatch, error) {
	metas, err := run.ScanMetas(fs.runDir(runID))
	if err != nil {
		return nil, fmt.Errorf("fsstore.ListDispatches %s: %w", runID, err)
	}
	out := make([]*scratch.Dispatch, len(metas))
	for i, m := range metas {
		out[i] = metaToDispatch(m)
	}
	return out, nil
}

// DispatchFacts returns active/reason counters for dispatch.arb.
func (fs *FS) DispatchFacts(runID string) (scratch.Facts, error) {
	metas, err := run.ScanMetas(fs.runDir(runID))
	if err != nil {
		return scratch.Facts{}, fmt.Errorf("fsstore.DispatchFacts %s: %w", runID, err)
	}
	var f scratch.Facts
	for _, m := range metas {
		if m.Status == "running" {
			f.Active++
		}
		if m.IsFableModel() {
			f.ReasonCount++
		}
	}
	return f, nil
}

// ── Document records ──────────────────────────────────────────────────────────

// WriteBrief writes body to dispatches/<dispatchID>/brief.md.
func (fs *FS) WriteBrief(runID, dispatchID string, body []byte) error {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "brief.md")
	return writeFileAtomic(path, body)
}

// ReadBrief reads dispatches/<dispatchID>/brief.md.
func (fs *FS) ReadBrief(runID, dispatchID string) ([]byte, error) {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "brief.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fsstore.ReadBrief %s/%s: %w", runID, dispatchID, err)
	}
	return data, nil
}

// WriteReport writes body to dispatches/<dispatchID>/report.md.
func (fs *FS) WriteReport(runID, dispatchID string, body []byte) error {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "report.md")
	return writeFileAtomic(path, body)
}

// ReadReport reads dispatches/<dispatchID>/report.md.
func (fs *FS) ReadReport(runID, dispatchID string) ([]byte, error) {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "report.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fsstore.ReadReport %s/%s: %w", runID, dispatchID, err)
	}
	return data, nil
}

// AppendNote writes body to a new timestamped file under notes/ and returns its ref.
func (fs *FS) AppendNote(runID, author string, body []byte) (scratch.NoteRef, error) {
	notesDir := filepath.Join(fs.runDir(runID), "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return scratch.NoteRef{}, fmt.Errorf("fsstore.AppendNote: mkdir notes: %w", err)
	}

	// Generate a sortable filename: <timestamp>-<author>.md
	now := time.Now().UTC()
	safeAuthor := strings.NewReplacer("/", "-", " ", "-").Replace(author)
	filename := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405.000000000"), safeAuthor)
	path := filepath.Join(notesDir, filename)

	if err := os.WriteFile(path, body, 0o644); err != nil {
		return scratch.NoteRef{}, fmt.Errorf("fsstore.AppendNote: write %s: %w", path, err)
	}

	return scratch.NoteRef{
		Filename:  filename,
		Author:    author,
		WrittenAt: now,
	}, nil
}

// ListNotes returns all note references for a run, ordered by filename.
func (fs *FS) ListNotes(runID string) ([]scratch.NoteRef, error) {
	notesDir := filepath.Join(fs.runDir(runID), "notes")
	entries, err := os.ReadDir(notesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsstore.ListNotes %s: %w", runID, err)
	}

	var refs []scratch.NoteRef
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		refs = append(refs, scratch.NoteRef{
			Filename:  e.Name(),
			WrittenAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Filename < refs[j].Filename
	})
	return refs, nil
}

// ── Adapter config ────────────────────────────────────────────────────────────

// WriteAdapterConfig writes cfg to dispatches/<dispatchID>/settings.json.
func (fs *FS) WriteAdapterConfig(runID, dispatchID string, cfg []byte) error {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "settings.json")
	return writeFileAtomic(path, cfg)
}

// ── Trace streams ─────────────────────────────────────────────────────────────

// AppendTraceEvent appends ev to the appropriate JSONL trace file.
// Kind "tool" → tool_trace.jsonl; kind "read" → context_trace.jsonl.
// All other kinds → tool_trace.jsonl (forward compat).
// Failures are non-fatal; the caller should log but not block.
func (fs *FS) AppendTraceEvent(runID, dispatchID string, ev scratch.TraceEvent) error {
	dispDir := fs.dispatchDir(runID, dispatchID)
	if err := os.MkdirAll(dispDir, 0o755); err != nil {
		return fmt.Errorf("fsstore.AppendTraceEvent: mkdir: %w", err)
	}

	var path string
	switch ev.Kind {
	case "read", "dispatch", "report":
		// Context-trace events: context reads, child dispatches, and reports.
		path = filepath.Join(dispDir, "context_trace.jsonl")
	default:
		// Tool-trace events: tool invocations.
		path = filepath.Join(dispDir, "tool_trace.jsonl")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("fsstore.AppendTraceEvent: open %s: %w", path, err)
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(ev)
}

// AuditSink opens the per-run audit JSONL file for the given kind and returns
// an auditlog.Sink and a no-op io.Closer (the sink closes on each write).
// kind must be "dispatch" or "toolgate".
func (fs *FS) AuditSink(runID, kind string) (*auditlog.Sink, io.Closer, error) {
	if kind != "dispatch" && kind != "toolgate" {
		return nil, nil, fmt.Errorf("fsstore.AuditSink: unknown kind %q (want dispatch|toolgate)", kind)
	}
	auditDir := filepath.Join(fs.runDir(runID), "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("fsstore.AuditSink: mkdir %s: %w", auditDir, err)
	}
	path := filepath.Join(auditDir, kind+".jsonl")
	sink, err := auditlog.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("fsstore.AuditSink: %w", err)
	}
	// auditlog.Sink is a no-op closer (file is opened/closed per write).
	return sink, io.NopCloser(nil), nil
}

// ── Materialization ───────────────────────────────────────────────────────────

// Materialize is a no-op on fsstore: the run directory on disk is the
// materialized form already. The dir argument is ignored.
func (fs *FS) Materialize(runID, dispatchID, dir string) error {
	return nil
}

// ReadAdapterConfig reads dispatches/<dispatchID>/settings.json.
func (fs *FS) ReadAdapterConfig(runID, dispatchID string) ([]byte, error) {
	path := filepath.Join(fs.dispatchDir(runID, dispatchID), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fsstore.ReadAdapterConfig %s/%s: %w", runID, dispatchID, err)
	}
	return data, nil
}

// RenderTree renders the dispatch tree for runID as a human-readable string.
func (fs *FS) RenderTree(runID string) (string, error) {
	tree, err := run.RenderTree(fs.runDir(runID))
	if err != nil {
		return "", fmt.Errorf("fsstore.RenderTree %s: %w", runID, err)
	}
	return tree, nil
}

// BuildRunSummaryJSON builds the derived run summary and returns it as
// indented JSON bytes. Delegates to run.BuildRunSummary.
func (fs *FS) BuildRunSummaryJSON(runID string) ([]byte, error) {
	summary, err := run.BuildRunSummary(fs.runDir(runID))
	if err != nil {
		return nil, fmt.Errorf("fsstore.BuildRunSummaryJSON %s: %w", runID, err)
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fsstore.BuildRunSummaryJSON %s: marshal: %w", runID, err)
	}
	return data, nil
}

// BuildDispatchTree returns the full dispatch tree for runID as a *scratch.DispatchNode.
// It converts the run.Node tree to scratch.DispatchNode so callers need not import internal/run.
func (fs *FS) BuildDispatchTree(runID string) (*scratch.DispatchNode, error) {
	root, err := run.BuildTree(fs.runDir(runID))
	if err != nil {
		return nil, fmt.Errorf("fsstore.BuildDispatchTree %s: %w", runID, err)
	}
	return runNodeToDispatchNode(root), nil
}

// runNodeToDispatchNode recursively converts a run.Node to a scratch.DispatchNode.
func runNodeToDispatchNode(n *run.Node) *scratch.DispatchNode {
	if n == nil {
		return &scratch.DispatchNode{}
	}
	dn := &scratch.DispatchNode{}
	if n.Meta != nil {
		dn.Dispatch = metaToDispatch(n.Meta)
	}
	for _, child := range n.Children {
		dn.Children = append(dn.Children, runNodeToDispatchNode(child))
	}
	return dn
}

// ── internal helpers ──────────────────────────────────────────────────────────

// writeFileAtomic writes data to path with an exclusive flock, creating or
// truncating the file. Uses the same flock discipline as run.flockWrite.
func writeFileAtomic(path string, data []byte) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, data, 0o644)
}

// writeDispatchRaw marshals a scratch.Dispatch to meta.json with the same
// indented JSON format as run.WriteMeta. It uses scratch.Dispatch's JSON tags
// directly so that v2 fields with omitempty are included/excluded correctly,
// producing byte-identical output to run.WriteMeta for v1-only dispatch fields.
func writeDispatchRaw(runDir string, d *scratch.Dispatch) error {
	path := filepath.Join(runDir, "dispatches", d.ID, "meta.json")
	// Ensure parent dir exists (AllocDispatch normally creates it, but defensive).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// readDispatchRaw reads meta.json into a scratch.Dispatch. By decoding into
// scratch.Dispatch directly, v2 fields (Tier, Enforcement, etc.) are preserved
// on round-trip even though run.ReadMeta would discard them.
func readDispatchRaw(runDir, dispatchID string) (*scratch.Dispatch, error) {
	path := filepath.Join(runDir, "dispatches", dispatchID, "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d scratch.Dispatch
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
