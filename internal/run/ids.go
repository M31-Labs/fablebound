// Package run manages the run store: ids, directory trees, manifests, metas.
package run

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

const base36Chars = "0123456789abcdefghijklmnopqrstuvwxyz"

// NewRunID returns a run identifier of the form YYYYMMDD-HHMMSS-<4 base36>.
// The 4-character base36 suffix adds ~1.7M of entropy to disambiguate
// concurrent or rapid sequential runs.
func NewRunID() string {
	t := time.Now().UTC()
	suffix := randomBase36(4)
	return fmt.Sprintf("%s-%s", t.Format("20060102-150405"), suffix)
}

// NewDispatchID returns the next dispatch id (d01, d02, …) given the
// zero-based ordinal n (first dispatch = 0 → "d01").
func NewDispatchID(n int) string {
	return fmt.Sprintf("d%02d", n+1)
}

// NextDispatchID returns the next dNN id by counting existing numeric dispatch
// metas (ids of the form dNN), ignoring non-numeric ids like "root".
// n should be the count of existing dNN dispatches (i.e. NextDispatchID(0)="d01").
func NextDispatchID(metas []*Meta) string {
	count := 0
	for _, m := range metas {
		if isNumericDispatchID(m.ID) {
			count++
		}
	}
	return fmt.Sprintf("d%02d", count+1)
}

// isNumericDispatchID returns true for ids like "d01", "d02", etc.
func isNumericDispatchID(id string) bool {
	if len(id) < 2 || id[0] != 'd' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// nextDispatchIDFromDirs counts dNN subdirectories under <runDir>/dispatches/
// and returns the next available dNN id.  It counts directory entries rather
// than parseable metas so that a directory created by AllocDispatch but not
// yet containing a meta.json still reserves its slot.
func nextDispatchIDFromDirs(runDir string) (string, error) {
	dispDir := filepath.Join(runDir, "dispatches")
	entries, err := os.ReadDir(dispDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("d%02d", 1), nil
		}
		return "", fmt.Errorf("read dispatch dirs: %w", err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() && isNumericDispatchID(e.Name()) {
			count++
		}
	}
	return fmt.Sprintf("d%02d", count+1), nil
}

// randomBase36 returns a random lowercase base-36 string of length n.
func randomBase36(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = base36Chars[rand.Intn(len(base36Chars))]
	}
	return string(b)
}
