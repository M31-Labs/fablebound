package checkpoint

import (
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/scratch"
)

// ClassifyFreshness classifies a checkpoint candidate against the current
// worktree baseline and paths that have already been accepted since the
// candidate was reported.
func ClassifyFreshness(c scratch.CheckpointCandidate, currentBaseGitRev, currentDirtyHash string, acceptedChangedPaths []string) string {
	if overlapsAny(c.ChangedFiles, acceptedChangedPaths) {
		return scratch.CheckpointStatusConflicting
	}
	if sameBaseline(c, currentBaseGitRev, currentDirtyHash) {
		return scratch.CheckpointStatusFresh
	}
	if len(c.ChangedFiles) == 0 {
		return scratch.CheckpointStatusLateStale
	}
	return scratch.CheckpointStatusLateValid
}

func sameBaseline(c scratch.CheckpointCandidate, currentBaseGitRev, currentDirtyHash string) bool {
	return c.BaseGitRev != "" &&
		c.BaseGitRev == currentBaseGitRev &&
		c.BaseDirtyHash == currentDirtyHash
}

func overlapsAny(candidatePaths, acceptedPaths []string) bool {
	for _, candidate := range candidatePaths {
		for _, accepted := range acceptedPaths {
			if pathOverlaps(candidate, accepted) {
				return true
			}
		}
	}
	return false
}

func pathOverlaps(a, b string) bool {
	a = cleanPath(a)
	b = cleanPath(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func cleanPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if p == "." {
		return ""
	}
	return strings.TrimPrefix(p, "./")
}
