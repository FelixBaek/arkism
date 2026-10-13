package crew

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestCLIUsageFromClaudeJSON(t *testing.T) {
	v, err := host.Loads(`{"structured_output":{},"usage":{"input_tokens":3,"output_tokens":9},
		"modelUsage":{"claude-haiku-5":{"inputTokens":10,"outputTokens":1},"claude-sonnet-5-5":{"inputTokens":200,"cacheReadInputTokens":50,"cacheCreationInputTokens":25,"outputTokens":40}}}`)
	if err != nil {
		t.Fatal(err)
	}
	u := cliUsage(obj(v), "sonnet")
	if u[0].Value != "claude-sonnet-5-5" || u[2].Value != int64(285) || u[3].Value != int64(41) {
		t.Fatalf("modelUsage: %v", u)
	}
	v, _ = host.Loads(`{"usage":{"input_tokens":7,"cache_read_input_tokens":3,"output_tokens":5}}`)
	if u = cliUsage(obj(v), "haiku"); u[0].Value != "haiku" || u[2].Value != int64(10) || u[3].Value != int64(5) {
		t.Fatalf("usage only: %v", u)
	}
	if u = cliUsage(map[string]any{}, "haiku"); u != nil {
		t.Fatalf("nothing reported: %v", u)
	}
	if got := withUsage(host.Obj{{Key: "a", Value: 1}}, nil); len(got) != 1 {
		t.Fatal("empty usage added", got)
	}
}
