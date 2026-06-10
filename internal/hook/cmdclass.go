package hook

import (
	"path/filepath"
	"strings"
)

// ClassifyCommand inspects a Bash command string and returns "readonly" if
// every pipeline segment is read-only and safe to run in the ambient
// orchestrator context, or "other" if any segment performs side effects or
// could escape containment.
//
// Algorithm
//
//  1. Split the command into segments on shell sequencing operators
//     (|, ;, &&, ||) and newlines. Each token on either side of these
//     operators is treated independently.
//
//  2. Strip leading VAR=val environment assignments from each segment
//     (e.g. "FOO=1 BAR=2 gts callgraph X" → argv0 = "gts").
//
//  3. Redirect/substitution policy (conservative, documented):
//     • "2>&1" (exact token) is permitted — it is the documented usage
//       pattern for capturing stderr alongside stdout (e.g. hypha recall
//       "..." 2>&1 | head -80).
//     • Any other ">" or ">>" anywhere → other (output redirect).
//     • Any "<" anywhere → other (input redirect — arguably safe but kept
//       conservative per spec).
//     • Any backtick or "$(" anywhere → other (command substitution).
//
//  4. Classify the segment by argv0 basename + subcommand where relevant.
//     Every segment must classify as "readonly"; the first "other" segment
//     short-circuits the whole command to "other".
//
// Read-only allowlist:
//
//	General text-processing utilities: ls, cat, head, tail, wc, grep, rg,
//	find, tree, file, stat, du, sort, uniq, cut, pwd, which, echo, diff,
//	jq, column.
//
//	git: status, log, show, diff, blame, rev-parse, ls-files, describe,
//	shortlog, grep, and bare "git branch" or "git branch --list" / "-l"
//	forms only (any other branch args → other).
//
//	go: doc, list, version, vet.
//
//	gts: all subcommands (read-only by design).
//
//	hypha: all subcommands EXCEPT "mcp serve" and "hub serve" → other.
//
//	tiller: runs, poll, version subcommands only.
func ClassifyCommand(cmd string) string {
	if cmd == "" {
		return "other"
	}

	// Check for unsafe redirect/substitution tokens in the raw command first,
	// but allow "2>&1" as an explicit exception.
	// We scan for the dangerous tokens after stripping "2>&1".
	sanitised := strings.ReplaceAll(cmd, "2>&1", "")
	if containsRedirectOrSubst(sanitised) {
		return "other"
	}

	// Split into segments on |, ;, &&, ||, newlines.
	segments := splitSegments(cmd)
	for _, seg := range segments {
		if classifySegment(seg) != "readonly" {
			return "other"
		}
	}
	return "readonly"
}

// containsRedirectOrSubst reports whether s contains any of the dangerous
// characters/patterns that indicate output redirect, input redirect, or
// command substitution: >, >>, <, `, $(.
func containsRedirectOrSubst(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '>', '<', '`':
			return true
		case '$':
			if i+1 < len(s) && s[i+1] == '(' {
				return true
			}
		}
	}
	return false
}

// splitSegments splits a shell command string into segments on the sequencing
// operators |, ;, &&, ||, and newlines.  The split is textual (no quoting
// awareness needed for our classifier — misclassifying a quoted "|" is
// acceptable since the conservative fallback is "other").
func splitSegments(cmd string) []string {
	// Replace multi-char operators first, then split on single chars.
	// We normalise &&, || to ; for simplicity, then split on | and ;.
	s := cmd
	s = strings.ReplaceAll(s, "&&", ";")
	s = strings.ReplaceAll(s, "||", ";")
	s = strings.ReplaceAll(s, "\n", ";")
	// Now split on | and ;.
	// We split on | separately first, then on ;.
	var segs []string
	for _, part := range strings.Split(s, "|") {
		for _, sub := range strings.Split(part, ";") {
			t := strings.TrimSpace(sub)
			if t != "" {
				segs = append(segs, t)
			}
		}
	}
	return segs
}

