package claudecode

// detect_test.go: white-box tests for DetectTier hardening.
// These tests live in package claudecode (not claudecode_test) to access the
// unexported helpers directly, mirroring the original ambient_test.go in
// internal/hook.
//
// Rule 2 vs Rule 5 reconciliation:
//   Rule 2 (isQualifyingAssistantLine): sidechain lines are filtered out entirely,
//   so they never become "the last qualifier".
//   Rule 5: if ONLY sidechain lines exist and no root qualifier is found,
//   DetectTier returns ("", false) — fail open, no enforcement.
//   The "sidechain_after_root_fable" case exercises the typical mixed scenario:
//   rule 2 filters the trailing sidechain line, the root fable line is the last
//   qualifier → returns ("reason", true). Consistent with rules 2+5.

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func fixturesDir(t *testing.T) string {
	t.Helper()
	// Fixtures are shared with the hook package — use relative path from the
	// module root.  Tests run with cwd == the package directory.
	p := filepath.Join("..", "..", "hook", "testdata")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("hook testdata not found at %s: %v", p, err)
	}
	return p
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(fixturesDir(t), name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("testdata fixture not found: %s", p)
	}
	return p
}

// ─── Fix 1: <synthetic> skip ────────────────────────────────────────────────

// TestSyntheticSkip: trailing <synthetic> after real fable line must NOT
// suppress fable detection.
func TestSyntheticSkip(t *testing.T) {
	p := fixturePath(t, "fable_then_synthetic.jsonl")
	tier, ok := DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (synthetic must not suppress fable detection)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// ─── Fix 2: isSidechain guard ────────────────────────────────────────────────

// TestSidechainAfterRootFable: a sidechain assistant line after a root fable
// line must be filtered; root fable line must win.
func TestSidechainAfterRootFable(t *testing.T) {
	p := fixturePath(t, "sidechain_after_root_fable.jsonl")
	tier, ok := DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (sidechain sonnet must not override root fable)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// TestSidechainOnly: when transcript contains ONLY sidechain assistant lines
// and no root qualifier, must return ("", false) → fail open.
func TestSidechainOnly(t *testing.T) {
	p := fixturePath(t, "sidechain_only.jsonl")
	tier, ok := DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false (sidechain-only must not trigger enforcement)")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty (sidechain-only must yield no result)", tier)
	}
}

// ─── Fix 3 + Fix 4: large line + full-scan fallback ─────────────────────────

// TestLargeLineThenFable: a >64 KB line followed by a root fable assistant
// line must not cause the scanner to fail open.
func TestLargeLineThenFable(t *testing.T) {
	p := fixturePath(t, "large_line_then_fable.jsonl")
	tier, ok := DetectTier(p)
	if !ok {
		t.Errorf("got ok=false, want true (large line must be skipped, not abort scan)")
	}
	if tier != "reason" {
		t.Errorf("got tier=%q, want reason", tier)
	}
}

// ─── Model switch (fable → opus) ─────────────────────────────────────────────

// TestFableThenOpus: after a /model switch from fable to opus, no fable tier.
func TestFableThenOpus(t *testing.T) {
	p := fixturePath(t, "fable_then_opus.jsonl")
	tier, ok := DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false (opus is not fable)")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty (model switch to opus must clear detection)", tier)
	}
}

// ─── First turn / empty transcript ───────────────────────────────────────────

// TestFirstTurnNoAssistant: a transcript with no assistant line yet → fail open.
func TestFirstTurnNoAssistant(t *testing.T) {
	p := fixturePath(t, "first_turn_no_assistant.jsonl")
	tier, ok := DetectTier(p)
	if ok {
		t.Errorf("got ok=true, want false for first-turn transcript")
	}
	if tier != "" {
		t.Errorf("got tier=%q, want empty for first-turn transcript", tier)
	}
}

// TestMissingTranscriptPath: empty transcript_path → ("", false).
func TestMissingTranscriptPath(t *testing.T) {
	tier, ok := DetectTier("")
	if ok || tier != "" {
		t.Errorf("empty path: got (%q, %v), want (\"\", false)", tier, ok)
	}
}

// TestNonexistentTranscript: nonexistent file → ("", false).
func TestNonexistentTranscript(t *testing.T) {
	tier, ok := DetectTier("/nonexistent/path/does-not-exist.jsonl")
	if ok || tier != "" {
		t.Errorf("nonexistent path: got (%q, %v), want (\"\", false)", tier, ok)
	}
}

// ─── IsFableModel ─────────────────────────────────────────────────────────────

// TestIsFableModel: sanity-check the exported helper.
func TestIsFableModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-fable-5", true},
		{"fable", true},
		{"claude-opus-4-8", false},
		{"claude-sonnet-4-5", false},
		{"", false},
		{"<synthetic>", false},
	}
	for _, c := range cases {
		got := IsFableModel(c.model)
		if got != c.want {
			t.Errorf("IsFableModel(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}
