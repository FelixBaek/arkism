package skills

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestRecallDigestAsksForApprovedTopicDigests(t *testing.T) {
	swapRouter(t)
	reply := `{"items":[{"id":"dg_1","title":"배포","body":"staging 먼저","authority":"data_only_untrusted"}],"authority":"data_only_untrusted","notice":"data"}`
	calls := fakeRequest(t, reply)
	out := call(t, "recall_digest", `{"query":"배포 순서","limit":2}`)
	want := map[string]any{"scope": host.Scope(), "query": "배포 순서", "limit": 2}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/digests/recall" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if canon(t, structured(t, out)) != canon(t, j(t, reply)) {
		t.Fatal(out)
	}
	if _, err := callTool("recall_digest", j(t, `{"limit":9}`)); err == nil {
		t.Fatal("limit over 5 accepted")
	}
}
