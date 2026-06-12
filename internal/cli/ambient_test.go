package cli

import (
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/tiller/internal/ambientgate"
)

func TestRunAmbientDisableEnable(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	if err := runAmbient([]string{"disable"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	marker := filepath.Join(dir, ambientgate.DisabledRelPath)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing after disable: %v", err)
	}
	if !ambientgate.IsDisabled(dir) {
		t.Fatal("ambientgate should report disabled")
	}

	if err := runAmbient([]string{"status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := runAmbient([]string{"enable"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker should be removed after enable, stat err=%v", err)
	}
}

func TestRunAmbientRejectsUnknownCommand(t *testing.T) {
	if err := runAmbient([]string{"pause"}); err == nil {
		t.Fatal("expected unknown ambient command to fail")
	}
}
