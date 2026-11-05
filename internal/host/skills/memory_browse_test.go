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
	out := structured(t, call(t, "reflect", `{"query":"배포 순서는?","limit":5,"tags":["deploy"],"tags_match":"all","tag_groups":[[{"tags":["x"],"not":true}]],"max_tokens":256}`))
	want := map[string]any{"scope": host.Scope(), "query": "배포 순서는?", "limit": 5, "tags": []any{"deploy"}, "tags_match": "all", "tag_groups": []any{[]any{map[string]any{"tags": []any{"x"}, "not": true}}}, "max_tokens": 256}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/reflect" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if field(out, "answer") != "staging 먼저" || field(out, "authority") != "data_only_untrusted" || field(out, "usage") != nil ||
		canon(t, field(out, "cited_memory_ids")) != canon(t, []any{"m1"}) {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`{}`, `{"query":"  "}`, `{"query":"q","limit":21}`, `{"query":"q","max_tokens":10}`, `{"query":"q","limit":true}`,
		`{"query":"q","tags":"x"}`, `{"query":"q","tags":[3]}`, `{"query":"q","tags_match":"all"}`, `{"query":"q","tags":["a"],"tags_match":"most"}`, `{"query":"q","tag_groups":[[]]}`, `{"query":"q","tags":[""]}`, `{"query":"` + strings.Repeat("가", 2001) + `"}`} {
		if _, err := callTool("reflect", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestReflectPassesLoopSchemaAndMission(t *testing.T) {
	swapRouter(t)
	reply := `{"answer":"{\"day\":\"Fri\"}","structured_answer":{"day":"Fri"},"tool_trace":[{"tool":"recall","args":{"query":"x"},"ids":["m1"]}],"cited_memory_ids":[],"directives_applied":[],"retrieved":[]}`
	calls := fakeRequest(t, reply)
	args := `{"query":"q","max_iterations":4,"response_schema":{"type":"object"},"mission":"for on-call","disposition":{"skepticism":5}}`
	out := structured(t, call(t, "reflect", args))
	want := map[string]any{"scope": host.Scope(), "query": "q", "max_iterations": 4, "response_schema": map[string]any{"type": "object"}, "mission": "for on-call", "disposition": map[string]any{"skepticism": 5}}
	if len(*calls) != 1 || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if canon(t, field(out, "structured_answer")) != canon(t, map[string]any{"day": "Fri"}) || canon(t, field(out, "tool_trace")) == "[]" {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`{"query":"q","max_iterations":9}`, `{"query":"q","response_schema":"x"}`, `{"query":"q","mission":3}`,
		`{"query":"q","disposition":{"skepticism":6}}`, `{"query":"q","disposition":{"mood":1}}`, `{"query":"q","mission":"` + strings.Repeat("가", 2001) + `"}`} {
		if _, err := callTool("reflect", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestReflectPassesBudgetFactTypesAndInclude(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"answer":"a","cited_memory_ids":[],"directives_applied":[],"retrieved":[]}`)
	args := `{"query":"q","budget":"low","fact_types":["observation"],"exclude_mental_model_ids":["mm1"],"exclude_mental_models":false,"apply_all_directives":true,"include":{"tool_calls":{"output":true}}}`
	call(t, "reflect", args)
	want := map[string]any{"scope": host.Scope(), "query": "q", "budget": "low", "fact_types": []any{"observation"}, "exclude_mental_model_ids": []any{"mm1"},
		"exclude_mental_models": false, "apply_all_directives": true, "include": map[string]any{"tool_calls": map[string]any{"output": true}}}
	if len(*calls) != 1 || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	*calls = nil
	for _, bad := range []string{`{"query":"q","budget":"max"}`, `{"query":"q","include":{"chunks":true}}`, `{"query":"q","include":{"tool_calls":{"output":"yes"}}}`,
		`{"query":"q","include":{"tool_calls":{"output":true,"x":1}}}`, `{"query":"q","apply_all_directives":"yes"}`} {
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
		"causal_links":  "/v1/causal-links?" + scope + "&status=active&limit=20",
		// the server pages knowledge sources by an offset cursor
		"knowledge_sources": "/v1/knowledge/sources?" + scope + "&limit=5&cursor=40",
	} {
		args := `{"kind":"` + kind + `"}`
		switch kind {
		case "observations":
			args = `{"kind":"observations","limit":5,"cursor":"c/1","query":"배포"}`
		case "entities":
			args = `{"kind":"entities","query":"arkism"}`
		case "knowledge_sources":
			args = `{"kind":"knowledge_sources","limit":5,"cursor":"40","query":"ignored"}`
		}
		calls := fakeRequest(t, `{"items":[{"id":"x","active":{"text":"ok"},"draft":{"text":"unreviewed"},"draft_diff":[1]}],"next_cursor":"20","truncated":false}`)
		out := structured(t, call(t, "browse_memory", args))
		if len(*calls) != 1 || (*calls)[0].path != path || (*calls)[0].payload != nil {
			t.Fatal(kind, *calls)
		}
		item := asMap(asList(field(out, "items"))[0])
		if field(out, "next_cursor") != "20" || field(out, "truncated") != false || field(out, "authority") != "data_only_untrusted" || item["active"] == nil {
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
	calls = fakeRequest(t, `{"id":"e1","name":"Arkism","aliases":["ark"],"summary":{"text":"approved"},"summary_draft":{"text":"unreviewed"}}`)
	out = structured(t, call(t, "browse_memory", `{"kind":"entities","id":"e1"}`))
	entity := asMap(field(out, "item"))
	if !strings.HasPrefix((*calls)[0].path, "/v1/entities/e1?") || entity["name"] != "Arkism" || entity["summary"] == nil || entity["summary_draft"] != nil {
		t.Fatal(*calls, out)
	}
	calls = fakeRequest(t, `{"id":"mm1","active":{"text":"approved"},"draft":{"text":"unreviewed"},"draft_diff":[1]}`)
	item := asMap(field(structured(t, call(t, "browse_memory", `{"kind":"mental_models","id":"mm1"}`)), "item"))
	if _, hasDraft := item["draft"]; hasDraft || item["draft_diff"] != nil || item["active"] == nil {
		t.Fatal("mental model draft leaked", item)
	}
	*calls = nil
	for _, bad := range []string{`{}`, `{"kind":"reviews"}`, `{"kind":"file_job"}`, `{"kind":"file_job","id":".."}`, `{"kind":"causal_links","id":"c1"}`, `{"kind":"entities","limit":51}`, `{"kind":"entities","limit":true}`,
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
	out := structured(t, call(t, "recall", `{"query":"어제 뭘 정했지","explain":true,"expand_causal":true,"timezone":"Asia/Seoul"}`))
	payload := asMap((*calls)[0].payload)
	if _, hasModes := payload["modes"]; hasModes || payload["explain"] != true || payload["expand_causal"] != true || payload["timezone"] != "Asia/Seoul" {
		t.Fatal(payload)
	}
	if asMap(field(out, "explain"))["time_window"] == nil || !strings.Contains(canon(t, field(out, "memories")), `"explain":{"lane_ranks":{"temporal":1}}`) {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`{"query":"q","explain":"yes"}`, `{"query":"q","expand_causal":1}`, `{"query":"q","timezone":3}`, `{"query":"q","timezone":"Asia/Seoul?x=1"}`, `{"query":"q","timezone":"` + strings.Repeat("a", 65) + `"}`} {
		if _, err := callTool("recall", j(t, bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestRecallPassesQueryTimeRerankDepthAndInclude(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "")
	hit := `{"id":"m1","kind":"decision","status":"active","text":"결정","score":0.05,"lanes":["keyword"],"source_traces":[{"source_id":"s","state":"available","excerpt":"원문"}],"entities":[{"id":"e","name":"billing","type":"service"}]}`
	calls := fakeRequest(t, `{"items":[`+hit+`]}`)
	out := structured(t, call(t, "recall", `{"query":"어제 결정","query_time":"2026-11-04T09:00:00+09:00","rerank":false,"graph_depth":3,"include":["sources","entities"],"min_score":0.02,"tags":["ops"],"tags_match":"all_strict","prefer_observations":true}`))
	payload := asMap((*calls)[0].payload)
	if payload["query_time"] != "2026-11-04T09:00:00+09:00" || payload["rerank"] != false || canon(t, payload["graph_depth"]) != "3" || canon(t, payload["include"]) != `["sources","entities"]` {
		t.Fatal(payload)
	}
	if canon(t, payload["min_score"]) != "0.02" || payload["prefer_observations"] != true || canon(t, payload["filters"]) != `{"tags":["ops"],"tags_match":"all_strict"}` {
		t.Fatal(payload)
	}
	if got := canon(t, field(out, "memories")); !strings.Contains(got, `"source_traces"`) || !strings.Contains(got, `"entities"`) {
		t.Fatal(got)
	}
	*calls = nil
	structured(t, call(t, "recall", `{"query":"q","rerank":"model"}`))
	if payload := asMap((*calls)[0].payload); payload["rerank"] != "model" {
		t.Fatal(payload)
	}
	*calls = nil
	tuning := `"lane_weights":{"graph":2},"temporal_window":{"from":"2026-10-01T00:00:00Z","to":"2026-11-01T00:00:00Z"},"min_scores":{"semantic":0.3},"rerank_options":{"prune":true},"include_options":{"source_facts":{"max_tokens":500}}`
	structured(t, call(t, "recall", `{"query":"q","include":["source_facts"],"tags":["a"],"tag_groups":[[{"not":true,"or":[{"tags":["b"]},{"tags":["c"],"resolve":"fuzzy"}]}]],`+tuning+`}`))
	payload = asMap((*calls)[0].payload)
	if canon(t, payload["filters"]) != `{"tag_groups":[[{"not":true,"or":[{"tags":["b"]},{"resolve":"fuzzy","tags":["c"]}]}]],"tags":["a"]}` ||
		canon(t, payload["lane_weights"]) != `{"graph":2}` || canon(t, payload["rerank_options"]) != `{"prune":true}` || payload["temporal_window"] == nil || payload["min_scores"] == nil || payload["include_options"] == nil {
		t.Fatal(payload)
	}
	*calls = nil
	for _, bad := range []string{`{"query":"q","tag_groups":[]}`, `{"query":"q","tag_groups":[[]]}`, `{"query":"q","tag_groups":["a"]}`, `{"query":"q","lane_weights":[1]}`, `{"query":"q","min_scores":0.5}`, `{"query":"q","include":["sources","entities","source_facts","sources"]}`,
		`{"query":"q","query_time":"yesterday"}`, `{"query":"q","rerank":"no"}`, `{"query":"q","rerank":"Model"}`, `{"query":"q","rerank":1}`, `{"query":"q","graph_depth":4}`, `{"query":"q","graph_depth":1.5}`, `{"query":"q","include":["chunks"]}`, `{"query":"q","include":["sources","sources"]}`, `{"query":"q","min_score":2}`, `{"query":"q","min_score":"0.1"}`, `{"query":"q","tags_match":"all"}`, `{"query":"q","tags":["a"],"tags_match":"most"}`, `{"query":"q","tags":[]}`, `{"query":"q","tags":[1]}`, `{"query":"q","prefer_observations":"yes"}`} {
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
// it still answers "what did we decide yesterday", so the floor keeps it like an exact match;
// a hit the asked-for causal lane found (an approved link from a top hit) is kept the same way.
func TestRecallKeepsTemporalOnlyHitUnderDefaultFloor(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "")
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	fakeRequest(t, `{"items":[{"id":"t1","kind":"episodic","status":"active","text":"어제 배포","score":0.016,"lanes":["temporal"]},`+
		`{"id":"c1","kind":"decision","status":"active","text":"원인","score":0.016,"lanes":["causal"]},`+
		`{"id":"v1","kind":"episodic","status":"active","text":"무관","score":0.016,"lanes":["vector"]}]}`)
	out := structured(t, call(t, "recall", `{"query":"어제 뭐 했지","expand_causal":true}`))
	if got := canon(t, field(out, "memories")); !strings.Contains(got, `"id":"t1"`) || !strings.Contains(got, `"id":"c1"`) || strings.Contains(got, `"id":"v1"`) {
		t.Fatal(got)
	}
}
