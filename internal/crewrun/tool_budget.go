package crewrun

import "context"

// ToolCallLimit is the owner's initial cap for one logical AI step, including
// its runner retries and model fallback. Counts are reported after dispatch.
const ToolCallLimit = 50

type aiToolUsage struct {
	Protocol      string `json:"protocol"`
	Limit         int    `json:"limit"`
	ObservedCalls int    `json:"observed_calls"`
	Complete      bool   `json:"complete"`
	Exceeded      bool   `json:"exceeded"`
	Enforcement   string `json:"enforcement"`
}

type aiToolBudgetKey struct{}
type aiToolBudget struct {
	observed int
	complete bool
	seen     bool
}

// Only a single logical call's sequential retry/fallback chain shares this
// state. Independently invoked calls receive independent budgets.
func withAIToolBudget(ctx context.Context) (context.Context, *aiToolBudget) {
	if b, ok := ctx.Value(aiToolBudgetKey{}).(*aiToolBudget); ok {
		return ctx, b
	}
	b := &aiToolBudget{}
	return context.WithValue(ctx, aiToolBudgetKey{}, b), b
}

func (b *aiToolBudget) summary() *aiToolUsage {
	return &aiToolUsage{Protocol: "crew-tool-calls-v1", Limit: ToolCallLimit, ObservedCalls: b.observed, Complete: b.complete, Exceeded: b.observed > ToolCallLimit, Enforcement: "post_run"}
}

func (b *aiToolBudget) consume(answer *aiAnswer, err error) error {
	u := answer.ToolUsage
	if u == nil {
		return err
	} // older/custom runners remain explicitly unobserved
	if u.ObservedCalls < 0 || u.Protocol != "crew-tool-calls-v1" || u.Limit != ToolCallLimit {
		return stopAITools(answer, "tool_call_count_unknown")
	}
	b.observed += u.ObservedCalls
	b.complete = (!b.seen || b.complete) && u.Complete
	b.seen = true
	answer.ToolUsage = b.summary()
	if b.observed > ToolCallLimit {
		return stopAITools(answer, "tool_call_limit_exceeded")
	}
	if !u.Complete && u.Enforcement == "post_run" && answer.Error != "cli_not_found" {
		return stopAITools(answer, "tool_call_count_unknown")
	}
	return err
}

func stopAITools(answer *aiAnswer, code string) error {
	answer.OK, answer.Error, answer.Stopped = false, code, code
	return failf("ai_failed", "%s", code)
}

func (b *aiToolBudget) exhausted(model string) (aiAnswer, error) {
	answer := aiAnswer{Model: model, ToolUsage: b.summary()}
	err := stopAITools(&answer, "tool_call_limit_reached")
	return answer, err
}

func (b *aiToolBudget) answerOnly(answer aiAnswer) aiAnswer {
	kept := observationAnswerOnly(answer)
	if b.seen {
		kept.ToolUsage = b.summary()
	}
	return kept
}
