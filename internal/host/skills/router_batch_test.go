package skills

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// A batch answers as one array, in order, with nothing for its notifications.
func TestHandleMessageAnswersABatch(t *testing.T) {
	var out strings.Builder
	handleMessage(&out, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"nope"},7]`)
	var got []struct {
		ID    any
		Error *struct{ Code int }
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil || len(got) != 3 {
		t.Fatalf("%v: %s", err, out.String())
	}
	if got[0].Error != nil || got[1].Error.Code != -32601 || got[2].Error.Code != -32600 {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	handleMessage(&out, `[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)
	if out.Len() != 0 {
		t.Fatalf("notifications answered: %s", out.String())
	}
	out.Reset()
	handleMessage(&out, `[]`)
	if !strings.Contains(out.String(), "-32600") {
		t.Fatalf("empty batch: %s", out.String())
	}
}

func TestHTTPFailureShowsTheHubReason(t *testing.T) {
	err := &host.HTTPError{Code: 400, Body: []byte(`{"code":"invalid_request","detail":"content: SKILL.md is 70000 bytes, larger than the 64000 byte limit"}`)}
	if got := httpFailure(err); got != "400 invalid_request: content: SKILL.md is 70000 bytes, larger than the 64000 byte limit" {
		t.Fatalf("%v", got)
	}
}
