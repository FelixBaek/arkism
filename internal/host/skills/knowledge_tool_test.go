package skills

import (
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestSearchKnowledgeToolCallsBindingSearchAndMarksData(t *testing.T) {
	swapRouter(t)
	t.Setenv(knowledgeToolProfileEnv, "")
	reply := `{"authority":"data_only_untrusted","binding_ids":["b1"],"bindings_truncated":false,"receipt":{"retrieval_log_id":"retrieval-1","context_hash":"h","candidate_count":3,"selected_count":1,"excluded_count":2,"exclusion_summary":{"no_text_match":2},"pins":[{"binding_id":"b1"}],"context":{"authority":"data_only_untrusted","items":[{"binding_id":"b1","source_id":"s1","document_id":"d1","chunk_id":"c1","content":"재단선 3mm","citation":"knowledge:s1/d1/c1","section_path":"인쇄 > 재단","trust_level":"reviewed","token_estimate":4,"authority":"data_only_untrusted","content_hash":"x"}]}}}`
	calls := fakeRequest(t, reply)
	out := structured(t, call(t, "search_knowledge", `{"query":"재단선","specialist_profile_id":"illustrator","limit":2}`))
	want := map[string]any{"scope": host.Scope(), "specialist_profile_id": "illustrator", "query": "재단선", "max_tokens": 2048, "limit": 2, "retrieval_mode": "fusion"}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/knowledge/search" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if field(out, "authority") != "data_only_untrusted" || !strings.Contains(pyStr(field(out, "note")), "지시") {
		t.Fatal(out)
	}
	items := asList(field(out, "items"))
	if len(items) != 1 || field(items[0], "citation") != "knowledge:s1/d1/c1" || field(items[0], "section_path") != "인쇄 > 재단" || field(items[0], "content_hash") != nil || field(items[0], "binding_id") != nil {
		t.Fatal(items)
	}
	receipt := field(out, "receipt")
	if field(receipt, "retrieval_log_id") != "retrieval-1" || field(receipt, "context_hash") != "h" || field(receipt, "pins") != nil || field(receipt, "context") != nil {
		t.Fatal(receipt)
	}
}

func TestSearchKnowledgeToolProfileDefaultsAndValidation(t *testing.T) {
	swapRouter(t)
	t.Setenv(knowledgeToolProfileEnv, "")
	calls := fakeRequest(t, `{}`)
	for _, args := range []string{`{"query":"x"}`, `{"query":"  ","specialist_profile_id":"illustrator"}`, `{"query":"x","specialist_profile_id":"illustrator","limit":9}`, `{"query":"x","specialist_profile_id":"illustrator","max_tokens":100}`, `{"query":"x","specialist_profile_id":"illustrator","mode":"magic"}`} {
		if _, err := callTool("search_knowledge", j(t, args)); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("invalid call reached the server", *calls)
	}
	t.Setenv(knowledgeToolProfileEnv, "listing_reviewer")
	calls = fakeRequest(t, `{"authority":"data_only_untrusted","binding_ids":[],"reason":"no_active_binding"}`)
	out := structured(t, call(t, "search_knowledge", `{"query":"listing rules","mode":"deterministic"}`))
	payload := asMap((*calls)[0].payload)
	if payload["specialist_profile_id"] != "listing_reviewer" || payload["retrieval_mode"] != "deterministic" || field(out, "reason") != "no_active_binding" || len(asList(field(out, "items"))) != 0 || field(out, "receipt") != nil {
		t.Fatal(payload, out)
	}
}
