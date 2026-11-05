package skills

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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
	"propose_change":        {`{"action":"directive_delete","target_id":"d1","revision":2}`, "/v1/maintenance_proposals"},
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
		"propose_change":        {`{}`, `{"action":"directive_delete","target_id":"a/b"}`, `{"action":"x","params":"text"}`, `{"action":"x","revision":-1}`},
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

func TestProposeChangeSendsAChangeProposal(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"id":"mp_1","status":"open"}`)
	out := structured(t, call(t, "propose_change", `{"action":"space_config_change","params":{"display_name":"Ops"}}`))
	p := asMap((*calls)[0].payload)
	proposal := asMap(p["proposal"])
	change := asMap(proposal["change"])
	if proposal["type"] != "change" || proposal["reason"] != "space_config_change" || asMap(change["params"])["display_name"] != "Ops" || change["target"] != nil {
		t.Fatal(p)
	}
	if field(out, "note") != proposalNote {
		t.Fatal(out)
	}
}

// A recalled procedure that names a file of this repository no longer there is flagged and
// proposed for a person to forget (reason path_missing); other kinds, and paths that cannot be
// judged here (another machine's, a Windows drive, a general memory's relative path), are only flagged.
func TestRecallProposesForgettingProcedureWithMissingPath(t *testing.T) {
	swapRouter(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	calls := fakeRequest(t, `{"id":"p1","status":"proposed"}`)
	found := []any{
		map[string]any{"id": "m1", "revision": json.Number("2"), "kind": "procedural", "text": "Run " + filepath.Join(repo, "scripts", "deploy-old.sh") + " before release."},
		map[string]any{"id": "m2", "revision": json.Number("1"), "kind": "user", "text": "Keep notes in " + filepath.Join(repo, "notes.md")},
		map[string]any{"id": "m3", "revision": json.Number("1"), "kind": "procedural", "text": "Run /home/other/arkism/deploy.sh before release."},
		map[string]any{"id": "m4", "revision": json.Number("1"), "kind": "procedural", "text": `배포 전 C:/Users/felix/tools/check.ps1 를 실행한다.`},
		map[string]any{"id": "m5", "revision": json.Number("1"), "kind": "procedural", "reach": "general", "text": "Run tools/check_old.py before commit."},
		map[string]any{"id": "m6", "revision": json.Number("1"), "kind": "procedural", "text": "Keys live in ~/.config/arkism/old.key"},
		map[string]any{"id": "m7", "revision": json.Number("1"), "kind": "procedural", "reach": "project", "text": "Run tools/gone_check.py before commit."},
	}
	out, err := recallShape(found)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out {
		if v, _ := m.(host.Obj).Get("path_check"); v != "missing" {
			t.Fatal("missing path not flagged", m)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	for i, want := range []string{"m1", "m7"} {
		p := (*calls)[i].payload.(map[string]any)["proposal"].(map[string]any)
		if (*calls)[i].path != "/v1/maintenance_proposals" || p["type"] != "invalidation" || p["reason"] != "path_missing" || asMap(p["memory"])["id"] != want {
			t.Fatalf("proposal %d = %+v", i, p)
		}
	}
}

// The session-archive lane reaches the agent beside the memories, labelled as unreviewed.
func TestSearchExtrasKeepsSessionLaneApart(t *testing.T) {
	out := searchExtras(map[string]any{"sessions": []any{map[string]any{"text": "결제 장애 때 롤백했다"}}})
	if v, _ := out.Get("sessions"); len(asList(v)) != 1 {
		t.Fatal(out)
	}
	if note, _ := out.Get("sessions_note"); !strings.Contains(str(note), "검토한 기억이 아니") {
		t.Fatal(out)
	}
	if empty := searchExtras(map[string]any{}); len(empty) != 0 {
		t.Fatal(empty)
	}
}

// Recalled memories keep their event span and recording time; conversation turns reach the agent apart.
func TestRecallShapeKeepsEventSpanAndConversationLane(t *testing.T) {
	occurred := map[string]any{"from": "2023-05-07T00:00:00Z", "to": "2023-05-07T23:59:59Z"}
	out, err := recallShape([]any{map[string]any{"id": "m1", "text": "Caroline went to a support group on 2023-05-07.", "occurred": occurred, "collected_at": "2023-05-08T10:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	m := out[0].(host.Obj)
	if v, _ := m.Get("occurred"); v == nil {
		t.Fatal("occurred dropped", m)
	}
	if v, _ := m.Get("collected_at"); v != "2023-05-08T10:00:00Z" {
		t.Fatal("collected_at dropped", m)
	}
	extras := searchExtras(map[string]any{"conversation": []any{map[string]any{"text": "[2023-05-08] Caroline: hi", "authority": "data_only_untrusted"}}})
	if v, _ := extras.Get("conversation"); len(asList(v)) != 1 {
		t.Fatal(extras)
	}
}
