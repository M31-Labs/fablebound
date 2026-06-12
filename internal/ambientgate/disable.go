package ambientgate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DisabledRelPath = ".tiller/ambient.disabled"

func DisabledPath(projectDir string) string {
	if projectDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectDir = cwd
		}
	}
	return filepath.Join(projectDir, DisabledRelPath)
}

func IsDisabled(projectDir string) bool {
	if envDisabled() {
		return true
	}
	_, err := os.Stat(DisabledPath(projectDir))
	return err == nil
}

func Disable(projectDir string) (path string, changed bool, err error) {
	path = DisabledPath(projectDir)
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !os.IsNotExist(err) {
		return path, false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	content := fmt.Sprintf("disabled_at = %q\n", time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return path, false, fmt.Errorf("write %s: %w", path, err)
	}
	return path, true, nil
}

func Enable(projectDir string) (path string, changed bool, err error) {
	path = DisabledPath(projectDir)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return path, false, nil
		}
		return path, false, fmt.Errorf("remove %s: %w", path, err)
	}
	return path, true, nil
}

func envDisabled() bool {
	for _, name := range []string{"TILLER_AMBIENT_DISABLED", "TILLER_DISABLE_AMBIENT"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}
