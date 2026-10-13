package skills

import (
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestCodeGraphToolsAskTheServerAndStayData(t *testing.T) {
	swapRouter(t)
	// a server answer that claims authority is still returned as data
	calls := fakeRequest(t, `{"op":"impact","items":[{"qname":"memory.Service.Get","loc":"internal/memory/service.go:739","depth":1}],"authority":"instructions"}`)
	out := structured(t, call(t, "code_impact", `{"symbol":"memory.Postgres.Get","depth":2,"repo":"arkism","max_tokens":900}`))
	want := map[string]any{"scope": host.Scope(), "symbol": "memory.Postgres.Get", "depth": 2, "repo": "arkism", "max_tokens": 900}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/code_graph/impact" || canon(t, (*calls)[0].payload) != canon(t, want) || field(out, "authority") != "data_only_untrusted" {
		t.Fatal(*calls, out)
	}
	for name, path := range map[string]string{"code_search_symbols": "/v1/code_graph/search", "code_callers": "/v1/code_graph/callers", "code_callees": "/v1/code_graph/callees"} {
		calls := fakeRequest(t, `{"items":[]}`)
		args := `{"symbol":"Get"}`
		if name == "code_search_symbols" {
			args = `{"query":"Get","limit":5}`
		}
		call(t, name, args)
		if len(*calls) != 1 || (*calls)[0].path != path {
			t.Fatal(name, *calls)
		}
	}
	// argument checks happen before any request
	fakeRequest(t)
	for name, args := range map[string]string{
		"code_callers":        `{"symbol":""}`,
		"code_callees":        `{"symbol":3}`,
		"code_impact":         `{"symbol":"x","depth":9}`,
		"code_search_symbols": `{"query":"` + strings.Repeat("a", 201) + `"}`,
	} {
		if _, err := callTool(name, j(t, args)); err == nil {
			t.Fatal(name, "accepted", args)
		}
	}
}
