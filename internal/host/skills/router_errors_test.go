package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// answer runs one line through the router and returns the error it answered with.
func answer(t *testing.T, line string) (code float64, message string) {
	t.Helper()
	var out bytes.Buffer
	handleMessage(&out, line)
	var got struct {
		Error struct {
			Code    float64
			Message string
		}
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%s -> %q: %v", line, out.String(), err)
	}
	return got.Error.Code, got.Error.Message
}

func TestRouterErrorsSayWhatToDoNext(t *testing.T) {
	stubSpaceTools(t, nil, nil)
	for _, c := range []struct {
		line string
		code float64
		want string
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, -32601, "method not found: resources/list"},
		{`not json`, -32700, "parse error"},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"x"}`, -32602, "params must be an object"},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, -32602, `unknown tool "nope"`},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"enable_tool","arguments":{"name":"x"}}}`, -32602, "enable_tool: missing required argument(s): kind"},
	} {
		if code, message := answer(t, c.line); code != c.code || !strings.Contains(message, c.want) {
			t.Errorf("%s -> %v %q, want %v %q", c.line, code, message, c.code, c.want)
		}
	}
	var out bytes.Buffer
	handleMessage(&out, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	handleMessage(&out, `{"jsonrpc":"2.0","method":"notifications/cancelled"}`)
	if got := strings.TrimSpace(out.String()); got != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Errorf("ping and a notification answered %q", got)
	}
	stubSpaceTools(t, nil, errors.New("down"))
	if _, message := answer(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"router_status","arguments":{}}}`); !strings.Contains(message, "tool_not_enabled: the Arkism server could not be read") {
		t.Errorf("unreachable server answered %q", message)
	}
}
