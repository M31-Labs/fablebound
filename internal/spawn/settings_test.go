package spawn_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/fablebound/internal/spawn"
)

// -update rewrites the golden files from the current output.
var update = flag.Bool("update", false, "update golden files")

var goldenCases = []struct {
	profile string
	depth   int
}{
	{"orchestrator", 1},
	{"orchestrator", 2},
	{"insight", 1},
	{"insight", 2},
	{"readonly", 1},
	{"readonly", 2},
	{"execution", 1},
	{"execution", 2},
}

func goldenPath(profile string, depth int) string {
	return filepath.Join("testdata", "settings",
		profile+"-"+depthStr(depth)+".json")
}

func depthStr(depth int) string {
	switch depth {
	case 1:
		return "depth1"
	case 2:
		return "depth2"
	default:
		return "depth" + string(rune('0'+depth))
	}
}

func TestSettings_Golden(t *testing.T) {
	for _, tc := range goldenCases {
		tc := tc
		t.Run(tc.profile+"-depth"+depthStr(tc.depth), func(t *testing.T) {
			got, err := spawn.Settings(tc.profile, tc.depth)
			if err != nil {
				t.Fatalf("Settings(%q, %d): %v", tc.profile, tc.depth, err)
			}

			path := goldenPath(tc.profile, tc.depth)

			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("writing golden %s: %v", path, err)
				}
				t.Logf("updated golden %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("Settings(%q, %d) mismatch\n--- got ---\n%s\n--- want ---\n%s",
					tc.profile, tc.depth, got, want)
			}
		})
	}
}

// TestSettings_HookBlocks verifies every profile at every depth embeds both
// PreToolUse and PostToolUse hook blocks with the correct command.
func TestSettings_HookBlocks(t *testing.T) {
	for _, tc := range goldenCases {
		tc := tc
		t.Run(tc.profile+"-depth"+depthStr(tc.depth), func(t *testing.T) {
			got, err := spawn.Settings(tc.profile, tc.depth)
			if err != nil {
				t.Fatalf("Settings: %v", err)
			}

			var doc map[string]interface{}
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			hooks, ok := doc["hooks"].(map[string]interface{})
			if !ok {
				t.Fatal("missing hooks object")
			}

			for _, event := range []string{"PreToolUse", "PostToolUse"} {
				list, ok := hooks[event].([]interface{})
				if !ok || len(list) == 0 {
					t.Errorf("missing %s hook block", event)
					continue
				}
				block, ok := list[0].(map[string]interface{})
				if !ok {
					t.Errorf("%s[0] is not an object", event)
					continue
				}
				if block["matcher"] != ".*" {
					t.Errorf("%s matcher = %v, want .*", event, block["matcher"])
				}
				inner, ok := block["hooks"].([]interface{})
				if !ok || len(inner) == 0 {
					t.Errorf("%s missing inner hooks list", event)
					continue
				}
				h, ok := inner[0].(map[string]interface{})
				if !ok {
					t.Errorf("%s inner hook[0] not an object", event)
					continue
				}
				if h["command"] != "fablebound hook" {
					t.Errorf("%s hook command = %v, want \"fablebound hook\"", event, h["command"])
				}
			}
		})
	}
}

// TestSettings_Depth2NoDispatch verifies that no depth-2 golden file contains
// "Bash(fablebound *)" or "Bash(fablebound dispatch*)" — terminal agents
// must not have dispatch capability.
func TestSettings_Depth2NoDispatch(t *testing.T) {
	for _, tc := range goldenCases {
		if tc.depth < 2 {
			continue
		}
		tc := tc
		t.Run(tc.profile+"-depth2", func(t *testing.T) {
			got, err := spawn.Settings(tc.profile, tc.depth)
			if err != nil {
				t.Fatalf("Settings: %v", err)
			}

			text := string(got)

			// Must not contain the unrestricted fablebound allow.
			if strings.Contains(text, `"Bash(fablebound *)"`) {
				t.Errorf("depth-2 settings for %q still contains Bash(fablebound *)", tc.profile)
			}
			// Must not contain a dispatch-specific allow.
			if strings.Contains(text, `"Bash(fablebound dispatch`) {
				t.Errorf("depth-2 settings for %q still contains Bash(fablebound dispatch*)", tc.profile)
			}
		})
	}
}

// TestSettings_OrchestratorDenyList verifies the exact deny list for the
// orchestrator profile.
func TestSettings_OrchestratorDenyList(t *testing.T) {
	got, err := spawn.Settings("orchestrator", 1)
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}

	perms, ok := doc["permissions"].(map[string]interface{})
	if !ok {
		t.Fatal("missing permissions")
	}
	denyRaw, ok := perms["deny"].([]interface{})
	if !ok {
		t.Fatal("deny list is not an array")
	}

	want := []string{"Edit", "Write", "NotebookEdit", "Agent", "WebFetch", "WebSearch"}
	if len(denyRaw) != len(want) {
		t.Fatalf("deny list len=%d want %d: %v", len(denyRaw), len(want), denyRaw)
	}
	for i, w := range want {
		if denyRaw[i] != w {
			t.Errorf("deny[%d]=%v want %q", i, denyRaw[i], w)
		}
	}
}

// TestSettings_UnknownProfile verifies that an unknown profile returns an error.
func TestSettings_UnknownProfile(t *testing.T) {
	_, err := spawn.Settings("bogus", 1)
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

// TestSettings_Depth1HasFablebound verifies that depth-1 settings include the
// unrestricted Bash(fablebound *) entry.
func TestSettings_Depth1HasFablebound(t *testing.T) {
	profiles := []string{"orchestrator", "insight", "readonly"}
	for _, p := range profiles {
		p := p
		t.Run(p, func(t *testing.T) {
			got, err := spawn.Settings(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), `"Bash(fablebound *)"`) {
				t.Errorf("depth-1 %q missing Bash(fablebound *)", p)
			}
		})
	}
}

// TestSettings_Depth2HasNoteForm verifies that depth-2 settings include
// "Bash(fablebound note *)" (not the full form).
func TestSettings_Depth2HasNoteForm(t *testing.T) {
	profiles := []string{"orchestrator", "insight", "readonly"}
	for _, p := range profiles {
		p := p
		t.Run(p, func(t *testing.T) {
			got, err := spawn.Settings(p, 2)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), `"Bash(fablebound note *)"`) {
				t.Errorf("depth-2 %q missing Bash(fablebound note *)", p)
			}
		})
	}
}
