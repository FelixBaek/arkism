package capture

import (
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The prompt recall opts in to aggregate widening; an older server that rejects the
// unknown field still answers once the opt-in is dropped.
func TestPromptRecallAggregateOptIn(t *testing.T) {
	receiptHookFixture(t)
	host.ForgetAggregateOptIn()
	t.Cleanup(host.ForgetAggregateOptIn)
	var sent []bool
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		_, on := body.(map[string]any)["aggregate_items"]
		sent = append(sent, on)
		if on && len(sent) == 1 {
			return nil, &host.HTTPError{Code: 400}
		}
		return map[string]any{"items": []any{map[string]any{"id": "m1"}}}, nil
	}
	items, _, err := searchMemoriesObserved("which cities has she visited", "repo", 6, 8, nil)
	if err != nil || len(items) != 1 || len(sent) != 2 || !sent[0] || sent[1] {
		t.Fatalf("items=%v err=%v sent=%v", items, err, sent)
	}
}
