package hook

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/fsstore"
)

func refreshAmbientStatusSnapshot(runDir string, updatedAt time.Time) {
	if runDir == "" {
		return
	}
	runID := filepath.Base(runDir)
	st := fsstore.Open(filepath.Dir(runDir))
	_ = st.WriteStatusMarkdownWithOptions(runID, ambientStatusOptions(updatedAt))
}

func ambientStatusOptions(updatedAt time.Time) scratch.StatusOptions {
	return scratch.StatusOptions{
		UpdatedAt:            updatedAt,
		OutputTokenBudget:    positiveInt64Env("TILLER_AMBIENT_OUTPUT_TOKEN_BUDGET"),
		ReasoningTokenBudget: positiveInt64Env("TILLER_AMBIENT_REASONING_TOKEN_BUDGET"),
		BudgetWarnRatio:      budgetWarnRatioEnv("TILLER_AMBIENT_BUDGET_WARN_RATIO"),
	}
}

func positiveInt64Env(name string) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func budgetWarnRatioEnv(name string) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 || parsed > 1 {
		return 0
	}
	return parsed
}
