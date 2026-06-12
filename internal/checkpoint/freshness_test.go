package checkpoint

import (
	"testing"

	"m31labs.dev/tiller/internal/scratch"
)

func TestClassifyFreshness(t *testing.T) {
	base := scratch.CheckpointCandidate{
		BaseGitRev:    "abc123",
		BaseDirtyHash: "dirty123",
		ChangedFiles:  []string{"internal/hook/hook.go", "internal/hook/ambient_test.go"},
	}

	tests := []struct {
		name     string
		c        scratch.CheckpointCandidate
		baseRev  string
		dirty    string
		accepted []string
		want     string
	}{
		{
			name:    "fresh exact baseline",
			c:       base,
			baseRev: "abc123",
			dirty:   "dirty123",
			want:    scratch.CheckpointStatusFresh,
		},
		{
			name: "fresh clean baseline",
			c: scratch.CheckpointCandidate{
				BaseGitRev:   "abc123",
				ChangedFiles: []string{"internal/hook/hook.go"},
			},
			baseRev: "abc123",
			want:    scratch.CheckpointStatusFresh,
		},
		{
			name:     "conflicting exact file",
			c:        base,
			baseRev:  "abc123",
			dirty:    "dirty456",
			accepted: []string{"internal/hook/hook.go"},
			want:     scratch.CheckpointStatusConflicting,
		},
		{
			name:     "conflicting directory overlap",
			c:        base,
			baseRev:  "abc123",
			dirty:    "dirty456",
			accepted: []string{"internal/hook"},
			want:     scratch.CheckpointStatusConflicting,
		},
		{
			name:     "conflicting normalized path overlap",
			c:        base,
			baseRev:  "abc123",
			dirty:    "dirty456",
			accepted: []string{"./internal/hook/"},
			want:     scratch.CheckpointStatusConflicting,
		},
		{
			name:     "late valid disjoint paths",
			c:        base,
			baseRev:  "abc123",
			dirty:    "dirty456",
			accepted: []string{"internal/scratch/records.go"},
			want:     scratch.CheckpointStatusLateValid,
		},
		{
			name: "late stale without changed files",
			c: scratch.CheckpointCandidate{
				BaseGitRev:    "abc123",
				BaseDirtyHash: "dirty123",
			},
			baseRev: "abc123",
			dirty:   "dirty456",
			want:    scratch.CheckpointStatusLateStale,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyFreshness(tc.c, tc.baseRev, tc.dirty, tc.accepted)
			if got != tc.want {
				t.Fatalf("ClassifyFreshness()=%q want %q", got, tc.want)
			}
		})
	}
}
