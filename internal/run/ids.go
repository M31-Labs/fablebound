// Package run manages the run store: ids, directory trees, manifests, metas.
package run

import (
	"fmt"
	"math/rand"
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

// randomBase36 returns a random lowercase base-36 string of length n.
func randomBase36(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = base36Chars[rand.Intn(len(base36Chars))]
	}
	return string(b)
}
