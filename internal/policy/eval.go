package policy

import (
	"fmt"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/vm"
)

// Route is the routing outcome from the DispatchRoute strategy.
type Route struct {
	Model          string
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
		Model:          stringParam(p, "model"),
		Profile:        stringParam(p, "profile"),
		MaxTurns:       intParam(p, "max_turns"),
		TimeoutMinutes: intParam(p, "timeout_minutes"),
	}

	// Honor --model downgrade: substitute iff the explicit request is a
	// cost downgrade (haiku < sonnet < fable).
	if req.Model != "" && modelCost(req.Model) < modelCost(route.Model) {
		route.Model = req.Model
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

// modelCost returns a comparable cost rank: lower = cheaper.
func modelCost(model string) int {
	switch model {
	case "haiku":
		return 0
	case "sonnet":
		return 1
	case "opus":
		return 2
	case "fable":
		return 3
	default:
		return 1 // unknown = treat as sonnet
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
