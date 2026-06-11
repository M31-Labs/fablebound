package tier_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/tier"
)

// TestDefaultsParse verifies that the embedded default models.toml parses
// without error and produces the expected tier→candidates mapping.
func TestDefaultsParse(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}

	cases := []struct {
		name      string
		wantCount int
		wantFirst string
	}{
		{"reason", 1, "claude-headless:anthropic/fable"},
		{"scrutiny", 1, "claude-headless:anthropic/opus"},
		{"execute", 2, "claude-headless:anthropic/sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cand, err := cfg.Resolve(tc.name, 0)
			if err != nil {
				t.Fatalf("Resolve(%q, 0) error: %v", tc.name, err)
			}
			if got := cand.String(); got != tc.wantFirst {
				t.Errorf("Resolve(%q, 0) = %q, want %q", tc.name, got, tc.wantFirst)
			}
			// verify candidate count via bucket wrapping
			all := make(map[string]bool)
			for i := 0; i < tc.wantCount*2; i++ {
				c, err := cfg.Resolve(tc.name, i)
				if err != nil {
					t.Fatalf("Resolve(%q, %d) error: %v", tc.name, i, err)
				}
				all[c.String()] = true
			}
			if len(all) != tc.wantCount {
				t.Errorf("tier %q: got %d distinct candidates, want %d", tc.name, len(all), tc.wantCount)
			}
		})
	}
}

// TestCandidateParse verifies that Candidate fields are parsed correctly.
func TestCandidateParse(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	cand, err := cfg.Resolve("execute", 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cand.Adapter != "claude-headless" {
		t.Errorf("Adapter = %q, want %q", cand.Adapter, "claude-headless")
	}
	if cand.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cand.Provider, "anthropic")
	}
	if cand.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", cand.Model, "sonnet")
	}
}

// TestBucketWraps verifies bucket % len(candidates) indexing.
func TestBucketWraps(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	// execute has 2 candidates: [sonnet, haiku]
	c0, _ := cfg.Resolve("execute", 0)
	c1, _ := cfg.Resolve("execute", 1)
	c2, _ := cfg.Resolve("execute", 2) // wraps → same as c0
	if c0.String() != c2.String() {
		t.Errorf("bucket 0 and bucket 2 should be same candidate, got %q and %q", c0, c2)
	}
	if c0.String() == c1.String() {
		t.Errorf("bucket 0 and bucket 1 should be different candidates")
	}
}

// TestNegativeBucketWraps verifies that a negative bucket index does not panic
// and wraps correctly (Python-style modulo via guard).
func TestNegativeBucketWraps(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// execute has 2 candidates; bucket -1 should wrap to index 1 (len-1).
	cNeg, err := cfg.Resolve("execute", -1)
	if err != nil {
		t.Fatalf("Resolve(execute, -1) error: %v", err)
	}
	cPos, err := cfg.Resolve("execute", 1)
	if err != nil {
		t.Fatalf("Resolve(execute, 1) error: %v", err)
	}
	if cNeg.String() != cPos.String() {
		t.Errorf("bucket -1 and bucket 1 (len=2) should resolve to same candidate, got %q and %q", cNeg, cPos)
	}
	// bucket -2 should wrap to index 0.
	cNeg2, err := cfg.Resolve("execute", -2)
	if err != nil {
		t.Fatalf("Resolve(execute, -2) error: %v", err)
	}
	c0, err := cfg.Resolve("execute", 0)
	if err != nil {
		t.Fatalf("Resolve(execute, 0) error: %v", err)
	}
	if cNeg2.String() != c0.String() {
		t.Errorf("bucket -2 and bucket 0 (len=2) should resolve to same candidate, got %q and %q", cNeg2, c0)
	}
}

// TestProjectOverrideWins verifies that a project .tiller/models.toml replaces
// tiers it defines while leaving others intact.
func TestProjectOverrideWins(t *testing.T) {
	tmpDir := t.TempDir()
	tillerDir := filepath.Join(tmpDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	override := `[tiers.execute]
candidates = ["my-adapter:myprovider/mymodel"]
`
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(override), 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	cfg, err := tier.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Override tier wins.
	cand, err := cfg.Resolve("execute", 0)
	if err != nil {
		t.Fatalf("Resolve execute: %v", err)
	}
	if cand.String() != "my-adapter:myprovider/mymodel" {
		t.Errorf("execute override = %q, want %q", cand, "my-adapter:myprovider/mymodel")
	}

	// Non-overridden tiers use embedded defaults.
	cand, err = cfg.Resolve("reason", 0)
	if err != nil {
		t.Fatalf("Resolve reason: %v", err)
	}
	if cand.String() != "claude-headless:anthropic/fable" {
		t.Errorf("reason (non-overridden) = %q, want default", cand)
	}
}

// TestUnknownTierErrors verifies that Resolve on an unknown tier returns an error.
func TestUnknownTierErrors(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = cfg.Resolve("bogus-tier", 0)
	if err == nil {
		t.Fatal("expected error for unknown tier, got nil")
	}
	if !strings.Contains(err.Error(), "bogus-tier") {
		t.Errorf("error message should mention tier name, got: %v", err)
	}
}

// TestMalformedFileErrors verifies that a malformed models.toml returns an
// error that includes a 1-based line number.
func TestMalformedFileErrors(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantLine int
	}{
		{
			name: "bad_candidate_format",
			content: `[tiers.execute]
candidates = ["nocolon"]
`,
			wantLine: 2,
		},
		{
			name: "bad_section_header",
			content: `[tiers]
candidates = ["x:y/z"]
`,
			wantLine: 1,
		},
		{
			name: "malformed_array",
			content: `[tiers.execute]
candidates = "not-an-array"
`,
			wantLine: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tillerDir := filepath.Join(tmpDir, ".tiller")
			if err := os.MkdirAll(tillerDir, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(tc.content), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := tier.Load(tmpDir)
			if err == nil {
				t.Fatalf("expected parse error, got nil")
			}
			// Error must contain the exact 1-based line number.
			wantLineStr := fmt.Sprintf("line %d", tc.wantLine)
			if !strings.Contains(err.Error(), wantLineStr) {
				t.Errorf("error should contain %q, got: %v", wantLineStr, err)
			}
			t.Logf("got expected error: %v", err)
		})
	}
}

