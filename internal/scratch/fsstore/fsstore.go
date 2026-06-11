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
		ReasonBudget:  r.ReasonBudget,
		MaxDepth:      r.MaxDepth,
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
		ReasonBudget:  m.ReasonBudget,
		MaxDepth:      m.MaxDepth,
		CreatedAt:     m.CreatedAt,
		EndedAt:       m.EndedAt,
		RootSessionID: m.RootSessionID,
		PolicySHAs:    m.PolicySHAs,
		HyphaTraceID:  m.HyphaTraceID,
		StoreMode:     m.Store,
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
// Reads raw meta.json (via readDispatchRaw) to preserve all v2 fields
// (DenyReason, Tier, Enforcement, ClaimedBy, LeaseUntil) that run.ScanMetas
// would drop (it only knows about run.Meta fields).
func (fs *FS) ListDispatches(runID string) ([]*scratch.Dispatch, error) {
	dispatchesDir := filepath.Join(fs.runDir(runID), "dispatches")
	entries, err := os.ReadDir(dispatchesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsstore.ListDispatches %s: readdir: %w", runID, err)
	}

	var out []*scratch.Dispatch
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, err := readDispatchRaw(fs.runDir(runID), e.Name())
		if err != nil {
			continue // skip corrupt / partial writes
		}
		out = append(out, d)
	}
	// entries from os.ReadDir are already in lexicographic order (d01, d02, …).
	return out, nil
}

// DispatchFacts returns active/reason counters for dispatch.arb.
// active = status IN ("running","pending","claimed") — mirrors pgstore semantics.
func (fs *FS) DispatchFacts(runID string) (scratch.Facts, error) {
	metas, err := run.ScanMetas(fs.runDir(runID))
	if err != nil {
		return scratch.Facts{}, fmt.Errorf("fsstore.DispatchFacts %s: %w", runID, err)
	}
	var f scratch.Facts
	for _, m := range metas {
		switch m.Status {
		case "running", "pending", "claimed":
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
// It builds the tree directly from ListDispatches so that all v2 fields
// (Enforcement, Provider, Adapter, ClaimedBy, LeaseUntil, DenyReason, …) are
// preserved in the output. The previous approach went through run.BuildTree and
// run.Meta, silently dropping v2 fields that run.Meta has no knowledge of.
func (fs *FS) BuildDispatchTree(runID string) (*scratch.DispatchNode, error) {
	dispatches, err := fs.ListDispatches(runID)
	if err != nil {
		return nil, fmt.Errorf("fsstore.BuildDispatchTree %s: %w", runID, err)
	}
	return buildFSDispatchNodeTree(dispatches), nil
}

// buildFSDispatchNodeTree assembles a *scratch.DispatchNode tree from a flat
// list of dispatches, mirroring the pgstore approach. Children are sorted by ID
// to produce a deterministic order.
func buildFSDispatchNodeTree(dispatches []*scratch.Dispatch) *scratch.DispatchNode {
	byID := make(map[string]*scratch.DispatchNode, len(dispatches))
	for _, d := range dispatches {
		d := d
		byID[d.ID] = &scratch.DispatchNode{Dispatch: d}
	}

	var roots []*scratch.DispatchNode
	for _, n := range byID {
		parentID := n.Dispatch.Parent
		if parentID == "" {
			roots = append(roots, n)
		} else if parent, ok := byID[parentID]; ok {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		return fsDispNodeID(roots[i]) < fsDispNodeID(roots[j])
	})
	for _, n := range byID {
		sort.Slice(n.Children, func(i, j int) bool {
			return fsDispNodeID(n.Children[i]) < fsDispNodeID(n.Children[j])
		})
	}

	if len(roots) == 0 {
		return &scratch.DispatchNode{}
	}
	if len(roots) == 1 {
		return roots[0]
	}
	return &scratch.DispatchNode{Children: roots}
}

func fsDispNodeID(n *scratch.DispatchNode) string {
	if n.Dispatch != nil {
		return n.Dispatch.ID
	}
	return ""
}

// ── internal helpers ──────────────────────────────────────────────────────────

// writeFileSafe writes data to path crash-atomically: it writes to a sibling
// temp file in the same directory, then renames it over the destination.
// Because rename(2) is atomic on a single filesystem, readers always see
// either the complete old file or the complete new file — never a truncated
// intermediate. Parent directory is created if absent.
func writeFileSafe(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// writeFileAtomic is an alias for writeFileSafe; it writes data to path via a
// temp-file + rename so readers never observe a partially-written file.
func writeFileAtomic(path string, data []byte) error {
	return writeFileSafe(path, data)
}

// writeDispatchRaw marshals a scratch.Dispatch to meta.json with the same
// indented JSON format as run.WriteMeta. It uses scratch.Dispatch's JSON tags
// directly so that v2 fields with omitempty are included/excluded correctly,
// producing byte-identical output to run.WriteMeta for v1-only dispatch fields.
// The write is crash-atomic via a temp-file + rename; concurrent readers always
// see either the complete old or complete new meta.json.
func writeDispatchRaw(runDir string, d *scratch.Dispatch) error {
	path := filepath.Join(runDir, "dispatches", d.ID, "meta.json")
	// Ensure parent dir exists (AllocDispatch normally creates it, but defensive).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dispatch %s: %w", d.ID, err)
	}
	// Append a trailing newline to match json.Encoder.Encode output format.
	data = append(data, '\n')
	return writeFileSafe(path, data)
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
