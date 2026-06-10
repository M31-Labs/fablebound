// Package storeutil provides the provider-agnostic scratch.Store resolver.
//
// It lives in a separate package from scratch to avoid an import cycle:
// scratch ← fsstore/pgstore ← scratch. storeutil imports all three.
//
// Resolution order (normative, spec §5.1 / plan P3.4):
//  1. opts.StoreKind (from --store flag)
//  2. TILLER_STORE env
//  3. manifest `store` field (children inherit — requires TILLER_RUN_DIR)
//  4. default: "fs"
//
// Valid store names: fs | pg | tee
//
// For "pg" and "tee", TILLER_STORE_DSN (or opts.DSN) must be set.
//
// Hot-path guard: the hook (tiller hook / internal/hook) MUST never call
// Resolve — it uses fsstore.Open directly and never dials. Resolve is safe
// for dispatch and supervise child processes: when TILLER_RUN_DIR is set and
// the manifest says tee/pg, Resolve opens the full store. If the DSN env is
// missing or the pg dial fails, it soft-fails to fsstore with a log line (fs
// is authoritative).
package storeutil

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"m31labs.dev/tiller/internal/run"
	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
	"m31labs.dev/tiller/internal/scratch/pgstore"
)

// Options carries optional overrides for Resolve.
type Options struct {
	// StoreKind overrides TILLER_STORE (e.g. from --store flag).
	// Valid: "fs" | "pg" | "tee" | "" (inherit from env / default).
	StoreKind string

	// DSN overrides TILLER_STORE_DSN. Required when StoreKind is "pg" or "tee".
	DSN string
}

// Resolve opens a scratch.Store for the current invocation.
// Returns the store, the current run ID, and a closer (nil for fsstore).
//
// Resolution order:
//  1. opts.StoreKind (from --store flag)
//  2. TILLER_STORE env
//  3. manifest `store` field (when TILLER_RUN_DIR is set)
//  4. default: "fs"
//
// When TILLER_RUN_DIR is set AND opts.StoreKind/TILLER_STORE are both empty,
// the manifest's store field is consulted. If it says "tee" or "pg", the
// corresponding store is opened using TILLER_STORE_DSN (inherited through
// spawn). If the DSN is missing or the dial fails, resolution soft-falls back
// to fsstore with a log line — fs is always authoritative.
//
// NOTE: The hook (internal/hook) must NOT call Resolve; it uses fsstore.Open
// directly. This is enforced by convention: the hook package has no import of
// storeutil.
//
// opts may be nil (uses env only).
func Resolve(opts *Options) (scratch.Store, string, func() error, error) {
	if opts == nil {
		opts = &Options{}
	}

	// ── Determine explicit store kind (flag or env) ──────────────────────────
	kind := opts.StoreKind
	if kind == "" {
		kind = os.Getenv("TILLER_STORE")
	}

	// ── Child context: TILLER_RUN_DIR is set ────────────────────────────────
	// When we have a run dir AND no explicit store kind, consult the manifest.
	if runDir := os.Getenv("TILLER_RUN_DIR"); runDir != "" {
		if kind == "" {
			// Read the manifest to discover the parent's store selection.
			// Soft-fail: if unreadable, fall through to fs default.
			if m, err := run.ReadManifest(runDir); err == nil && m.Store != "" {
				kind = m.Store
			}
		}

		// Determine DSN.
		dsn := opts.DSN
		if dsn == "" {
			dsn = os.Getenv("TILLER_STORE_DSN")
		}

		// Open fs side (always needed as the authoritative copy + runID source).
		runsBase := filepath.Dir(runDir)
		runID := filepath.Base(runDir)
		fst := fsstore.Open(runsBase)

		switch kind {
		case "pg":
			if dsn == "" {
				log.Printf("storeutil.Resolve: TILLER_STORE=pg but TILLER_STORE_DSN is unset; falling back to fsstore")
				return fst, runID, nil, nil
			}
			pg, err := pgstore.OpenStore(context.Background(), dsn)
			if err != nil {
				log.Printf("storeutil.Resolve: open pgstore: %v; falling back to fsstore", err)
				return fst, runID, nil, nil
			}
			closer := func() error { return pg.Close() }
			return pg, runID, closer, nil

		case "tee":
			if dsn == "" {
				log.Printf("storeutil.Resolve: TILLER_STORE=tee but TILLER_STORE_DSN is unset; falling back to fsstore")
				return fst, runID, nil, nil
			}
			pg, err := pgstore.OpenStore(context.Background(), dsn)
			if err != nil {
				log.Printf("storeutil.Resolve: open pgstore for tee: %v; falling back to fsstore", err)
				return fst, runID, nil, nil
			}
			tee := scratch.NewTeeStore(fst, pg)
			closer := func() error {
				closeErr := tee.Close()
				_ = pg.Close()
				return closeErr
			}
			return tee, runID, closer, nil

		default:
			// "fs" or unknown — fsstore only.
			return fst, runID, nil, nil
		}
	}

	// ── Top-level (no TILLER_RUN_DIR) ────────────────────────────────────────
	if kind == "" {
		kind = "fs" // default
	}

	// Determine DSN.
	dsn := opts.DSN
	if dsn == "" {
		dsn = os.Getenv("TILLER_STORE_DSN")
	}

	// ── Open the appropriate backend ─────────────────────────────────────────
	switch kind {
	case "fs", "":
		st, runID, err := fsstore.Resolve()
		if err != nil {
			return nil, "", nil, err
		}
		return st, runID, nil, nil

	case "pg":
		if dsn == "" {
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: TILLER_STORE=pg requires TILLER_STORE_DSN")
		}
		pg, err := pgstore.OpenStore(context.Background(), dsn)
		if err != nil {
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: open pgstore: %w", err)
		}
		closer := func() error { return pg.Close() }
		return pg, "", closer, nil

	case "tee":
		if dsn == "" {
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: TILLER_STORE=tee requires TILLER_STORE_DSN")
		}
		// fs side uses the same resolution as plain "fs".
		fs, _, err := fsstore.Resolve()
		if err != nil {
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: open fsstore for tee: %w", err)
		}
		pg, err := pgstore.OpenStore(context.Background(), dsn)
		if err != nil {
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: open pgstore for tee: %w", err)
		}
		tee := scratch.NewTeeStore(fs, pg)
		closer := func() error {
			err := tee.Close()
			_ = pg.Close()
			return err
		}
		// runID from fsstore (fs is authoritative).
		_, runID, err2 := fsstore.Resolve()
		if err2 != nil {
			_ = closer()
			return nil, "", nil, fmt.Errorf("storeutil.Resolve: resolve runID for tee: %w", err2)
		}
		return tee, runID, closer, nil

	default:
		return nil, "", nil, fmt.Errorf("storeutil.Resolve: unknown store kind %q (want fs|pg|tee)", kind)
	}
}

// ResolveForRun opens the Store and resolves runID.
// If runID is non-empty it is used as-is; otherwise the ID is taken from
// TILLER_RUN_DIR (fsstore.Resolve semantics).
func ResolveForRun(runID string, opts *Options) (scratch.Store, string, func() error, error) {
	st, currentID, closer, err := Resolve(opts)
	if err != nil {
		return nil, "", nil, err
	}
	if runID != "" {
		return st, runID, closer, nil
	}
	if currentID == "" {
		if closer != nil {
			_ = closer()
		}
		return nil, "", nil, fmt.Errorf("storeutil.ResolveForRun: no run context (TILLER_RUN_DIR unset) and no runID provided")
	}
	return st, currentID, closer, nil
}