// TestAdapterSection verifies that [adapter.<name>] sections are parsed and
// accessible via Config.AdapterConfig.
func TestAdapterSection(t *testing.T) {
	tmpDir := t.TempDir()
	tillerDir := filepath.Join(tmpDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `[tiers.execute]
candidates = ["command:echo-agent/-"]

[adapter.echo-agent]
argv = ["/usr/bin/echo", "{brief}", "{report}"]
report = "stdout"
timeout = "5m"
`
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := tier.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ac := cfg.AdapterConfig("echo-agent")
	if ac == nil {
		t.Fatal("AdapterConfig(\"echo-agent\") returned nil")
	}
	wantArgv := []string{"/usr/bin/echo", "{brief}", "{report}"}
	if len(ac.Argv) != len(wantArgv) {
		t.Fatalf("Argv len = %d, want %d", len(ac.Argv), len(wantArgv))
	}
	for i, want := range wantArgv {
		if ac.Argv[i] != want {
			t.Errorf("Argv[%d] = %q, want %q", i, ac.Argv[i], want)
		}
	}
	if ac.Report != "stdout" {
		t.Errorf("Report = %q, want %q", ac.Report, "stdout")
	}
	if ac.Timeout != "5m" {
		t.Errorf("Timeout = %q, want %q", ac.Timeout, "5m")
	}
}

// TestAdapterSectionMissing verifies that AdapterConfig returns nil for unknown names.
func TestAdapterSectionMissing(t *testing.T) {
	cfg, err := tier.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.AdapterConfig("nonexistent"); got != nil {
		t.Errorf("AdapterConfig(nonexistent) = %v, want nil", got)
	}
}

// TestAdapterSectionDefaults verifies that an [adapter.<name>] with only argv
// gets sensible defaults (report="stdout", timeout="").
func TestAdapterSectionDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	tillerDir := filepath.Join(tmpDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `[tiers.execute]
candidates = ["command:minimal/-"]

[adapter.minimal]
argv = ["/usr/bin/true"]
`
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := tier.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ac := cfg.AdapterConfig("minimal")
	if ac == nil {
		t.Fatal("AdapterConfig returned nil")
	}
	if ac.Report != "stdout" {
		t.Errorf("Report default = %q, want %q", ac.Report, "stdout")
	}
	if ac.Timeout != "" {
		t.Errorf("Timeout default = %q, want %q", ac.Timeout, "")
	}
}

// TestAdapterSectionMalformedArgv verifies that a malformed argv line is rejected.
func TestAdapterSectionMalformedArgv(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantLine int
	}{
		{
			name: "argv_not_array",
			content: `[tiers.execute]
candidates = ["command:bad/-"]

[adapter.bad]
argv = "not-an-array"
`,
			wantLine: 5,
		},
		{
			name: "report_not_quoted",
			content: `[tiers.execute]
candidates = ["command:bad/-"]

[adapter.bad]
argv = ["/bin/true"]
report = unquoted
`,
			wantLine: 6,
		},
		{
			name: "unexpected_key",
			content: `[tiers.execute]
candidates = ["command:bad/-"]

[adapter.bad]
argv = ["/bin/true"]
unknown_key = "value"
`,
			wantLine: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tillerDir := filepath.Join(tmpDir, ".tiller")
			if err := os.MkdirAll(tillerDir, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(tc.content), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := tier.Load(tmpDir)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			wantLineStr := fmt.Sprintf("line %d", tc.wantLine)
			if !strings.Contains(err.Error(), wantLineStr) {
				t.Errorf("error should contain %q, got: %v", wantLineStr, err)
			}
			t.Logf("got expected error: %v", err)
		})
	}
}

// TestDashModelIsLegal verifies that a model name of "-" is accepted.
func TestDashModelIsLegal(t *testing.T) {
	tmpDir := t.TempDir()
	tillerDir := filepath.Join(tmpDir, ".tiller")
	if err := os.MkdirAll(tillerDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `[tiers.execute]
candidates = ["cmd:-/cmd-name"]
`
	if err := os.WriteFile(filepath.Join(tillerDir, "models.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := tier.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load with dash model: %v", err)
	}
	cand, err := cfg.Resolve("execute", 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cand.Provider != "-" {
		t.Errorf("Provider = %q, want %q", cand.Provider, "-")
	}
}
