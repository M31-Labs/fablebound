package policy

import (
	"fmt"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
)

// Route is the routing outcome from the DispatchRoute strategy.
type Route struct {
	Tier           string // reason|scrutiny|execute
	Profile        string // orchestrator | insight | readonly | execution
	MaxTurns       int
	TimeoutMinutes int
}

// DispatchResult is the full result of evaluating a DispatchRequest.
type DispatchResult struct {
	Verdict   Verdict
	Rule      string
	Reason    string
	Route     Route
	Matched   []vm.MatchedRule
	Arbitrace *govern.Arbitrace
}

// ToolCallResult is the full result of evaluating a ToolCallRequest.
type ToolCallResult struct {
	Verdict   Verdict
	Rule      string
	Reason    string
	Arbitrace *govern.Arbitrace
}

// EvalDispatch evaluates a DispatchRequest against the loaded dispatch policy.
// On Allow it also evaluates the DispatchRoute strategy to obtain routing.
// On Allow, if req.Model is non-empty and represents a cost downgrade vs the
// routed model, the explicit model is substituted (§6.3).
func EvalDispatch(loaded *Loaded, req DispatchRequest) (DispatchResult, error) {
	prog := loaded.Prog
	ctx := ContextMap(req)
	dc := arbiter.DataFromStruct(req, prog)

	matched, trace, err := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("EvalGoverned(dispatch): %w", err)
	}

	verdict, rule, reason := Decide(matched)
	result := DispatchResult{
		Verdict:   verdict,
		Rule:      rule,
		Reason:    reason,
		Matched:   matched,
		Arbitrace: trace,
	}

	if verdict != VerdictAllow {
		return result, nil
	}

	// Evaluate routing strategy.
	stratRes, err := prog.Strategies.Evaluate("DispatchRoute", ctx)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("DispatchRoute strategy: %w", err)
	}

	p := stratRes.Params
	route := Route{
		Tier:           stringParam(p, "tier"),
		Profile:        stringParam(p, "profile"),
		MaxTurns:       intParam(p, "max_turns"),
		TimeoutMinutes: intParam(p, "timeout_minutes"),
	}

	// Honor --tier downgrade: substitute iff the explicit request is a
	// cost downgrade (execute < scrutiny < reason).
	if req.Tier != "" && tierCost(req.Tier) < tierCost(route.Tier) {
		route.Tier = req.Tier
	}

	result.Route = route
	return result, nil
}

// EvalToolCall evaluates a ToolCallRequest against the loaded toolgate policy.
func EvalToolCall(loaded *Loaded, req ToolCallRequest) (ToolCallResult, error) {
	prog := loaded.Prog
	ctx := ContextMap(req)
	dc := arbiter.DataFromStruct(req, prog)

	matched, trace, err := arbiter.EvalGoverned(prog, dc, prog.Segments, ctx)
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("EvalGoverned(toolgate): %w", err)
	}

	verdict, rule, reason := Decide(matched)
	return ToolCallResult{
		Verdict:   verdict,
		Rule:      rule,
		Reason:    reason,
		Arbitrace: trace,
	}, nil
}

// tierCost returns a comparable cost rank: lower = cheaper.
func tierCost(tier string) int {
	switch tier {
	case "execute":
		return 0
	case "scrutiny":
		return 1
	case "reason":
		return 2
	default:
		return 0 // unknown = treat as execute
	}
}

func stringParam(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	v, _ := p[key].(string)
	return v
}

func intParam(p map[string]any, key string) int {
	if p == nil {
		return 0
	}
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}
