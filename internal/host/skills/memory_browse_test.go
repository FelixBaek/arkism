package skills

import (
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestReflectAsksTheServerAndLabelsTheAnswer(t *testing.T) {
	swapRouter(t)
	reply := `{"answer":"staging 먼저","cited_memory_ids":["m1"],"directives_applied":["d1"],"retrieved":[{"id":"m1","kind":"decision","score":0.5}],"usage":{"input_tokens":9}}`
	calls := fakeRequest(t, reply)
	out := structured(t, call(t, "reflect", `{"query":"배포 순서는?","limit":5,"tags":["deploy"],"max_tokens":256}`))
	want := map[string]any{"scope": host.Scope(), "query": "배포 순서는?", "limit": 5, "tags": []any{"deploy"}, "max_tokens": 256}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/reflect" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if field(out, "answer") != "staging 먼저" || field(out, "authority") != "data_only_untrusted" || field(out, "usage") != nil ||
		canon(t, field(out, "cited_memory_ids")) != canon(t, []any{"m1"}) {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`{}`, `{"query":"  "}`, `{"query":"q","limit":21}`, `{"query":"q","max_tokens":10}`, `{"query":"q","limit":true}`,
		`{"query":"q","tags":"x"}`, `{"query":"q","tags":[3]}`, `{"query":"q","tags":[""]}`, `{"query":"` + strings.Repeat("가", 2001) + `"}`} {
		if _, err := callTool("reflect", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestBrowseMemoryListsEachKindReadOnly(t *testing.T) {
	swapRouter(t)
	scope := scopeQueryAll()
	for kind, path := range map[string]string{
		"directives":    "/v1/directives?" + scope + "&limit=20",
		"observations":  "/v1/observations?" + scope + "&limit=5&cursor=c%2F1&q=%EB%B0%B0%ED%8F%AC",
		"mental_models": "/v1/mental-models?" + scope + "&limit=20",
		"entities":      "/v1/entities?" + scope + "&limit=20&q=arkism",
	} {
		args := `{"kind":"` + kind + `"}`
		switch kind {
		case "observations":
			args = `{"kind":"observations","limit":5,"cursor":"c/1","query":"배포"}`
		case "entities":
			args = `{"kind":"entities","query":"arkism"}`
		}
		calls := fakeRequest(t, `{"items":[{"id":"x","active":{"text":"ok"},"draft":{"text":"unreviewed"},"draft_diff":[1]}],"next_cursor":"20"}`)
		out := structured(t, call(t, "browse_memory", args))
		if len(*calls) != 1 || (*calls)[0].path != path || (*calls)[0].payload != nil {
			t.Fatal(kind, *calls)
		}
		item := asMap(asList(field(out, "items"))[0])
		if field(out, "next_cursor") != "20" || field(out, "authority") != "data_only_untrusted" || item["active"] == nil {
			t.Fatal(kind, out)
		}
		if _, hasDraft := item["draft"]; hasDraft != (kind != "mental_models") {
			t.Fatal(kind, "draft handling", item)
		}
	}
}

func TestBrowseMemoryReadsOneItem(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"id":"job/1","status":"running","processed":3}`)
	out := structured(t, call(t, "browse_memory", `{"kind":"file_job","id":"job/1"}`))
	if (*calls)[0].path != "/v1/files/memories/jobs/job%2F1?"+scopeQueryAll() || !strings.Contains(canon(t, field(out, "item")), `"processed":3`) {
		t.Fatal(*calls, out)
	}
	fakeRequest(t, `{"id":"o1","status":"proposed","content":"draft"}`)
	if _, err := callTool("browse_memory", j(t, `{"kind":"observations","id":"o1"}`)); err == nil {
		t.Fatal("unreviewed observation returned")
	}
	calls = fakeRequest(t, `{"id":"e1","name":"Arkism","aliases":["ark"]}`)
	out = structured(t, call(t, "browse_memory", `{"kind":"entities","id":"e1"}`))
	if !strings.HasPrefix((*calls)[0].path, "/v1/entities/e1?") || asMap(field(out, "item"))["name"] != "Arkism" {
		t.Fatal(*calls, out)
	}
	calls = fakeRequest(t, `{"id":"mm1","active":{"text":"approved"},"draft":{"text":"unreviewed"},"draft_diff":[1]}`)
	item := asMap(field(structured(t, call(t, "browse_memory", `{"kind":"mental_models","id":"mm1"}`)), "item"))
	if _, hasDraft := item["draft"]; hasDraft || item["draft_diff"] != nil || item["active"] == nil {
		t.Fatal("mental model draft leaked", item)
	}
	*calls = nil
	for _, bad := range []string{`{}`, `{"kind":"reviews"}`, `{"kind":"file_job"}`, `{"kind":"file_job","id":".."}`, `{"kind":"entities","limit":51}`, `{"kind":"entities","limit":true}`,
		`{"kind":"entities","query":"` + strings.Repeat("a", 201) + `"}`} {
		if _, err := callTool("browse_memory", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestRecallPassesExplainAndTimezoneWithServerLanes(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "")
	hit := `{"id":"m1","kind":"decision","status":"active","text":"어제 결정","sources":[{"source_id":"s"}],"score":0.04,"lanes":["keyword","temporal"],"explain":{"lane_ranks":{"temporal":1}}}`
	calls := fakeRequest(t, `{"items":[`+hit+`],"explain":{"lanes":["exact","keyword","vector","temporal"],"time_window":{"phrase":"어제"}}}`)
	out := structured(t, call(t, "recall", `{"query":"어제 뭘 정했지","explain":true,"timezone":"Asia/Seoul"}`))
	payload := asMap((*calls)[0].payload)
	if _, hasModes := payload["modes"]; hasModes || payload["explain"] != true || payload["timezone"] != "Asia/Seoul" {
		t.Fatal(payload)
	}
	if asMap(field(out, "explain"))["time_window"] == nil || !strings.Contains(canon(t, field(out, "memories")), `"explain":{"lane_ranks":{"temporal":1}}`) {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`{"query":"q","explain":"yes"}`, `{"query":"q","timezone":3}`, `{"query":"q","timezone":"Asia/Seoul?x=1"}`, `{"query":"q","timezone":"` + strings.Repeat("a", 65) + `"}`} {
		if _, err := callTool("recall", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestReadWikiTree(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"folders":[{"path":"ops","pages":1}],"pages":[{"id":"p1","title":"배포"}],"truncated":false,"authority":"data_only_untrusted"}`)
	out := structured(t, call(t, "read_wiki", `{"tree":true}`))
	if (*calls)[0].path != "/v1/wiki/tree?"+scopeQueryAll() || len(asList(field(out, "pages"))) != 1 || field(out, "authority") != "data_only_untrusted" {
		t.Fatal(*calls, out)
	}
	if _, err := callTool("read_wiki", j(t, `{"tree":true,"query":"배포"}`)); err == nil {
		t.Fatal("tree with query accepted")
	}
}

// A hit only the time-word lane found scores one-lane RRF (about 1/61), under the default floor;
// it still answers "what did we decide yesterday", so the floor keeps it like an exact match.
func TestRecallKeepsTemporalOnlyHitUnderDefaultFloor(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "")
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	fakeRequest(t, `{"items":[{"id":"t1","kind":"episodic","status":"active","text":"어제 배포","score":0.016,"lanes":["temporal"]},`+
		`{"id":"v1","kind":"episodic","status":"active","text":"무관","score":0.016,"lanes":["vector"]}]}`)
	out := structured(t, call(t, "recall", `{"query":"어제 뭐 했지"}`))
	if got := canon(t, field(out, "memories")); !strings.Contains(got, `"id":"t1"`) || strings.Contains(got, `"id":"v1"`) {
		t.Fatal(got)
	}
}
