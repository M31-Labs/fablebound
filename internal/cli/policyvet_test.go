package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyVet_DefaultPoliciesCompile verifies that the embedded default
// policies compile cleanly under policyVet (no arbiter CLI required for the
// compile+hash phase).
func TestPolicyVet_DefaultPoliciesCompile(t *testing.T) {
	// Run from a temp dir so there is no project-local .tiller/policy override.
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// policyVet must succeed (embedded defaults compile) and print policy hashes.
	if err := policyVet(); err != nil {
		t.Fatalf("policyVet() returned error: %v", err)
	}
}

// TestPolicyVet_CorruptPolicyFails verifies that a malformed project-local
// policy file causes policyVet to return an error.
func TestPolicyVet_CorruptPolicyFails(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	policyDir := filepath.Join(tmpDir, ".tiller", "policy")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write an invalid arb file.
	badArb := filepath.Join(policyDir, "dispatch.arb")
	if err := os.WriteFile(badArb, []byte("this is not valid arbiter syntax!"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := policyVet(); err == nil {
		t.Fatal("policyVet() expected error for corrupt dispatch.arb, got nil")
	}
}

// TestPolicyVet_ArbiterAbsentExits0 verifies that when the arbiter binary is
// not on PATH, policyVet still exits 0 (with a warning) after the compile step.
func TestPolicyVet_ArbiterAbsentExits0(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	origPATH := os.Getenv("PATH")
	t.Cleanup(func() {
		os.Chdir(origDir)
		os.Setenv("PATH", origPATH)
	})
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Remove arbiter from PATH by pointing to an empty directory.
	emptyBin := t.TempDir()
	os.Setenv("PATH", emptyBin)

	// policyVet must succeed (exits 0) even with no arbiter binary.
	if err := policyVet(); err != nil {
		t.Fatalf("policyVet() returned error with arbiter absent: %v", err)
	}
}

// TestMaterializePolicyFiles verifies that the temp dir contains all expected
// files when using the embedded defaults.
func TestMaterializePolicyFiles(t *testing.T) {
	tmpDir := t.TempDir()

	_, schemaFile, arbFiles, testFiles, cleanup, err := materializePolicyFiles(tmpDir)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("materializePolicyFiles: %v", err)
	}

	// schemas.go must exist.
	if schemaFile == "" {
		t.Fatal("schemaFile is empty")
	}
	if _, err := os.Stat(schemaFile); err != nil {
		t.Fatalf("schemas.go not materialized: %v", err)
	}
	data, _ := os.ReadFile(schemaFile)
	if !strings.Contains(string(data), "DispatchRequest") {
		t.Error("schemas.go does not contain DispatchRequest")
	}

	// arb files must exist for all kinds.
	for _, kind := range policyKinds {
		f, ok := arbFiles[kind]
		if !ok || f == "" {
			t.Errorf("arbFiles[%s] is empty", kind)
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("arb file for %s not found: %v", kind, err)
		}
	}

	// test.arb files must exist for all kinds.
	for _, kind := range policyKinds {
		f, ok := testFiles[kind]
		if !ok || f == "" {
			t.Errorf("testFiles[%s] is empty", kind)
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("test.arb file for %s not found: %v", kind, err)
		}
		// Each test file must live next to its arb file (arbiter test bundlePath
		// assumption: strip .test.arb → .arb).
		expectedArb := strings.TrimSuffix(f, ".test.arb") + ".arb"
		if _, err := os.Stat(expectedArb); err != nil {
			t.Errorf("arb file not co-located with test.arb for %s: %v", kind, err)
		}
	}
}
