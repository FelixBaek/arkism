package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"reflect"
	"testing"
	"time"
)

func explicitRecallView(scope receiptchain.Scope) *ContextViewRecall {
	return &ContextViewRecall{Protocol: "context-view-exclusion-v1", Scope: scope, Exclude: []receiptchain.Ref{}, TokenEncoding: "cl100k_base", BudgetTokens: 2000, MaxItems: 6}
}
func TestProductionViewRecallCollectorIndependent(t *testing.T) {
	for _, kind := range []string{"nil", "closed", "wrong_reader", "valid"} {
		t.Run(kind, func(t *testing.T) {
			checkProductionViewCollector(t, kind)
		})
	}
}
func TestProductionViewRecallErrorNoFallback(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	calls := 0
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		calls++
		if path != "/v1/context/build" {
			t.Fatal(path)
		}
		return nil, &host.HTTPError{Code: 400}
	}
	var out bytes.Buffer
	e := HookWithReceiptChainView(map[string]any{"session_id": "reader", "prompt": "production view failure"}, "UserPromptSubmit", &out, nil, explicitRecallView(scope))
	if e == nil || out.Len() != 0 || calls != 1 {
		t.Fatal("masked failure broadened", e, calls)
	}
}
func TestProductionViewRecallRequiredConfiguration(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	for _, mutate := range []func(*ContextViewRecall){func(v *ContextViewRecall) { v.Scope.UserID = "foreign" }, func(v *ContextViewRecall) { v.Protocol = "unknown" }, func(v *ContextViewRecall) { v.Exclude = nil }, func(v *ContextViewRecall) { v.MaxItems = 2 }, func(v *ContextViewRecall) { v.MaxItems = 9 }, func(v *ContextViewRecall) { v.BudgetTokens = 0 }, func(v *ContextViewRecall) { v.BudgetTokens = 64001 }, func(v *ContextViewRecall) { v.TokenEncoding = "" }, func(v *ContextViewRecall) { v.TokenEncoding = "unknown" }} {
		v := explicitRecallView(scope)
		mutate(v)
		capPost = func(string, any, time.Duration) (map[string]any, error) {
			t.Fatal("invalid view requested")
			return nil, nil
		}
		if e := HookWithReceiptChainView(map[string]any{"session_id": "reader", "prompt": "production invalid view"}, "UserPromptSubmit", &bytes.Buffer{}, nil, v); e == nil {
			t.Fatal("invalid accepted")
		}
	}
}
func TestProductionViewRecallRetriesPreserveSelector(t *testing.T) {
	for _, kind := range []string{"503", "degraded"} {
		t.Run(kind, func(t *testing.T) {
			checkProductionViewRetry(t, kind)
		})
	}
}
func TestProductionViewNilLegacyWire(t *testing.T) {
	scope, _ := receiptHookFixture(t)
	_ = scope
	calls := []map[string]any{}
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/search" {
			t.Fatal(path)
		}
		raw, _ := json.Marshal(body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		calls = append(calls, b)
		return map[string]any{"items": []any{}}, nil
	}
	_, _, a := searchMemoriesObserved("query", "repo", 6, 8, nil)
	_, _, b := recallWithView("query", "repo", 6, 8, nil, nil)
	if !errors.Is(a, b) || len(calls) != 2 || !reflect.DeepEqual(calls[0], calls[1]) || calls[0]["max_chars"] != float64(6000) || calls[0]["context_view"] != nil {
		t.Fatal("legacy wire changed")
	}
}

func checkProductionViewCollector(t *testing.T, kind string) {
	t.Helper()

	scope, items := receiptHookFixture(t)
	view := explicitRecallView(scope)
	view.Exclude = []receiptchain.Ref{{Collection: "memories", ID: "chain-a", Revision: 1}}
	var chain *receiptchain.Session
	if kind != "nil" {
		chain = receiptHookSession(t, scope, "reader")
		if kind == "closed" {
			chain.Freeze()
		}
		if kind == "wrong_reader" {
			chain = receiptHookSession(t, scope, "other")
		}
	}
	calls := 0
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		calls++
		if path != "/v1/context/build" {
			t.Fatal("unmasked request", path)
		}
		raw, _ := json.Marshal(body)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if got["budget_tokens"] != float64(2000) || got["max_items"] != float64(6) || got["token_encoding"] != "cl100k_base" || got["context_view"] == nil || got["max_chars"] != nil || got["query"] != nil {
			t.Fatal("view wire", got)
		}
		return map[string]any{"context_id": "view-context", "items": items[1:]}, nil
	}
	var out bytes.Buffer
	if e := HookWithReceiptChainView(map[string]any{"session_id": "reader", "prompt": "production view recall"}, "UserPromptSubmit", &out, chain, view); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || bytes.Contains(out.Bytes(), []byte("BODY_CANARY chain-a")) {
		t.Fatal("view ignored")
	}
}

func checkProductionViewRetry(t *testing.T, kind string) {
	t.Helper()

	scope, items := receiptHookFixture(t)
	v := explicitRecallView(scope)
	v.Exclude = []receiptchain.Ref{{Collection: "memories", ID: "chain-a", Revision: 1}}
	var bodies []map[string]any
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/context/build" {
			t.Fatal(path)
		}
		raw, _ := json.Marshal(body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			if kind == "503" {
				return nil, &host.HTTPError{Code: 503}
			}
			return map[string]any{"items": items[1:], "degraded": []any{"memory_vector_unavailable"}}, nil
		}
		return map[string]any{"items": items[1:]}, nil
	}
	if _, _, e := recallWithView("production retry", "repo", 6, 8, nil, v); e != nil {
		t.Fatal(e)
	}
	if len(bodies) != 2 {
		t.Fatal(len(bodies))
	}
	for _, b := range bodies {
		if !reflect.DeepEqual(b["context_view"], bodies[0]["context_view"]) || b["budget_tokens"] != float64(2000) {
			t.Fatal("retry view changed")
		}
	}
	if kind == "degraded" && !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatal("degraded retry changed body")
	}
}
