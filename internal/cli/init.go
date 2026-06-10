package cli

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/roles"
)

// runInit implements `tiller init`.
// It materializes .tiller/policy/*.arb and .tiller/roles/*.md,
// creates runs/, and appends .tiller/runs/ to the project .gitignore
// (idempotent — no duplicate lines).
func runInit(_ []string) error {
	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	fbDir := filepath.Join(projectDir, ".tiller")

	// Materialize policy defaults.
	if err := materializeDefaults(
		policy.EmbeddedDefaults(),
		filepath.Join(fbDir, "policy"),
	); err != nil {
		return fmt.Errorf("materialize policies: %w", err)
	}

	// Materialize role defaults.
	if err := materializeDefaults(
		roles.EmbeddedDefaults(),
		filepath.Join(fbDir, "roles"),
	); err != nil {
		return fmt.Errorf("materialize roles: %w", err)
	}

	// Create runs/ directory.
	runsDir := filepath.Join(fbDir, "runs")
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}

	// Append .tiller/runs/ to .gitignore (idempotent).
	if err := appendGitignore(projectDir, ".tiller/runs/"); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}

	fmt.Println("tiller init: done")
	return nil
}

// materializeDefaults copies all files from srcFS into destDir, skipping
// files that already exist (idempotent).
func materializeDefaults(srcFS fs.ReadDirFS, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}
	entries, err := srcFS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		dest := filepath.Join(destDir, entry.Name())
		if _, err := os.Stat(dest); err == nil {
			// Already exists — skip (idempotent).
			continue
		}
		data, err := fs.ReadFile(srcFS, entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
	}
	return nil
}

// appendGitignore adds line to the project .gitignore if not already present.
func appendGitignore(projectDir, line string) error {
	gitignorePath := filepath.Join(projectDir, ".gitignore")

	// Read existing lines to check for duplicates.
	existing := map[string]bool{}
	if data, err := os.ReadFile(gitignorePath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			existing[strings.TrimSpace(scanner.Text())] = true
		}
	}

	if existing[line] {
		return nil // already present
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()

	// Ensure we start on a new line.
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}
