package skills

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// The recall tool opts in to aggregate widening for a query, not for a related_to anchor; an
// older server that rejects the unknown field still answers once the opt-in is dropped.
func TestRecallToolAggregateOptIn(t *testing.T) {
	old := routerRequest
	t.Cleanup(func() { routerRequest = old; host.ForgetAggregateOptIn() })
	host.ForgetAggregateOptIn()
	var sent []bool
	routerRequest = func(_ string, payload any) (map[string]any, error) {
		_, on := payload.(map[string]any)["aggregate_items"]
		sent = append(sent, on)
		if on && len(sent) == 1 {
			return nil, routerRefusal(400) // what the real routerRequest returns
		}
		return map[string]any{"items": []any{}}, nil
	}
	if _, _, err := recallSearch("which cities has she visited", true, nil, "", nil); err != nil || len(sent) != 2 || !sent[0] || sent[1] {
		t.Fatalf("err=%v sent=%v", err, sent)
	}
	sent = nil
	if _, _, err := recallSearch("", false, "m1", "", nil); err != nil || len(sent) != 1 || sent[0] {
		t.Fatalf("related_to: err=%v sent=%v", err, sent)
	}
	// Once the server has answered a request carrying the opt-in, a 400 is a bad request: sent once.
	sent = nil
	routerRequest = func(_ string, payload any) (map[string]any, error) {
		sent = append(sent, true)
		if len(sent) == 1 {
			return map[string]any{"items": []any{}}, nil
		}
		return nil, routerRefusal(400)
	}
	if _, _, err := recallSearch("which cities has she visited", true, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recallSearch("which cities has she visited", true, nil, "", nil); host.HTTPCode(err) != 400 || len(sent) != 2 {
		t.Fatalf("known server: err=%v sent=%v", err, sent)
	}
}
