package spawn_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/tiller/internal/policy"
	"m31labs.dev/tiller/internal/spawn"
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
				if h["command"] != "tiller hook" {
					t.Errorf("%s hook command = %v, want \"tiller hook\"", event, h["command"])
				}
			}
		})
	}
}

// TestSettings_Depth2NoDispatch verifies that no depth-2 golden file's allow
// list contains "Bash(tiller *)" or "Bash(tiller dispatch*)" — terminal
// agents must not have dispatch capability in the allow list.
// Note: "Bash(tiller dispatch*)" may appear in the deny list for execution
// profile as a defence-in-depth guardrail; that is expected and correct.
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

			var doc map[string]interface{}
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			perms, ok := doc["permissions"].(map[string]interface{})
			if !ok {
				t.Fatal("missing permissions object")
			}
			allowRaw, _ := perms["allow"].([]interface{})
			var allowStrings []string
			for _, a := range allowRaw {
				if s, ok := a.(string); ok {
					allowStrings = append(allowStrings, s)
				}
			}

			// The allow list must not contain the unrestricted tiller allow.
			// It may contain the queue-only "Bash(tiller dispatch --queue *)" per spec §4.3.
			for _, a := range allowStrings {
				if a == "Bash(tiller *)" {
					t.Errorf("depth-2 allow list for %q contains Bash(tiller *)", tc.profile)
				}
				// Broad tiller dispatch without --queue must not appear in the allow list.
				// "Bash(tiller dispatch --queue *)" is the queue-only form and IS allowed.
				if a == "Bash(tiller dispatch *)" || a == "Bash(tiller dispatch*)" {
					t.Errorf("depth-2 allow list for %q contains unrestricted dispatch %q", tc.profile, a)
				}
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

// TestSettings_Depth1HasTiller verifies that depth-1 settings include the
// unrestricted Bash(tiller *) entry.
func TestSettings_Depth1HasTiller(t *testing.T) {
	profiles := []string{"orchestrator", "insight", "readonly"}
	for _, p := range profiles {
		p := p
		t.Run(p, func(t *testing.T) {
			got, err := spawn.Settings(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), `"Bash(tiller *)"`) {
				t.Errorf("depth-1 %q missing Bash(tiller *)", p)
			}
		})
	}
}

// TestSettings_Depth2HasNoteForm verifies that depth-2 settings include
// "Bash(tiller note *)" (not the full form).
func TestSettings_Depth2HasNoteForm(t *testing.T) {
	profiles := []string{"orchestrator", "insight", "readonly"}
	for _, p := range profiles {
		p := p
		t.Run(p, func(t *testing.T) {
			got, err := spawn.Settings(p, 2)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), `"Bash(tiller note *)"`) {
				t.Errorf("depth-2 %q missing Bash(tiller note *)", p)
			}
		})
	}
}

// TestSettings_Depth2ExecutionDenyDispatch verifies that the execution profile at
// depth >= 2 contains "Bash(tiller dispatch *)" in the deny list (broad dispatch
// without --queue is denied), providing a settings-layer guardrail per spec §4.3.
func TestSettings_Depth2ExecutionDenyDispatch(t *testing.T) {
	got, err := spawn.Settings("execution", 2)
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

	found := false
	for _, d := range denyRaw {
		if d == "Bash(tiller dispatch *)" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("execution depth-2 deny list missing \"Bash(tiller dispatch *)\"; got: %v", denyRaw)
	}
}

// TestSettings_Depth1ExecutionNoDenyDispatch verifies that execution at depth 1
// does NOT have "Bash(tiller dispatch *)" in the deny list (depth-1 workers
// may dispatch without restriction).
func TestSettings_Depth1ExecutionNoDenyDispatch(t *testing.T) {
	got, err := spawn.Settings("execution", 1)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(got), `"Bash(tiller dispatch *)"`) {
		t.Error("execution depth-1 deny list unexpectedly contains Bash(tiller dispatch *)")
	}
}

// TestBuildEnv_SetsTILLER_TIER verifies that BuildEnv exports TILLER_TIER
// from the dispatch's tier (Route.Tier), matching the adapter.go docstring claim.
func TestBuildEnv_SetsTILLER_TIER(t *testing.T) {
	a := spawn.ClaudeArgs{
		RunDir:      "/tmp/runs/r01",
		DispatchID:  "d01",
		Role:        "worker",
		CallerDepth: 0,
		Route:       policy.Route{Tier: "execute"},
	}
	env := spawn.BuildEnv(a)

	envMap := make(map[string]string, len(env))
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		envMap[kv[:idx]] = kv[idx+1:]
	}

	if envMap["TILLER_TIER"] != "execute" {
		t.Errorf("TILLER_TIER = %q, want %q", envMap["TILLER_TIER"], "execute")
	}
	if envMap["TILLER_ROLE"] != "worker" {
		t.Errorf("TILLER_ROLE = %q, want %q", envMap["TILLER_ROLE"], "worker")
	}
}
