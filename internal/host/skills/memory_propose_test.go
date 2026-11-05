package skills

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// proposalTools maps each memory write tool to valid arguments and the one route it must call.
var proposalTools = map[string][2]string{
	"propose_directive":     {`{"title":"Units","rule":"Use metric units."}`, "/v1/directives"},
	"propose_mental_model":  {`{"title":"Deploys","query":"deploy order"}`, "/v1/mental-models"},
	"propose_memory_update": {`{"memory_id":"m1","revision":3,"text":"Deploy on Thursday."}`, "/v1/capture"},
	"propose_invalidation":  {`{"memory_id":"m1","revision":3,"reason":"incorrect"}`, "/v1/maintenance_proposals"},
	"add_document":          {`{"filename":"notes.md","text":"배포는 목요일"}`, "/v1/files/memories"},
	"cancel_job":            {`{"id":"job1"}`, "/v1/operations/job1/cancel"},
	"read_document":         {`{"source":"file","id":"doc1"}`, "/v1/files/memories/documents/doc1/chunks?"},
	"read_space_config":     {`{}`, "/v1/spaces/"},
}

// Routes that approve, review, decide, transition, supersede or activate anything.
var activatingRoute = regexp.MustCompile(`/(review|review_batch|decide|confirm|transition|supersede|expiry|restore|approve|activate|run|refresh)\b`)

func TestNoMemoryWriteToolCanMakeAnythingActive(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"status":"candidate"}],"id":"x","status":"proposed"}`)
	for name, c := range proposalTools {
		*calls = nil
		call(t, name, c[0])
		if len(*calls) != 1 || !strings.HasPrefix((*calls)[0].path, c[1]) || activatingRoute.MatchString((*calls)[0].path) {
			t.Fatal(name, *calls)
		}
		if p, ok := (*calls)[0].payload.(map[string]any); ok {
			if p["active"] == true || p["status"] != nil || p["approved"] != nil {
				t.Fatal(name, "asked for an active state", p)
			}
		}
	}
	// The directive asks for inactive; the server stores an agent's as proposed anyway.
	*calls = nil
	call(t, "propose_directive", proposalTools["propose_directive"][0])
	if (*calls)[0].payload.(map[string]any)["active"] != false {
		t.Fatal((*calls)[0].payload)
	}
}

func TestProposeMemoryUpdateSendsACorrectionCandidate(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"status":"candidate","candidate_id":"c1"}]}`)
	out := call(t, "propose_memory_update", `{"memory_id":"m1","revision":3,"text":"Deploy on Thursday.","kind":"procedural"}`)
	claim := (*calls)[0].payload.(map[string]any)["claims"].([]any)[0].(map[string]any)
	want := map[string]any{"collection": "memories", "id": "m1", "revision": 3}
	if canon(t, claim["supersedes"]) != canon(t, want) || claim["kind"] != "procedural" || claim["text"] != "Deploy on Thursday." {
		t.Fatal(claim)
	}
	if isErr, _ := out.Get("isError"); isErr != false {
		t.Fatal(out)
	}
	*calls = nil
	call(t, "remember", `{"text":"plain"}`)
	if c := (*calls)[0].payload.(map[string]any)["claims"].([]any)[0].(map[string]any); c["supersedes"] != nil {
		t.Fatal("remember sent supersedes", c)
	}
}

func TestMemoryWriteToolPayloads(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"id":"x"}`)
	call(t, "propose_invalidation", `{"memory_id":"m1","revision":2}`)
	want := map[string]any{"scope": host.Scope(), "proposal": map[string]any{"type": "invalidation", "reason": "outdated",
		"memory": map[string]any{"collection": "memories", "id": "m1", "revision": 2}}}
	if canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal((*calls)[0].payload)
	}
	*calls = nil
	call(t, "add_document", `{"filename":"notes.md","text":"배포는 목요일","dry_run":true}`)
	p := (*calls)[0].payload.(map[string]any)
	if p["content_base64"] != base64.StdEncoding.EncodeToString([]byte("배포는 목요일")) || p["dry_run"] != true || p["filename"] != "notes.md" {
		t.Fatal(p)
	}
	*calls = nil
	call(t, "read_document", `{"source":"knowledge","id":"k1","view":"history","cursor":"a b","limit":5}`)
	if got := (*calls)[0].path; !strings.HasPrefix(got, "/v1/knowledge/sources/k1/history?") || !strings.HasSuffix(got, "&limit=5&cursor=a+b") || (*calls)[0].payload != nil {
		t.Fatal(got)
	}
	*calls = nil
	call(t, "cancel_job", `{"id":"j1","expected_revision":4}`)
	if canon(t, (*calls)[0].payload) != canon(t, map[string]any{"scope": host.Scope(), "expected_revision": 4}) {
		t.Fatal((*calls)[0].payload)
	}
	*calls = nil
	call(t, "browse_memory", `{"kind":"jobs"}`)
	call(t, "browse_memory", `{"kind":"knowledge_sources","limit":5}`)
	if !strings.HasPrefix((*calls)[0].path, "/v1/operations?") || !strings.HasPrefix((*calls)[1].path, "/v1/knowledge/sources?") {
		t.Fatal(*calls)
	}
}

func TestMemoryWriteToolsValidateBeforeTheNetwork(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{}`)
	for name, bad := range map[string][]string{
		"propose_directive":     {`{"rule":"r"}`, `{"title":"t","rule":"r","applies_to":"all"}`, `{"title":"t","rule":"r","priority":101}`, `{"title":"t","rule":"r","priority":true}`},
		"propose_mental_model":  {`{"title":"a\nb","query":"q"}`, `{"title":"` + strings.Repeat("가", 121) + `","query":"q"}`, `{"title":"t"}`},
		"propose_memory_update": {`{"memory_id":"m1","text":"x"}`, `{"memory_id":"a/b","revision":1,"text":"x"}`, `{"memory_id":"..","revision":1,"text":"x"}`, `{"memory_id":"m1","revision":0,"text":"x"}`},
		"propose_invalidation":  {`{"memory_id":"m1"}`, `{"memory_id":"m1","revision":1,"reason":"boring"}`},
		"add_document":          {`{"filename":"a.md"}`, `{"filename":"a.md","text":"x","content_base64":"eA=="}`, `{"filename":"../a.md","text":"x"}`, `{"filename":"a.md","text":""}`},
		"cancel_job":            {`{}`, `{"id":"a/b"}`, `{"id":"j","expected_revision":-1}`},
		"read_document":         {`{"source":"web","id":"d"}`, `{"source":"file","id":"d","view":"raw"}`, `{"source":"file","id":"d","limit":201}`},
	} {
		for _, args := range bad {
			if _, err := callTool(name, j(t, args)); err == nil {
				t.Fatal(name, args)
			}
		}
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation", *calls)
	}
}

func TestMemoryWriteToolsFollowTheSpaceAllowlist(t *testing.T) {
	swapRouter(t)
	stubSpaceTools(t, []string{"recall", "propose_invalidation"}, nil)
	fakeRequest(t, `{"id":"x"}`)
	for name, c := range proposalTools {
		_, err := callTool(name, j(t, c[0]))
		if name == "propose_invalidation" && err != nil || name != "propose_invalidation" && (err == nil || !strings.Contains(err.Error(), "tool_not_enabled")) {
			t.Fatal(name, err)
		}
	}
}
