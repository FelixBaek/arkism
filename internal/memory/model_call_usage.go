package memory

import "context"

// ModelCallUsage is what a host worker's own model call cost (the digest and
// merge workers run Claude through the CLI, outside memoryd). The worker sends
// it with the result it made from the call; memoryd files it in the
// provider_usage ledger at list price, so 사용량 shows money for those calls.
type ModelCallUsage struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

const modelCallProvider = "claude_cli"

func validModelCallUsage(u *ModelCallUsage) bool {
	return u != nil && validRunLabel(u.Model, 128) && !containsSecret(u.Model) && u.Requests >= 0 && u.Requests <= 20 &&
		u.InputTokens >= 0 && u.InputTokens <= 1e8 && u.OutputTokens >= 0 && u.OutputTokens <= 1e8
}

// recordModelCall is best effort: accounting never refuses or fails the
// result it came with. Callers pass it only after authorizing the request.
func (s *Service) recordModelCall(ctx context.Context, scope Scope, operation string, u *ModelCallUsage) {
	if !validModelCallUsage(u) {
		return
	}
	requests := max(u.Requests, 1)
	reserved, err := s.beginProviderCall(ctx, scope, operation, modelCallProvider, u.Model)
	if err != nil {
		return
	}
	_ = s.finishProviderCall(ctx, reserved, ProviderUsage{Provider: modelCallProvider, Model: u.Model, Requests: requests,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.InputTokens + u.OutputTokens}, true)
}
