package hook

import "testing"

// TestClassifyCommand exercises the ClassifyCommand classifier with the
// canonical table from the spec plus additional edge cases.
func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		// ── Spec-required rows ───────────────────────────────────────────────
		// "hypha recall ... 2>&1 | head -80" must classify readonly.
		{`hypha recall "galaxy migration" 2>&1 | head -80`, "readonly"},
		// "hypha pulse | head -5" must classify readonly.
		{"hypha pulse | head -5", "readonly"},
		// "git log --oneline -3" must classify readonly.
		{"git log --oneline -3", "readonly"},
		// "ls; rm -rf /" must classify other (rm is not in the allowlist).
		{"ls; rm -rf /", "other"},
		// "cat x > y" must classify other (output redirect).
		{"cat x > y", "other"},
		// "hypha mcp serve" must classify other (daemon guard).
		{"hypha mcp serve", "other"},
		// "git branch new-feature" must classify other (non-list branch arg).
		{"git branch new-feature", "other"},
		// "FOO=1 gts callgraph X | wc -l" must classify other (env prefix unsafe).
		{"FOO=1 gts callgraph X | wc -l", "other"},
		// "echo $(whoami)" must classify other (command substitution).
		{"echo $(whoami)", "other"},

		// ── Hypha variants ───────────────────────────────────────────────────
		{"hypha recall ambient policy", "readonly"},
		{"hypha pulse", "readonly"},
		{"hypha show abc123", "readonly"},
		{"hypha trace start", "readonly"},
		{"hypha spore submit spec.md --sign", "readonly"},
		{"hypha graph backlinks", "readonly"},
		{"hypha assess change", "readonly"},
		{"hypha hub serve", "other"},
		// 2>&1 redirect is permitted with hypha.
		{"hypha recall x 2>&1", "readonly"},

		// ── git variants ─────────────────────────────────────────────────────
		{"git status", "readonly"},
		{"git log --oneline -10", "readonly"},
		{"git show HEAD", "readonly"},
		{"git diff HEAD~1", "readonly"},
		{"git blame main.go", "readonly"},
		{"git rev-parse HEAD", "readonly"},
		{"git ls-files", "readonly"},
		{"git describe --tags", "readonly"},
		{"git shortlog -sn", "readonly"},
		{"git grep TODO", "readonly"},
		{"git branch", "readonly"},           // bare branch
		{"git branch --list", "readonly"},    // --list flag
		{"git branch -l", "readonly"},        // -l flag
		{"git branch -l 'main'", "readonly"}, // -l + pattern
		{"git branch --show-current", "readonly"},
		{"git branch -d old", "other"},    // -d → other
		{"git branch -D main", "other"},   // -D → other
		{"git checkout main", "other"},    // checkout → other
		{"git push origin main", "other"}, // push → other
		{"git add .", "other"},            // add → other

		// ── go variants ──────────────────────────────────────────────────────
		{"go doc fmt.Println", "readonly"},
		{"go list ./...", "readonly"},
		{"go version", "readonly"},
		{"go vet ./...", "readonly"},
		{"go build ./...", "other"},
		{"go test ./...", "other"},
		{"go run main.go", "other"},

		// ── gts (all allowed) ─────────────────────────────────────────────────
		{"gts callgraph MyFunc", "readonly"},
		{"gts refs MyType", "readonly"},
		{"gts impact pkg/foo", "readonly"},
		{"gts hotspot .", "readonly"},
		{"gts dead .", "readonly"},

		// ── General utilities ─────────────────────────────────────────────────
		{"ls -la", "readonly"},
		{"cat go.mod", "readonly"},
		{"head -20 README.md", "readonly"},
		{"tail -f /tmp/log", "readonly"},
		{"wc -l *.go", "readonly"},
		{"grep -r TODO .", "readonly"},
		{"rg 'func Test' .", "readonly"},
		{"find . -name '*.go'", "readonly"},
		{"tree -L 2", "readonly"},
		{"stat main.go", "readonly"},
		{"du -sh .", "readonly"},
		{"sort go.sum", "readonly"},
		{"uniq -c", "readonly"},
		{"cut -d: -f1 /etc/passwd", "readonly"},
		{"pwd", "readonly"},
		{"which go", "readonly"},
		{"echo hello", "readonly"},
		{"diff a.go b.go", "readonly"},
		{"jq '.name' package.json", "readonly"},
		{"column -t", "readonly"},
		{"nl -ba AGENTS.md", "readonly"},
		{"sed -n '1,120p' AGENTS.md", "readonly"},
		{"sed -n '/Root Codex/p' AGENTS.md", "readonly"},
		{"sed -i 's/a/b/' AGENTS.md", "other"},
		{"sed -n '1,20w /tmp/out' AGENTS.md", "other"},
		{"ps aux", "readonly"},
		{"ps -ef", "readonly"},
		{"pgrep -af node", "readonly"},
		{"pidof node", "readonly"},
		{"lsof -iTCP -sTCP:LISTEN -P -n", "readonly"},
		{"netstat -tulpn", "readonly"},
		{"ss -ltnp", "readonly"},
		{"ss -tulpn", "readonly"},
		{"ss -K dst 127.0.0.1", "other"},
		{"ss --kill dst 127.0.0.1", "other"},
		{"ss --kill=dst", "other"},
		{"ss -D /tmp/x", "other"},
		{"ss --diag /tmp/x", "other"},
		{"ss --diag=/tmp/x", "other"},

		// ── Canopy read-oriented commands ────────────────────────────────────
		{"canopy search symbol Foo", "readonly"},
		{"canopy graph call Foo", "readonly"},
		{"canopy analyze report", "readonly"},
		{"canopy index build .", "other"},
		{"canopy init", "other"},
		{"canopy mcp --root .", "other"},

		// ── tiller variants ───────────────────────────────────────────────────
		{"tiller runs", "readonly"},
		{"tiller poll", "readonly"},
		{"tiller version", "readonly"},
		{"tiller dispatch worker run", "other"},
		{"tiller init", "other"},
		{"tiller run", "other"},

		// ── Env prefix rejection (any VAR=val prefix → other) ────────────────
		{"BAR=2 ls -la", "other"},
		{"FOO=1 BAR=2 gts hotspot .", "other"},
		{"PATH=/tmp go build ./...", "other"}, // env prefix → other
		{"X=1 rm -rf /", "other"},
		// These must still classify correctly without env prefix.
		{"git log", "readonly"},
		{"tiller uninstall", "other"}, // tiller uninstall is not a readonly op
		// Env prefix makes any command unsafe regardless of the command itself.
		{"PATH=/tmp/evil tiller uninstall", "other"},
		{"LD_PRELOAD=/x.so git log", "other"},

		// ── Redirect/substitution guards ─────────────────────────────────────
		{"ls > /tmp/out", "other"},    // output redirect
		{"ls >> /tmp/out", "other"},   // append redirect
		{"cat < /dev/stdin", "other"}, // input redirect
		{"echo `date`", "other"},      // backtick substitution
		{"echo $(date)", "other"},     // $() substitution
		// 2>&1 alone is permitted
		{"ls 2>&1", "readonly"},
		{"hypha recall x 2>&1 | head -5", "readonly"},

		// ── Multi-segment ────────────────────────────────────────────────────
		{"git log && git status", "readonly"},
		{"ls || echo nope", "readonly"},
		{"ls\ngit status", "readonly"},
		{"git log | grep fix | wc -l", "readonly"},
		{"git status; ls", "readonly"},
		{"ls; rm file", "other"},               // rm not in allowlist
		{"ls | sudo tee /etc/passwd", "other"}, // sudo not in allowlist

		// ── Empty/degenerate ─────────────────────────────────────────────────
		{"", "other"},

		// ── Quote-aware cases (TDD: these fail before the fix) ───────────────
		// Real denied commands from the bug report.
		{`grep -n "ambient\|Ambient" internal/hook/hook.go | head -30`, "readonly"},
		{`grep -n "func DetectTier\|func lastFable\|Scan\|ReadFile\|Open\|bufio" internal/adapter/claudecode/detect.go | head -15`, "readonly"},
		// Alternation in rg quoted arg.
		{`rg "foo|bar" src/ | wc -l`, "readonly"},
		// hypha with quoted arg containing & and |.
		{`hypha recall "graphs & pipes | tricky" --format text`, "readonly"},
		// Single-quoted arg with shell metacharacters — all literal.
		{`echo 'safe $(not run) literal'`, "readonly"},
		// $() inside double quotes — still command substitution.
		{`echo "danger $(whoami)"`, "other"},
		// Quoted semicolon in grep arg but real separator outside.
		{`grep "a;b" f.txt; rm x`, "other"},
		// > inside quoted filename arg — should be safe.
		{`cat "file with > angle.txt"`, "readonly"},
		// Unquoted redirect after a quoted arg.
		{`cat f > "out.txt"`, "other"},
		// Unterminated single quote — conservative → other.
		{`grep 'unterminated f.txt`, "other"},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := ClassifyCommand(tc.cmd)
			if got != tc.want {
				t.Errorf("ClassifyCommand(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestIsSelfUninstall exercises the IsSelfUninstall escape-hatch predicate.
func TestIsSelfUninstall(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// ── Allowed forms ────────────────────────────────────────────────────
		{"tiller uninstall", true},
		{"tiller uninstall --print", true},
		{"tiller uninstall --project", true},
		{"tiller uninstall --print --project", true},
		{"tiller uninstall --project --print", true},
		{"tiller uninstall --backend codex --project", true},
		{"tiller uninstall --project --backend claude-code --print", true},
		{"tiller uninstall --backend=codex", true},
		// Full path binary — base must be "tiller".
		{"/usr/local/bin/tiller uninstall", true},
		{"/home/user/go/bin/tiller uninstall --print", true},

		// ── Denied: chaining ─────────────────────────────────────────────────
		{"tiller uninstall; rm -rf /", false},
		{"tiller uninstall && echo done", false},
		{"tiller uninstall || true", false},

		// ── Denied: wrong subcommand ─────────────────────────────────────────
		{"tiller install", false},
		{"tiller run foo", false},
		{"tiller version", false},
		{"tiller uninstall extra-arg", false},

		// ── Denied: duplicate flags ───────────────────────────────────────────
		{"tiller uninstall --print --print", false},
		{"tiller uninstall --project --project", false},
		{"tiller uninstall --backend codex --backend claude-code", false},
		{"tiller uninstall --backend=codex --backend claude-code", false},

		// ── Denied: unknown flags ─────────────────────────────────────────────
		{"tiller uninstall --force", false},
		{"tiller uninstall --dry-run", false},
		{"tiller uninstall --backend", false},
		{"tiller uninstall --backend unknown", false},
		{"tiller uninstall --backend=unknown", false},

		// ── Denied: dangerous patterns ────────────────────────────────────────
		{"tiller uninstall > /dev/null", false},
		{"tiller uninstall `rm x`", false},

		// ── Denied: env assignments (could override PATH/LD_PRELOAD) ─────────
		{"PATH=/tmp/evil tiller uninstall", false},
		{"LD_PRELOAD=/tmp/x.so tiller uninstall", false},

		// ── Denied: wrong binary ──────────────────────────────────────────────
		{"notiller uninstall", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := IsSelfUninstall(tc.cmd)
			if got != tc.want {
				t.Errorf("IsSelfUninstall(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestIsAmbientControl(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"tiller ambient disable", true},
		{"tiller ambient enable", true},
		{"tiller ambient status", true},
		{"tiller ambient next", true},
		{"tiller ambient off", true},
		{"/usr/local/bin/tiller ambient on", true},
		{"tiller ambient disable --force", false},
		{"tiller ambient next extra", false},
		{"tiller ambient unknown", false},
		{"tiller ambient disable; rm -rf /", false},
		{"PATH=/tmp/evil tiller ambient disable", false},
		{"notiller ambient disable", false},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := IsAmbientControl(tc.cmd)
			if got != tc.want {
				t.Errorf("IsAmbientControl(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestIsCodexExec(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium "implement the fix"`, true},
		{`codex exec --model=gpt-5.5 --config=model_reasoning_effort=high --sandbox workspace-write "debug this"`, true},
		{`codex exec --model gpt-5.5 --config 'model_reasoning_effort="xhigh"' --sandbox read-only "review the diff"`, true},
		{`codex e -m gpt-5.5 -c model_reasoning_effort=med "small edit"`, true},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh --sandbox read-only --output-last-message .tiller/reports/review.md "review"`, true},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --cd . "work"`, true},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --cd packages/app "work"`, true},
		{`/home/draco/.bun/bin/codex exec -m gpt-5.5 -c model_reasoning_effort=medium --json --color never "work"`, true},

		// Must be explicit about model and effort.
		{`codex exec -c model_reasoning_effort=medium "work"`, false},
		{`codex exec -m gpt-5.5 "work"`, false},
		{`codex exec -m gpt-5.4 -c model_reasoning_effort=medium "work"`, false},

		// xhigh is scrutiny/review only and must be read-only.
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh "review"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh --sandbox workspace-write "review"`, false},

		// Dangerous flags and broad config are denied.
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --sandbox danger-full-access "work"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --dangerously-bypass-approvals-and-sandbox "work"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium -c 'sandbox_permissions=["disk-full-read-access"]' "work"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh --sandbox read-only -o review.md "review"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=xhigh --sandbox read-only -o /tmp/review.md "review"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --cd /tmp "work"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium --cd ../other "work"`, false},
		{`codex exec -m gpt-5.5 -c model_reasoning_effort=medium && rm -rf /`, false},
	}

	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := IsCodexExec(tc.cmd)
			if got != tc.want {
				t.Errorf("IsCodexExec(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
