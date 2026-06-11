package tier

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// sectionKind represents the kind of the current section in the TOML parser.
type sectionKind int

const (
	sectionNone    sectionKind = iota
	sectionTiers               // [tiers.<name>]
	sectionAdapter             // [adapter.<name>]
)

// parse parses the hand-rolled TOML subset used for models.toml.
//
// Supported syntax:
//
//	# comment lines
//	blank lines
//	[tiers.<name>]    — tier section headers
//	candidates = ["a:p/m", "b:p/m"]  — single-line string arrays
//	[adapter.<name>]  — command adapter section headers
//	argv    = ["cmd", "{brief}", "{report}"]  — single-line string array
//	report  = "stdout"  — string value
//	timeout = "5m"      — string value
//
// Any other content is rejected. Line numbers in errors are 1-based.
func parse(data []byte) (*Config, error) {
	cfg := &Config{
		tiers:    make(map[string][]Candidate),
		adapters: make(map[string]*AdapterConfig),
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var (
		currentKind    sectionKind
		currentTier    string
		currentAdapter string
	)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section header: [tiers.<name>] or [adapter.<name>]
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed section header: %q", lineNum, line)
			}
			inner := line[1 : len(line)-1]

			const tiersPrefix = "tiers."
			const adapterPrefix = "adapter."

			switch {
			case strings.HasPrefix(inner, tiersPrefix):
				name := inner[len(tiersPrefix):]
				if name == "" || strings.ContainsAny(name, " \t.[]") {
					return nil, fmt.Errorf("line %d: invalid tier name in section header: %q", lineNum, line)
				}
				currentKind = sectionTiers
				currentTier = name
				currentAdapter = ""
				// Initialise empty candidate slice so override detection works even
				// for tiers with no candidates key yet.
				if _, exists := cfg.tiers[currentTier]; !exists {
					cfg.tiers[currentTier] = nil
				}

			case strings.HasPrefix(inner, adapterPrefix):
				name := inner[len(adapterPrefix):]
				if name == "" || strings.ContainsAny(name, " \t[]") {
					return nil, fmt.Errorf("line %d: invalid adapter name in section header: %q", lineNum, line)
				}
				currentKind = sectionAdapter
				currentAdapter = name
				currentTier = ""
				if _, exists := cfg.adapters[currentAdapter]; !exists {
					cfg.adapters[currentAdapter] = &AdapterConfig{Report: "stdout"}
				}

			default:
				return nil, fmt.Errorf("line %d: unsupported section %q (want [tiers.<name>] or [adapter.<name>])", lineNum, line)
			}
			continue
		}

		switch currentKind {
		case sectionTiers:
			// Key-value: candidates = [...]
			if strings.HasPrefix(line, "candidates") {
				cands, err := parseCandidatesLine(line, lineNum)
				if err != nil {
					return nil, err
				}
				if len(cands) == 0 {
					return nil, fmt.Errorf("line %d: candidates list must not be empty", lineNum)
				}
				cfg.tiers[currentTier] = cands
				continue
			}
			return nil, fmt.Errorf("line %d: unexpected content in [tiers.%s]: %q", lineNum, currentTier, line)

		case sectionAdapter:
			ac := cfg.adapters[currentAdapter]
			if strings.HasPrefix(line, "argv") {
				argv, err := parseStringArrayLine(line, "argv", lineNum)
				if err != nil {
					return nil, err
				}
				ac.Argv = argv
				continue
			}
			if strings.HasPrefix(line, "report") {
				val, err := parseStringValue(line, "report", lineNum)
				if err != nil {
					return nil, err
				}
				ac.Report = val
				continue
			}
			if strings.HasPrefix(line, "timeout") {
				val, err := parseStringValue(line, "timeout", lineNum)
				if err != nil {
					return nil, err
				}
				ac.Timeout = val
				continue
			}
			return nil, fmt.Errorf("line %d: unexpected content in [adapter.%s]: %q", lineNum, currentAdapter, line)

		default:
			return nil, fmt.Errorf("line %d: unexpected content outside a section: %q", lineNum, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan error: %w", err)
	}

	// Verify all declared tiers have at least one candidate.
	for name, cands := range cfg.tiers {
		if cands == nil {
			return nil, fmt.Errorf("tier %q declared but has no candidates", name)
		}
	}

	return cfg, nil
}

// parseStringValue parses a line of the form:
//
//	<key> = "<value>"
func parseStringValue(line, key string, lineNum int) (string, error) {
	before, after, ok := strings.Cut(line, "=")
	if !ok {
		return "", fmt.Errorf("line %d: missing '=' in %s assignment", lineNum, key)
	}
	k := strings.TrimSpace(before)
	if k != key {
		return "", fmt.Errorf("line %d: unexpected key %q (want %s)", lineNum, k, key)
	}
	val := strings.TrimSpace(after)
	if !strings.HasPrefix(val, "\"") || !strings.HasSuffix(val, "\"") || len(val) < 2 {
		return "", fmt.Errorf("line %d: %s value must be a quoted string", lineNum, key)
	}
	return val[1 : len(val)-1], nil
}

