// Package storeutil provides the provider-agnostic scratch.Store resolver.
//
// It lives in a separate package from scratch to avoid an import cycle:
// scratch ← fsstore/pgstore ← scratch. storeutil imports all three.
//
// Resolution order (normative, spec §5.1 / plan P3.4):
//  1. opts.StoreKind (from --store flag)
//  2. TILLER_STORE env
//  3. default: "fs"
//
// Valid store names: fs | pg | tee
//
// For "pg" and "tee", TILLER_STORE_DSN (or opts.DSN) must be set.
//
// Hot-path guard: when TILLER_RUN_DIR is set (hook / child dispatch
// invocations), Resolve ALWAYS opens an fsstore regardless of TILLER_STORE —
// the hook evaluates toolgate locally and must never touch the network.
package storeutil

import (
	"context"
	"fmt"
	"os"

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
// When TILLER_RUN_DIR is set the function always returns an fsstore, bypassing
// TILLER_STORE entirely. This is the hot-path guard: hook / child dispatch
// invocations resolve identity via fsstore semantics and must never block on
// a network dial.
//
// opts may be nil (uses env only).
func Resolve(opts *Options) (scratch.Store, string, func() error, error) {
	if opts == nil {
		opts = &Options{}
	}

	// ── Hot-path guard: TILLER_RUN_DIR is set ───────────────────────────────
	// hook / child dispatch paths always use fsstore regardless of TILLER_STORE.
	if runDir := os.Getenv("TILLER_RUN_DIR"); runDir != "" {
		st, runID, err := fsstore.Resolve()
		if err != nil {
			return nil, "", nil, err
		}
		return st, runID, nil, nil
	}

	// ── Determine store kind ─────────────────────────────────────────────────
	kind := opts.StoreKind
	if kind == "" {
		kind = os.Getenv("TILLER_STORE")
	}
	if kind == "" {
		kind = "fs" // default
	}

	// ── Determine DSN ────────────────────────────────────────────────────────
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
