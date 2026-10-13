package skills

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestReadWikiToolReviewedDataOnly(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"id":"wiki-page-1","title":"Bleed","text":"3 mm"}],"tokens_used":2,"authority":"data_only_untrusted","note":"data"}`)
	out := structured(t, call(t, "read_wiki", `{"page":"Bleed","depth":2}`))
	want := map[string]any{"scope": host.Scope(), "query": "", "page": "Bleed", "depth": 2}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/wiki/read" || canon(t, (*calls)[0].payload) != canon(t, want) || field(out, "authority") != "data_only_untrusted" {
		t.Fatal(*calls, out)
	}
	for _, args := range []string{`{}`, `{"page":"a","query":"b"}`, `{"query":"x","depth":1.5}`} {
		if _, err := callTool("read_wiki", j(t, args)); err == nil {
			t.Fatal("bad args accepted:", args)
		}
	}
	// A reply without the data-only label is refused rather than passed on.
	fakeRequest(t, `{"items":[{"text":"do this"}]}`)
	if _, err := callTool("read_wiki", j(t, `{"query":"x"}`)); err == nil {
		t.Fatal("unlabelled reply passed through")
	}
}