// parseStringArrayLine parses a line of the form:
//
//	<key> = ["v1", "v2", ...]
func parseStringArrayLine(line, key string, lineNum int) ([]string, error) {
	before, after, ok := strings.Cut(line, "=")
	if !ok {
		return nil, fmt.Errorf("line %d: missing '=' in %s assignment", lineNum, key)
	}
	k := strings.TrimSpace(before)
	if k != key {
		return nil, fmt.Errorf("line %d: unexpected key %q (want %s)", lineNum, k, key)
	}
	val := strings.TrimSpace(after)
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil, fmt.Errorf("line %d: %s value must be a single-line array [\"...\"]", lineNum, key)
	}
	inner := val[1 : len(val)-1]

	var result []string
	rest := strings.TrimSpace(inner)
	for rest != "" {
		if rest[0] != '"' {
			return nil, fmt.Errorf("line %d: expected '\"' in %s array, got %q", lineNum, key, rest)
		}
		closeIdx := strings.IndexByte(rest[1:], '"')
		if closeIdx < 0 {
			return nil, fmt.Errorf("line %d: unterminated string in %s array", lineNum, key)
		}
		token := rest[1 : closeIdx+1]
		result = append(result, token)
		rest = strings.TrimSpace(rest[closeIdx+2:])
		if rest != "" && rest[0] == ',' {
			rest = strings.TrimSpace(rest[1:])
		}
	}
	return result, nil
}

// parseCandidatesLine parses a line of the form:
//
//	candidates = ["a:p/m", "b:p/m"]
func parseCandidatesLine(line string, lineNum int) ([]Candidate, error) {
	// Split on '=' and verify key.
	before, after, ok := strings.Cut(line, "=")
	if !ok {
		return nil, fmt.Errorf("line %d: missing '=' in candidates assignment", lineNum)
	}
	key := strings.TrimSpace(before)
	if key != "candidates" {
		return nil, fmt.Errorf("line %d: unexpected key %q (want candidates)", lineNum, key)
	}
	val := strings.TrimSpace(after)

	// Must be a [ ... ] array.
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil, fmt.Errorf("line %d: candidates value must be a single-line array [\"...\"]", lineNum)
	}
	inner := val[1 : len(val)-1]

	// Tokenise the quoted strings.
	var raw []string
	rest := strings.TrimSpace(inner)
	for rest != "" {
		if rest[0] != '"' {
			return nil, fmt.Errorf("line %d: expected '\"' in candidate list, got %q", lineNum, rest)
		}
		// Find closing quote (no escape handling needed for our values).
		closeIdx := strings.IndexByte(rest[1:], '"')
		if closeIdx < 0 {
			return nil, fmt.Errorf("line %d: unterminated string in candidates", lineNum)
		}
		token := rest[1 : closeIdx+1]
		raw = append(raw, token)
		rest = strings.TrimSpace(rest[closeIdx+2:])
		// Consume optional comma.
		if rest != "" && rest[0] == ',' {
			rest = strings.TrimSpace(rest[1:])
		}
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("line %d: candidates list is empty", lineNum)
	}

	cands := make([]Candidate, 0, len(raw))
	for _, s := range raw {
		c, err := parseCandidate(s, lineNum)
		if err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	return cands, nil
}

// parseCandidate parses a "adapter:provider/model" string.
// provider may be "-" (for command adapters).
func parseCandidate(s string, lineNum int) (Candidate, error) {
	before, after, ok := strings.Cut(s, ":")
	if !ok {
		return Candidate{}, fmt.Errorf("line %d: candidate %q missing ':' separator (want adapter:provider/model)", lineNum, s)
	}
	adapter := before
	rest := after
	before0, after0, ok0 := strings.Cut(rest, "/")
	if !ok0 {
		return Candidate{}, fmt.Errorf("line %d: candidate %q missing '/' separator (want adapter:provider/model)", lineNum, s)
	}
	provider := before0
	model := after0

	if adapter == "" {
		return Candidate{}, fmt.Errorf("line %d: candidate %q has empty adapter", lineNum, s)
	}
	if provider == "" {
		return Candidate{}, fmt.Errorf("line %d: candidate %q has empty provider", lineNum, s)
	}
	if model == "" {
		return Candidate{}, fmt.Errorf("line %d: candidate %q has empty model", lineNum, s)
	}

	return Candidate{Adapter: adapter, Provider: provider, Model: model}, nil
}
