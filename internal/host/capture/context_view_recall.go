package capture

import (
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/receiptchain"
)

// ContextViewRecall is an explicit in-process request to the existing context
// builder. Every budget and encoding is required; nil preserves legacy search.
// This is neither a persistent correction nor permission or learning evidence.
type ContextViewRecall struct {
	Protocol      string
	Scope         receiptchain.Scope
	Exclude       []receiptchain.Ref
	TokenEncoding string
	BudgetTokens  int
	MaxItems      int
}

func freezeRecallView(in *ContextViewRecall, name string) (*ContextViewRecall, error) {
	if in == nil {
		return nil, nil
	}
	raw, e := json.Marshal(capScope())
	var scope receiptchain.Scope
	if e != nil || json.Unmarshal(raw, &scope) != nil {
		return nil, receiptchain.ErrInvalid
	}
	if name != "UserPromptSubmit" || in.Protocol != "context-view-exclusion-v1" || !receiptchain.ValidScope(in.Scope) || in.Scope != scope || in.Exclude == nil || len(in.Exclude) > 128 {
		return nil, receiptchain.ErrInvalid
	}
	if in.MaxItems < 3 || in.MaxItems > 8 || in.BudgetTokens < 1 || in.BudgetTokens > 64000 || (in.TokenEncoding != "cl100k_base" && in.TokenEncoding != "o200k_base") {
		return nil, receiptchain.ErrInvalid
	}
	out := *in
	out.Exclude = append([]receiptchain.Ref{}, in.Exclude...)
	return &out, nil
}
func recallWithView(query, project string, limit int, timeout float64, observe searchResponseObserver, view *ContextViewRecall) ([]any, bool, error) {
	if view == nil {
		return searchMemoriesObserved(query, project, limit, timeout, observe)
	}
	deadline := monotonic() + timeout
	body := map[string]any{"scope": capScope(), "task": head(query, 1000), "modes": []any{"exact", "fts", "vector"}, "destination": "internal", "max_items": view.MaxItems, "budget_tokens": view.BudgetTokens, "token_encoding": view.TokenEncoding, "context_view": map[string]any{"protocol": view.Protocol, "context_scope": view.Scope, "exclude": view.Exclude}}
	if project != "" {
		body["project_key"] = project
	}
	return recallPostObserved("/v1/context/build", body, timeout, deadline, observe)
}
