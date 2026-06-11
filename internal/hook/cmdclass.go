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
//  1. Walk the command with a quote-aware state machine to find real (unquoted)
//     shell metacharacters.  States: unquoted, single-quoted, double-quoted,
//     and backslash-escape (in unquoted and double-quoted contexts; inside
//     single quotes everything is literal).
//
//  2. Segment separators (|, ;, &, &&, ||, newline) and redirects (>, >>, <)
//     are recognised only in the unquoted state.  The exact token "2>&1" is
//     permitted (the only form needed for capturing stderr).
//
//  3. Command substitution ($( or backtick) is dangerous in unquoted AND
//     double-quoted state; inside single quotes it is literal and safe.
//     $VAR / ${VAR} expansion (without the opening paren) is allowed.
//
//  4. An unterminated quote → "other" (conservative).
//
//  5. Classify each segment by argv0 basename + subcommand.  The first
//     "other" segment short-circuits the whole command.
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

	segments, ok := splitSegmentsQuoteAware(cmd)
	if !ok {
		// Unterminated quote or dangerous unquoted metacharacter found during
		// split — conservative denial.
		return "other"
	}

	for _, seg := range segments {
		if classifySegment(seg) != "readonly" {
			return "other"
		}
	}
	return "readonly"
}

// splitSegmentsQuoteAware walks cmd with a shell quote-aware state machine.
// It returns (segments, true) when the command is syntactically clean (no
// dangerous unquoted metacharacters other than the permitted operators).
// It returns (nil, false) if:
//   - an unterminated quote is detected, OR
//   - a dangerous unquoted pattern is found:
//     output/append redirect (>), input redirect (<), backtick (`), or
//     command substitution ($() — note: bare $VAR is allowed.
//
// Permitted separators that cause a new segment: |, ;, &&, ||, newline.
// The literal token "2>&1" (unquoted) is allowed and consumed without
// triggering a redirect error.
func splitSegmentsQuoteAware(cmd string) ([]string, bool) {
	const (
		stateUnquoted = iota
		stateSingle
		stateDouble
		stateEscapeUnquoted // backslash in unquoted
		stateEscapeDouble   // backslash in double-quoted
	)

	state := stateUnquoted
	var current strings.Builder
	var segments []string

	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			segments = append(segments, s)
		}
		current.Reset()
	}

	s := cmd
	for i := 0; i < len(s); {
		c := s[i]

		switch state {
		case stateEscapeUnquoted:
			// The character after an unquoted backslash is literal; write it
			// and return to unquoted state.
			current.WriteByte(c)
			state = stateUnquoted
			i++

		case stateEscapeDouble:
			current.WriteByte(c)
			state = stateDouble
			i++

		case stateSingle:
			// Inside single quotes everything is literal — no escapes at all.
			if c == '\'' {
				state = stateUnquoted
			} else {
				current.WriteByte(c)
			}
			i++

		case stateDouble:
			switch c {
			case '"':
				state = stateUnquoted
				i++
			case '\\':
				state = stateEscapeDouble
				i++
			case '`':
				// Command substitution inside double quotes — dangerous.
				return nil, false
			case '$':
				// Check for $( — command substitution even inside double quotes.
				if i+1 < len(s) && s[i+1] == '(' {
					return nil, false
				}
				// $VAR / ${VAR} are harmless variable expansions.
				current.WriteByte(c)
				i++
			default:
				current.WriteByte(c)
				i++
			}

		case stateUnquoted:
			switch c {
			case '\'':
				state = stateSingle
				i++

			case '"':
				state = stateDouble
				i++

			case '\\':
				state = stateEscapeUnquoted
				i++

			case '`':
				return nil, false

			case '$':
				if i+1 < len(s) && s[i+1] == '(' {
					return nil, false
				}
				current.WriteByte(c)
				i++

			case '>':
				// Allow the exact "2>&1" token — check if the current buffer
				// ends with "2" and what follows is ">&1" (optionally followed
				// by space/end).
				if i+2 < len(s) && s[i+1] == '&' && s[i+2] == '1' {
					// Verify the preceding char was '2'.
					buf := current.String()
					if len(buf) > 0 && buf[len(buf)-1] == '2' {
						// Consume >&1; strip trailing '2' from current.
						current.Reset()
						current.WriteString(buf[:len(buf)-1])
						i += 3 // skip >&1
						continue
					}
				}
				// Any other > is an output redirect — dangerous.
				return nil, false

			case '<':
				return nil, false

			case '|':
				// Check for ||
				if i+1 < len(s) && s[i+1] == '|' {
					flush()
					i += 2
				} else {
					// Pipeline segment separator.
					flush()
					i++
				}

			case '&':
				// Check for &&
				if i+1 < len(s) && s[i+1] == '&' {
					flush()
					i += 2
				} else {
					// Bare & (background job) — dangerous.
					return nil, false
				}

			case ';':
				flush()
				i++

			case '\n':
				flush()
				i++

			default:
				current.WriteByte(c)
				i++
			}
		}
	}

	// After walking the whole string, check for unterminated quotes.
	if state == stateSingle || state == stateDouble {
		return nil, false
	}

	flush()
	return segments, true
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
	// Any leading VAR=val assignment is treated as unsafe (could override PATH,
	// LD_PRELOAD, etc. to subvert the command being classified).
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return "readonly" // empty segment is harmless
	}
	if isVarAssignment(fields[0]) {
		return "other"
	}
	argv := fields
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

// IsSelfUninstall returns true if cmd is exactly "tiller uninstall" with only
// the optional --print and/or --project flags and no other arguments, no
// chaining, no redirects, and no command substitution.
//
// Allowed forms (any order of flags):
//
//	tiller uninstall
//	tiller uninstall --print
//	tiller uninstall --project
//	tiller uninstall --print --project
//	tiller uninstall --project --print
//
// Any chaining operator (;, &&, ||, |, &), redirect (>, <), or substitution
// causes the function to return false immediately.
func IsSelfUninstall(cmd string) bool {
	segments, ok := splitSegmentsQuoteAware(cmd)
	if !ok {
		// Dangerous metacharacter or unterminated quote — not safe.
		return false
	}
	// Must be exactly one segment.
	if len(segments) != 1 {
		return false
	}
	fields := strings.Fields(segments[0])
	if len(fields) == 0 {
		return false
	}
	// Any leading VAR=val assignment is unsafe — could override PATH/LD_PRELOAD.
	if len(fields) > 0 && isVarAssignment(fields[0]) {
		return false
	}
	argv := fields
	// Must start with "tiller" or a path whose base is "tiller".
	if len(argv) < 2 {
		return false
	}
	if filepath.Base(argv[0]) != "tiller" {
		return false
	}
	if argv[1] != "uninstall" {
		return false
	}
	// All remaining args must be --print or --project, each at most once.
	seenPrint, seenProject := false, false
	for _, arg := range argv[2:] {
		switch arg {
		case "--print":
			if seenPrint {
				return false // duplicate
			}
			seenPrint = true
		case "--project":
			if seenProject {
				return false // duplicate
			}
			seenProject = true
		default:
			return false // unknown arg
		}
	}
	return true
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
