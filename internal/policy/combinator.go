package policy

import (
	"m31labs.dev/arbiter/vm"
)

// Verdict is the decision outcome from the combinator.
type Verdict string

const (
	VerdictAllow Verdict = "Allow"
	VerdictDeny  Verdict = "Deny"
	VerdictAsk   Verdict = "Ask"
)

// verdictRank returns the ordering for tie-breaking: lower = higher precedence.
// Deny (0) > Ask (1) > Allow (2).
func verdictRank(v Verdict) int {
	switch v {
	case VerdictDeny:
		return 0
	case VerdictAsk:
		return 1
	case VerdictAllow:
		return 2
	default:
		return 3
	}
}

// Decide applies the tiller combinator over the matched rules returned by
// EvalGoverned:
//
//   - Lowest priority number wins.
//   - Ties resolve Deny > Ask > Allow.
//   - Zero matches → Deny with rule="no match", reason="no rule matched".
//
// Returns (verdict, ruleName, reason).
func Decide(matched []vm.MatchedRule) (Verdict, string, string) {
	if len(matched) == 0 {
		return VerdictDeny, "no match", "no rule matched"
	}

	var best vm.MatchedRule
	bestVerdict := Verdict("")
	found := false

	for _, mr := range matched {
		v := Verdict(mr.Action)
		if !found {
			best = mr
			bestVerdict = v
			found = true
			continue
		}
		// Lower priority number wins.
		if mr.Priority < best.Priority {
			best = mr
			bestVerdict = v
			continue
		}
		// Tie in priority: Deny > Ask > Allow.
		if mr.Priority == best.Priority && verdictRank(v) < verdictRank(bestVerdict) {
			best = mr
			bestVerdict = v
		}
	}

	reason := ""
	if r, ok := best.Params["reason"]; ok {
		if s, ok := r.(string); ok {
			reason = s
		}
	}
	return bestVerdict, best.Name, reason
}
