package hook

import (
	"path/filepath"
	"time"

	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func refreshAmbientStatusSnapshot(runDir string, updatedAt time.Time) {
	if runDir == "" {
		return
	}
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	_ = st.WriteStatusMarkdown(runID, updatedAt)
}
