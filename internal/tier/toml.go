package tier

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// parse parses the hand-rolled TOML subset used for models.toml.
//
// Supported syntax:
//
//	# comment lines
//	blank lines
//	[tiers.<name>]  — section headers
//	candidates = ["a:p/m", "b:p/m"]  — single-line string arrays
//
// Any other content is rejected. Line numbers in errors are 1-based.
func parse(data []byte) (*Config, error) {
	cfg := &Config{tiers: make(map[string][]Candidate)}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var currentTier string
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section header: [tiers.<name>]
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed section header: %q", lineNum, line)
			}
			inner := line[1 : len(line)-1]
			const prefix = "tiers."
			if !strings.HasPrefix(inner, prefix) {
				return nil, fmt.Errorf("line %d: unsupported section %q (want [tiers.<name>])", lineNum, line)
			}
			name := inner[len(prefix):]
			if name == "" || strings.ContainsAny(name, " \t.[]") {
				return nil, fmt.Errorf("line %d: invalid tier name in section header: %q", lineNum, line)
			}
			currentTier = name
			// Initialise empty candidate slice so override detection works even
			// for tiers with no candidates key yet (parser enforces candidates
			// must follow).
			if _, exists := cfg.tiers[currentTier]; !exists {
				cfg.tiers[currentTier] = nil
			}
			continue
		}

		// Key-value: candidates = [...]
		if strings.HasPrefix(line, "candidates") {
			if currentTier == "" {
				return nil, fmt.Errorf("line %d: candidates key outside of a [tiers.*] section", lineNum)
			}
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

		// Anything else is unexpected.
		return nil, fmt.Errorf("line %d: unexpected content: %q", lineNum, line)
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

// parseCandidatesLine parses a line of the form:
//
//	candidates = ["a:p/m", "b:p/m"]
func parseCandidatesLine(line string, lineNum int) ([]Candidate, error) {
	// Split on '=' and verify key.
	eqIdx := strings.IndexByte(line, '=')
	if eqIdx < 0 {
		return nil, fmt.Errorf("line %d: missing '=' in candidates assignment", lineNum)
	}
	key := strings.TrimSpace(line[:eqIdx])
	if key != "candidates" {
		return nil, fmt.Errorf("line %d: unexpected key %q (want candidates)", lineNum, key)
	}
	val := strings.TrimSpace(line[eqIdx+1:])

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
	colonIdx := strings.IndexByte(s, ':')
	if colonIdx < 0 {
		return Candidate{}, fmt.Errorf("line %d: candidate %q missing ':' separator (want adapter:provider/model)", lineNum, s)
	}
	adapter := s[:colonIdx]
	rest := s[colonIdx+1:]
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx < 0 {
		return Candidate{}, fmt.Errorf("line %d: candidate %q missing '/' separator (want adapter:provider/model)", lineNum, s)
	}
	provider := rest[:slashIdx]
	model := rest[slashIdx+1:]

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