// isVarAssignment reports whether tok looks like a shell variable assignment
// (VAR=val, possibly with quotes around val — we just check for '=' and a
// leading identifier).
func isVarAssignment(tok string) bool {
	if tok == "" {
		return false
	}
	idx := strings.IndexByte(tok, '=')
	if idx <= 0 {
		return false
	}
	// The part before '=' must be a valid identifier (A-Z, a-z, 0-9, _,
	// but must not start with a digit).
	key := tok[:idx]
	if len(key) == 0 || (key[0] >= '0' && key[0] <= '9') {
		return false
	}
	for _, c := range key {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// classifySegment classifies a single shell segment (already split and
// trimmed) as "readonly" or "other".
func classifySegment(seg string) string {
	// Strip leading VAR=val assignments to find argv0.
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return "readonly" // empty segment is harmless
	}
	start := 0
	for start < len(fields) && isVarAssignment(fields[start]) {
		start++
	}
	if start >= len(fields) {
		return "readonly" // only env assignments — harmless
	}
	argv := fields[start:]
	argv0base := filepath.Base(argv[0])
	sub := ""
	if len(argv) > 1 {
		sub = argv[1]
	}

	switch argv0base {
	// ── General read-only utilities ──────────────────────────────────────────
	case "ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "tree",
		"file", "stat", "du", "sort", "uniq", "cut", "pwd", "which",
		"echo", "diff", "jq", "column":
		return "readonly"

	// ── git ──────────────────────────────────────────────────────────────────
	case "git":
		return classifyGit(sub, argv)

	// ── go ───────────────────────────────────────────────────────────────────
	case "go":
		switch sub {
		case "doc", "list", "version", "vet":
			return "readonly"
		}
		return "other"

	// ── gts (all subcommands are read-only by design) ────────────────────────
	case "gts":
		return "readonly"

	// ── hypha (all except mcp serve / hub serve) ─────────────────────────────
	case "hypha":
		return classifyHypha(sub, argv)

	// ── tiller ───────────────────────────────────────────────────────────────
	case "tiller":
		switch sub {
		case "runs", "poll", "version":
			return "readonly"
		}
		return "other"
	}

	return "other"
}

// classifyGit classifies a git invocation by subcommand.
// Allowed: status, log, show, diff, blame, rev-parse, ls-files, describe,
//
//	shortlog, grep, and bare "git branch" or "git branch --list" / "-l".
//
// Any other branch args → other.
func classifyGit(sub string, argv []string) string {
	switch sub {
	case "status", "log", "show", "diff", "blame", "rev-parse",
		"ls-files", "describe", "shortlog", "grep":
		return "readonly"

	case "branch":
		// Allow bare "git branch" (no extra args) and "--list [pattern]" /
		// "-l [pattern]" forms only. Any other flags or bare branch names → other.
		// argv is [git, branch, ...args]; extra args start at index 2.
		if len(argv) <= 2 {
			return "readonly" // bare "git branch"
		}
		// Scan flags: only --list / -l are permitted option flags; at most one
		// optional non-option pattern argument is allowed after a list flag.
		sawListFlag := false
		sawPattern := false
		for _, arg := range argv[2:] {
			if arg == "--list" || arg == "-l" {
				sawListFlag = true
				continue
			}
			if !strings.HasPrefix(arg, "-") {
				// Non-option arg: only permitted as a glob pattern when a list
				// flag was already seen, and only once.
				if sawListFlag && !sawPattern {
					sawPattern = true
					continue
				}
				// Bare branch name without --list/-l preceding it → other.
				return "other"
			}
			// Any other flag → other.
			return "other"
		}
		return "readonly"
	}

	return "other"
}

// classifyHypha classifies a hypha invocation.
// All subcommands allowed EXCEPT "mcp serve" and "hub serve".
func classifyHypha(sub string, argv []string) string {
	// argv: [hypha, sub, ...]
	if sub == "" {
		// bare "hypha" with no subcommand
		return "readonly"
	}
	// Persistent daemon forms → other.
	if (sub == "mcp" || sub == "hub") && len(argv) >= 3 && argv[2] == "serve" {
		return "other"
	}
	return "readonly"
}
