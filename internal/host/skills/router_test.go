package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type reqCall struct {
	path    string
	payload any
}

func j(t *testing.T, s string) map[string]any {
	v, err := host.Loads(s)
	if err != nil {
		panic(err)
	}
	return v.(map[string]any)
}

func swapRouter(t *testing.T) {
	t.Helper()
	kitEnv(t) // route_skills also reads the agents' MCP configs: never this machine's
	req, base, tok, cache, rt, ro, pc, loaded := routerRequest, routerBaseURL, routerTokenFile, routerCacheDir, routerRuntime, receiveOnly, fromPC, loadedSkills
	routerTokenFile = filepath.Join(t.TempDir(), "missing-token") // never the real token
	capState := host.CaptureState
	host.CaptureState = t.TempDir() // record_memory_usage writes the metrics log
	t.Cleanup(func() { host.CaptureState = capState })
	routerCacheDir, fromPC, receiveOnly, loadedSkills = t.TempDir(), false, false, map[string]loadedSkill{}
	sessions, maxChars, pinRoot := routerSessions, skillMaxChars, t.TempDir()
	routerSessions = func() host.SessionStore { return host.SessionStore{Root: pinRoot} }
	t.Cleanup(func() { routerSessions, skillMaxChars = sessions, maxChars })
	t.Cleanup(func() {
		routerRequest, routerBaseURL, routerTokenFile, routerCacheDir, routerRuntime, receiveOnly, fromPC, loadedSkills = req, base, tok, cache, rt, ro, pc, loaded
	})
}

// fakeRequest answers with replies in order (one reply answers every call) and records the calls.
func fakeRequest(t *testing.T, replies ...string) *[]reqCall {
	t.Helper()
	calls := &[]reqCall{}
	routerRequest = func(path string, payload any) (map[string]any, error) {
		*calls = append(*calls, reqCall{path, payload})
		if len(replies) == 0 {
			t.Fatalf("unexpected request %s", path)
		}
		i := min(len(*calls), len(replies)) - 1
		if len(replies) == 1 {
			i = 0
		}
		return j(t, replies[i]), nil
	}
	return calls
}

func structured(t *testing.T, out host.Obj) any {
	t.Helper()
	v, ok := out.Get("structuredContent")
	if !ok {
		t.Fatal(out)
	}
	return v
}

func call(t *testing.T, name string, args string) host.Obj {
	t.Helper()
	out, err := callTool(name, j(t, args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeGolden(t *testing.T, name string, g map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRouterGoldenSessionAndTools(t *testing.T) {
	swapRouter(t)
	g := loadGolden(t, "router.json")
	fakeRequest(t, `{"items":[{"id":"m1","status":"candidate","text":"\u2028한글"}],"a":2,"z":1}`)
	var out strings.Builder
	routerMain(nil, strings.NewReader(g["session_in"].(string)), &out, nil)
	if out.String() != g["session_out"] {
		t.Fatalf("session:\n%s\nwant:\n%s", out.String(), g["session_out"])
	}
	for _, ro := range []bool{false, true} {
		receiveOnly = ro
		out.Reset()
		routerMain(nil, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out, nil)
		key := map[bool]string{false: "tools_list", true: "tools_list_receive_only"}[ro]
		if os.Getenv("ARKISM_UPDATE_GOLDEN") == "1" { // regenerate after adding a tool: ARKISM_UPDATE_GOLDEN=1 go test ./internal/host/skills -run Golden
			g[key] = strings.TrimSuffix(strings.TrimPrefix(out.String(), `{"jsonrpc":"2.0","id":1,"result":`), "}\n")
			writeGolden(t, "router.json", g)
			continue
		}
		if out.String() != `{"jsonrpc":"2.0","id":1,"result":`+g[key].(string)+"}\n" {
			t.Fatalf("%s:\n%s", key, out.String())
		}
	}
}

func TestRecallAtPreservesUnknownsWithoutUse(t *testing.T) {
	swapRouter(t)
	reply := `{"items":[],"unknown":[{"id":"old","reason":"history_not_recorded"}],"next_cursor":"next","authority":"data_only_untrusted"}`
	calls := fakeRequest(t, reply)
	out := call(t, "recall_at", `{"as_of":"2026-10-05T10:00:00Z","query":"결정","cursor":"prior"}`)
	want := map[string]any{"scope": host.Scope(), "as_of": "2026-10-05T10:00:00Z", "q": "결정", "kind": "", "limit": 20, "cursor": "prior"}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/memories/at" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if canon(t, structured(t, out)) != canon(t, j(t, reply)) {
		t.Fatal(out)
	}
}

func TestRecallRelationLaneAndInvalidAnchors(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "owner/repo")
	ref := `{"collection":"memories","id":"old","revision":3}`
	calls := fakeRequest(t, `{"items":[]}`)
	out := structured(t, call(t, "recall", `{"related_to":`+ref+`}`))
	want := map[string]any{"scope": host.Scope(), "query": "related memory evidence", "modes": []any{"relation"}, "purpose": "answer",
		"destination": "internal", "limit": 6, "max_chars": 6000, "related_to": j(t, ref), "project_key": "owner/repo"}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/search" || canon(t, (*calls)[0].payload) != canon(t, want) {
		t.Fatal(*calls)
	}
	if canon(t, field(out, "related_to")) != canon(t, j(t, ref)) || !strings.Contains(field(out, "relation_note").(string), "최대6개") {
		t.Fatal(out)
	}
	*calls = nil
	for _, bad := range []string{`[]`, `{}`, `{"collection":"jobs","id":"old","revision":3}`, `{"collection":"memories","id":"","revision":3}`,
		`{"collection":"memories","id":"` + strings.Repeat("x", 257) + `","revision":3}`, `{"collection":"memories","id":"x\n","revision":3}`,
		`{"collection":"memories","id":"old","revision":true}`, `{"collection":"memories","id":"old","revision":0}`,
		`{"collection":"memories","id":"old","revision":9223372036854775808}`,
		`{"collection":"memories","id":"old","revision":3,"scope":{"project_id":"foreign"}}`} {
		v, _ := host.Loads(bad)
		if _, err := recall("", nil, v); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err := recall("", []any{}, nil); err == nil { // None
		t.Fatal("none accepted")
	}
	if _, err := recall("", []any{"m"}, j(t, ref)); err == nil {
		t.Fatal("ids with related_to accepted")
	}
	if len(*calls) != 0 {
		t.Fatal("network before validation")
	}
}

func TestRecallKeepsConflictSourceAndReplacement(t *testing.T) {
	swapRouter(t)
	for _, byID := range []bool{true, false} {
		memory := `{"id":"m","revision":3,"kind":"decision","status":"active","approved_by":"human","text":"근거","sources":[{"source_id":"source","locator":"original"}],"supersedes":[{"collection":"memories","id":"old","revision":2}],`
		if byID {
			memory += `"conflict_id":"private-relation"}`
		} else {
			memory += `"conflict_pending":true}`
		}
		var out host.Obj
		var err error
		if byID {
			calls := fakeRequest(t, memory)
			out, err = recall("", []any{"m"}, nil)
			if len(*calls) != 1 {
				t.Fatal(*calls)
			}
		} else {
			fakeRequest(t, `{"items":[`+memory+`]}`)
			out, err = recall("근거", nil, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		m := asList(field(out, "memories"))[0].(host.Obj)
		if canon(t, field(m, "supersedes")) != `[{"collection":"memories","id":"old","revision":2}]` || field(m, "conflict_pending") != true ||
			canon(t, field(m, "revision")) != "3" || field(m, "conflict_id") != nil || canon(t, field(m, "sources")) != `[{"locator":"original","source_id":"source"}]` {
			t.Fatal(m)
		}
	}
}

func TestMemoryUseFollowsSessionLink(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"tags":["session:session-a"]}`, `{"items":[{"id":"archive-other","session_id":"session-b"}],"next_cursor":"next"}`,
		`{"items":[{"id":"archive-a","session_id":"session-a"}]}`, `{"status":"recorded"}`)
	v, err := recordMemoryUsage("memory-a", "turn-1")
	if err != nil || len(asList(field(v, "receipts"))) != 1 {
		t.Fatal(v, err)
	}
	last := (*calls)[len(*calls)-1]
	p := last.payload.(map[string]any)
	if last.path != "/v1/session-archives/used" || p["id"] != "archive-a" || p["memory_id"] != "memory-a" || p["event_id"] != "turn-1" {
		t.Fatal(last)
	}
	calls = fakeRequest(t, `{"tags":[]}`)
	if v, err := recordMemoryUsage("memory-a", "turn-1"); err != nil || len(asList(field(v, "receipts"))) != 0 || len(*calls) != 1 {
		t.Fatal(v, err)
	}
	// Both calls leave the local receipt the use judgement counts, linked original or not.
	log, err := os.ReadFile(filepath.Join(host.CaptureState, "metrics.jsonl"))
	if err != nil || strings.Count(string(log), `"event": "memory_used", "event_id": "turn-1", "memory_id": "memory-a"`) != 2 {
		t.Fatal(string(log), err)
	}
}

func TestArchivesAndSourcesDoNotAcknowledgeUse(t *testing.T) {
	swapRouter(t)
	ref := `{"archive_id":"` + strings.Repeat("a", 64) + `","sha256":"` + strings.Repeat("b", 64) + `","message_sha256":"` + strings.Repeat("c", 64) + `","chunk_index":2,"chunk_offset":123}`
	calls := fakeRequest(t, `{"sources":[{"state":"available","archive_messages":[`+ref+`]}],"authority":"data_only_untrusted"}`)
	out := structured(t, call(t, "trace_memory_sources", `{"memory_id":"m/one"}`))
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0].path, "/v1/memories/m%2Fone/sources?") ||
		canon(t, asMap(asList(field(out, "sources"))[0])["archive_messages"]) != canon(t, []any{j(t, ref)}) {
		t.Fatal(*calls, out)
	}
	calls = fakeRequest(t, `{"items":[]}`)
	searchSessionArchives("task context", "", "")
	if len(*calls) != 1 || (*calls)[0].path != "/v1/session-archives/search" {
		t.Fatal(*calls)
	}
	calls = fakeRequest(t, `{"items":[],"next_cursor":"next"}`)
	a64 := strings.Repeat("a", 64)
	out = structured(t, call(t, "search_session_archives", `{"archive_id":"`+a64+`","after":"cursor"}`))
	want := map[string]any{"scope": host.Scope(), "q": "", "after": "cursor", "limit": 50, "archive_id": a64}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/session-archives/search" || canon(t, (*calls)[0].payload) != canon(t, want) || field(out, "next_cursor") != "next" {
		t.Fatal(*calls)
	}
	*calls = nil
	if _, err := searchSessionArchives("", "", "../other"); err == nil || len(*calls) != 0 {
		t.Fatal("bad archive id")
	}
	calls = fakeRequest(t, `{"items":[]}`)
	call(t, "search_session_archives", `{"after":"`+a64+`"}`)
	if (*calls)[0].path != "/v1/session-archives/list" || (*calls)[0].payload.(map[string]any)["after"] != a64 {
		t.Fatal(*calls)
	}
	calls = fakeRequest(t, `{"text":"이전 내용","authority":"data_only_untrusted"}`)
	out = structured(t, call(t, "read_session_message", `{"ref":`+ref+`,"text_offset":4096}`))
	want = map[string]any{"scope": host.Scope(), "ref": j(t, ref), "text_offset": 4096}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/session-archives/message" || canon(t, (*calls)[0].payload) != canon(t, want) || field(out, "authority") != "data_only_untrusted" {
		t.Fatal(*calls)
	}
}

func TestReceiveOnlyBlocksSkillWritesNotMemory(t *testing.T) {
	swapRouter(t)
	receiveOnly = true
	tools, _ := routerTools()
	names := map[string]bool{}
	for _, raw := range tools {
		v, _ := host.Loads(string(raw))
		names[v.(map[string]any)["name"].(string)] = true
	}
	if names["record_skill_usage"] || !names["route_skills"] || !names["load_skill"] || !names["recall"] || !names["remember"] {
		t.Fatal(names)
	}
	loadedSkills["test@1"] = loadedSkill{map[string]any{}, time.Now()}
	if _, err := callTool("record_skill_usage", j(t, `{"ref":"test@1","status":"success"}`)); err == nil || !strings.Contains(err.Error(), "skills_receive_only") {
		t.Fatal(err)
	}
	fakeRequest(t, `{"items":[{"status":"candidate"}]}`)
	if field(call(t, "remember", `{"text":"durable preference"}`), "isError") != false {
		t.Fatal("remember blocked")
	}
}

func TestToolContractStatusAndVersionPattern(t *testing.T) {
	swapRouter(t)
	if versionIDRx.MatchString("skill-router") || !versionIDRx.MatchString("skill-"+strings.Repeat("a", 64)) {
		t.Fatal("version pattern")
	}
	for _, name := range []string{"router_status", "refresh_skills"} {
		calls := fakeRequest(t, `{"status":"ready"}`)
		out := structured(t, call(t, name, `{}`))
		if len(*calls) != 1 || (*calls)[0].path != "/readyz" || (*calls)[0].payload != nil || field(out, "ready") != true {
			t.Fatal(name, *calls)
		}
	}
	if !truthy(host.Scope()["workspace_id"]) {
		t.Fatal("scope")
	}
	tools, _ := routerTools()
	var names []string
	for i, raw := range tools {
		v, _ := host.Loads(string(raw))
		names = append(names, v.(map[string]any)["name"].(string))
		if i < len(host.WorkTools) && canon(t, v) != canon(t, host.WorkTools[i]) {
			t.Fatalf("work tool %d differs from host.WorkTools", i)
		}
	}
	if strings.Join(names, ",") != "save_work,resume_work,list_work,search_session_archives,recall_at,trace_memory_sources,read_session_message,record_memory_usage,route_skills,enable_tool,load_skill,read_skill_file,record_skill_usage,refresh_skills,run_workflow,get_workflow,wait_workflow,cancel_workflow,decide_workflow,list_crew,recall,router_status,remember,search_knowledge,recall_digest,read_wiki,code_search_symbols,code_callers,code_callees,code_impact,recent_sessions,recall_for_role,expand_knowledge_graph,handoff_work" {
		t.Fatal(names)
	}
	if _, err := routeSkills("  "); err == nil {
		t.Fatal("blank query")
	}
}

func TestRecallByIDOrQuery(t *testing.T) {
	swapRouter(t)
	memory := `{"id":"m1","kind":"user","status":"active","approved_by":"human","text":"답은 한국어로","sources":[{"source_id":"s"}],"scope":{}}`
	calls := fakeRequest(t, memory)
	out := structured(t, call(t, "recall", `{"ids":["m1"]}`))
	if !strings.HasPrefix((*calls)[0].path, "/v1/memories/m1?") || field(out, "authority") != "data_only_untrusted" ||
		canon(t, field(out, "memories")) != canon(t, j(t, `{"m":[{"id":"m1","kind":"user","status":"active","text":"답은 한국어로","sources":[{"source_id":"s"}],"conflict_pending":false}]}`)["m"]) {
		t.Fatal(out)
	}
	// key order is the fixed keep order
	if got := host.MustDumps(field(out, "memories"), host.DumpOpts{Compact: true, NoASCII: true}); got != `[{"id":"m1","kind":"user","status":"active","text":"답은 한국어로","sources":[{"source_id":"s"}],"conflict_pending":false}]` {
		t.Fatal(got)
	}
	scored := strings.TrimSuffix(memory, "}") + `,"score":0.0327,"lanes":["exact","keyword"]}`
	calls = fakeRequest(t, `{"items":[`+scored+`]}`)
	out = structured(t, call(t, "recall", `{"query":"답 언어"}`))
	if (*calls)[0].path != "/v1/search" || len(asList(field(out, "memories"))) != 1 {
		t.Fatal(*calls)
	}
	// ST-811: search hits keep the server's fused score and lanes.
	if got := host.MustDumps(field(out, "memories"), host.DumpOpts{Compact: true, NoASCII: true}); !strings.HasSuffix(got, `"conflict_pending":false,"score":0.0327,"lanes":["exact","keyword"]}]`) {
		t.Fatal(got)
	}
	if _, err := recall("  ", []any{}, nil); err == nil {
		t.Fatal("blank recall")
	}
}

// ST-815: the MCP recall tool drops hits below the shared score floor and says nothing related was found.
func TestRecallBelowScoreFloorReturnsNoRelatedMemory(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0.025")
	low := `{"id":"m2","kind":"lesson","status":"active","text":"무관","sources":[{"source_id":"s"}],"scope":{},"score":0.016393,"lanes":["vector"]}`
	fakeRequest(t, `{"items":[`+low+`]}`)
	out := structured(t, call(t, "recall", `{"query":"관련 없는 질문"}`))
	if len(asList(field(out, "memories"))) != 0 || field(out, "result") != host.NoRelatedMemory {
		t.Fatal(out)
	}
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0") // the same setting the hooks read turns the floor off
	fakeRequest(t, `{"items":[`+low+`]}`)
	out = structured(t, call(t, "recall", `{"query":"관련 없는 질문"}`))
	if len(asList(field(out, "memories"))) != 1 || field(out, "result") != nil {
		t.Fatal(out)
	}
}

func swapCrew(t *testing.T) {
	start, decide, list := host.StartWorkflow, host.DecideRun, host.ListCrew
	startRequest, get, wait, cancel := host.StartWorkflowRequest, host.GetWorkflow, host.WaitWorkflow, host.CancelWorkflow
	t.Cleanup(func() {
		host.StartWorkflow, host.DecideRun, host.ListCrew = start, decide, list
		host.StartWorkflowRequest, host.GetWorkflow, host.WaitWorkflow, host.CancelWorkflow = startRequest, get, wait, cancel
	})
}

func TestWorkflowTools(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	var got []any
	host.StartWorkflow = func(handle, task string, wait time.Duration, cwd string) (map[string]any, error) {
		got = []any{handle, task, wait, cwd}
		return j(t, `{"run_id":"run-1","status":"waiting","summary":"","waiting":{"node_id":"ok","question":"보낼까요?"}}`), nil
	}
	out := call(t, "run_workflow", `{"handle":"$리서치","task":"AI 뉴스 정리","cwd":"/tmp"}`)
	v := structured(t, out)
	if got[0] != "$리서치" || got[1] != "AI 뉴스 정리" || got[2] != 30*time.Second || got[3] != "/tmp" ||
		field(v, "run_id") != "run-1" || asMap(field(v, "waiting"))["question"] != "보낼까요?" || field(v, "authority") != "data_only_untrusted" || field(out, "isError") != false {
		t.Fatal(got, out)
	}
	host.StartWorkflow = func(handle, task string, wait time.Duration, cwd string) (map[string]any, error) {
		got = []any{wait}
		return j(t, `{"run_id":"r","status":"failed","summary":"loop_limit","waiting":null}`), nil
	}
	if field(call(t, "run_workflow", `{"handle":"x","task":"y","wait_seconds":0}`), "isError") != true || got[0] != time.Duration(0) {
		t.Fatal(got)
	}
	host.StartWorkflow = func(string, string, time.Duration, string) (map[string]any, error) {
		return nil, &host.CrewError{Msg: "$x(이)라는 워크플로우를 찾지 못했어요"}
	}
	out = call(t, "run_workflow", `{"handle":"x","task":"y"}`)
	text := field(asList(field(out, "content"))[0], "text").(string)
	if field(out, "isError") != true || !strings.Contains(text, "워크플로우를 찾지 못했어요") {
		t.Fatal(out)
	}
	host.DecideRun = func(runID string, approve bool, note string, wait time.Duration) (map[string]any, error) {
		got = []any{runID, approve, note, wait}
		return j(t, `{"run_id":"run-1","status":"succeeded","summary":"보냈어요","waiting":null}`), nil
	}
	out = call(t, "decide_workflow", `{"run_id":"run-1","approve":false,"note":"아직","wait_seconds":30}`)
	if got[0] != "run-1" || got[1] != false || got[2] != "아직" || got[3] != 30*time.Second || field(structured(t, out), "summary") != "보냈어요" || field(out, "isError") != false {
		t.Fatal(got, out)
	}
	rows := `[{"handle":"리서치","type":"workflow","name":"리서치","role":"조사","category":"","trigger":"","enabled":false,"steps":4}]`
	host.ListCrew = func(timeout time.Duration) ([]map[string]any, error) {
		var out []map[string]any
		for _, r := range asList(j(t, `{"r":`+rows+`}`)["r"]) {
			out = append(out, r.(map[string]any))
		}
		return out, nil
	}
	if s := canon(t, structured(t, call(t, "list_crew", `{}`))); s != canon(t, j(t, `{"crew":`+rows+`,"authority":"data_only_untrusted"}`)) {
		t.Fatal(s)
	}
}

func TestRememberIsACandidateAndReportsRejection(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"status":"candidate"}]}`)
	if field(call(t, "remember", `{"text":"답은 짧게","kind":"user"}`), "isError") != false {
		t.Fatal("candidate is error")
	}
	p := (*calls)[0].payload.(map[string]any)
	claim := p["claims"].([]any)[0].(map[string]any)
	if (*calls)[0].path != "/v1/capture" || claim["basis"] != "inferred" || claim["text"] != "답은 짧게" {
		t.Fatal(p)
	}
	for _, text := range []any{"", "    ", strings.Repeat("a", 4001), strings.Repeat("가", 1334), json123()} {
		if _, err := remember(text, "decision"); err == nil {
			t.Fatal(text)
		}
	}
	fakeRequest(t, `{"items":[{"status":"rejected","reason":"secret_detected"}]}`)
	if field(call(t, "remember", `{"text":"invalid"}`), "isError") != true {
		t.Fatal("rejection hidden")
	}
}

func json123() any { v, _ := host.Loads("123"); return v }

const versionReply = `{"id":"skill-v1","skill_id":"agent/example","analysis":{"name":"example","version":"v1","description":"example skill","source_path":"/skills/example/SKILL.md"},"sources":[{"source_id":"source-v1"}],"updated_at":"2026-01-01T00:00:00Z"}`

func TestRouteSkills(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"selected_id":"skill-v1","provider":"rules","judgments":[],"degraded":[]}`, versionReply)
	out, err := routeSkills("example task")
	if err != nil || field(asList(field(out, "skills"))[0], "id") != "agent/example" || len(*calls) != 2 {
		t.Fatal(out, err)
	}
	version := func(i string) string {
		return `{"id":"` + i + `","skill_id":"agent/` + i + `","analysis":{"name":"` + i + `","version":"v1","description":"d","source_path":"/s"},"sources":[],"updated_at":"2026-01-01T00:00:00Z"}`
	}
	fakeRequest(t, `{"selected_id":"","provider":"jev","degraded":["jev_uncertain"],"judgments":[{"id":"skill-a","suitability":0.4},{"id":"skill-b","suitability":1.5},{"id":"skill-c","suitability":0.9}]}`,
		version("skill-b"), version("skill-c"))
	out, err = routeSkills("task")
	if err != nil {
		t.Fatal(err)
	}
	skills := asList(field(out, "skills"))
	if len(skills) != 2 || field(skills[0], "id") != "agent/skill-b" || field(skills[1], "id") != "agent/skill-c" || field(skills[0], "unconfirmed") != true || field(skills[1], "unconfirmed") != true {
		t.Fatal(out)
	}
}

// hubFake is a resolve endpoint whose answer the test sets; offline drops the connection.
type hubFake struct {
	answer   func(payload map[string]any) any
	offline  bool
	payloads []map[string]any
}

func newHubFake(t *testing.T) *hubFake {
	t.Helper()
	h := &hubFake{}
	srv := httptestServer(t, func(payload map[string]any) (any, bool) {
		if h.offline {
			return nil, false
		}
		h.payloads = append(h.payloads, payload)
		return h.answer(payload), true
	})
	routerBaseURL = srv
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("synthetic-token"), 0o600)
	routerTokenFile = token
	return h
}

func TestLoadSkillResolvesThroughTheHub(t *testing.T) {
	swapRouter(t)
	h := newHubFake(t)
	text := "---\nname: example\ndescription: d\n---\nbody\n"
	item := func() map[string]any {
		return map[string]any{"ref": "agent/example@latest-approved", "skill_id": "agent/example", "resolved_version": 2, "version_id": "skill-v2",
			"content_hash": digest([]byte(text)), "instructions": text, "name": "example", "validation_status": "active",
			"requirements": map[string]any{"authority": "requirements_only"}, "provenance_summary": map[string]any{"source_path": "/skills/example/SKILL.md"}}
	}
	h.answer = func(map[string]any) any { return map[string]any{"items": []any{item()}} }
	out, err := loadSkill("agent/example", "")
	if err != nil || field(out, "ref") != "agent/example@2" || field(out, "content") != text || field(out, "resolution_mode") != "hub" {
		t.Fatal(out, err)
	}
	if p := h.payloads[0]; canon(t, p["refs"]) != `["agent/example@latest-approved"]` || p["trial_id"] != nil {
		t.Fatal(p)
	}
	routerRuntime = "codex"
	for _, trial := range []string{"trial-one", "trial-two"} {
		h.answer = func(map[string]any) any {
			it := item()
			it["trial_id"], it["trial_status"], it["validation_status"] = trial, "active", "approved"
			return map[string]any{"items": []any{it}}
		}
		out, err := loadSkill("agent/example", trial)
		p := h.payloads[len(h.payloads)-1]
		if err != nil || field(out, "ref") != "agent/example@2#"+trial || p["trial_id"] != trial || p["runtime"] != "codex" || canon(t, p["refs"]) != `["agent/example@latest-approved"]` {
			t.Fatal(out, err, p)
		}
	}
	if loadedSkills["agent/example@2#trial-one"].item["trial_id"] != "trial-one" {
		t.Fatal("trial not remembered")
	}
	if _, err := loadSkill("agent/example@2", "trial-one"); err == nil {
		t.Fatal("pinned trial accepted")
	}
	h.answer = func(map[string]any) any {
		return map[string]any{"items": []any{map[string]any{"ref": "x@1", "error": "skill_not_found"}}}
	}
	if _, err := loadSkill("x@1", ""); err == nil || err.Error() != "skill_not_found" {
		t.Fatal(err)
	}
}

func TestLoadSkillListsBundleAndReadsStayInside(t *testing.T) {
	swapRouter(t)
	h := newHubFake(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	old := host.SkillsRoot
	host.SkillsRoot = base
	t.Cleanup(func() { host.SkillsRoot = old })
	write := func(rel, text string) {
		p := filepath.Join(base, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	os.MkdirAll(filepath.Join(base, "pack", ".claude-plugin"), 0o755)
	write("pack/scripts/gate.py", "print('gate')\n")
	write("pack/scripts/api.key", "secret")
	write("pack/skills/demo/SKILL.md", "---\nname: demo\ndescription: d\n---\n")
	write("pack/skills/demo/references/rules.md", "rules")
	write("pack/scripts/.env.production/config.json", "secret")
	write("outside.txt", "no")
	skill := filepath.Join(base, "pack", "skills", "demo", "SKILL.md")
	item := func(source string) map[string]any {
		return map[string]any{"ref": "pack/demo@latest-approved", "skill_id": "pack/demo", "resolved_version": 1, "name": "demo", "content_hash": digest([]byte("x")),
			"instructions": "x", "validation_status": "active", "provenance_summary": map[string]any{"source_path": source}}
	}
	h.answer = func(map[string]any) any { return map[string]any{"items": []any{item(skill)}} }
	out, err := loadSkill("pack/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	bundle := field(out, "bundle")
	files := asList(field(bundle, "files"))
	if field(bundle, "root") != "pack" || field(bundle, "skill_dir") != "skills/demo" || canon(t, field(bundle, "root_dirs")) != `{"scripts":1}` ||
		!containsAny(files, "skills/demo/references/rules.md") || field(bundle, "mode") != "unversioned_local" {
		t.Fatal(bundle)
	}
	for _, f := range files {
		if s := f.(string); strings.Contains(s, ".env") || strings.HasSuffix(s, ".key") {
			t.Fatal(s)
		}
	}
	if r, err := readSkillFile("pack/demo@1", "scripts/gate.py"); err != nil || field(r, "content") != "print('gate')\n" {
		t.Fatal(r, err)
	}
	for _, bad := range []string{"../outside.txt", "scripts/api.key", "scripts/.env.production/config.json", "scripts", "/etc/passwd"} {
		if _, err := readSkillFile("pack/demo@1", bad); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err := readSkillFile("pack/other@1", "scripts/gate.py"); err == nil {
		t.Fatal("unloaded ref")
	}
	if localBundle("/etc/hostname") != nil {
		t.Fatal("outside root listed")
	}
	// 폴더가 사라져도 본문 로드는 산다
	h.answer = func(map[string]any) any {
		return map[string]any{"items": []any{item(filepath.Join(base, "moved", "SKILL.md"))}}
	}
	out, err = loadSkill("pack/demo", "")
	if err != nil || field(out, "content") != "x" || field(out, "bundle") != nil {
		t.Fatal(out, err)
	}
}

func containsAny(l []any, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestOnlyTheWindowsMarkerMakesAPC(t *testing.T) {
	t.Setenv("ARKISM_CLIENT", "")
	t.Setenv("SSH_CONNECTION", "1.2.3.4 1 5.6.7.8 22")
	if pcClient() {
		t.Fatal("ssh is not a pc")
	}
	t.Setenv("ARKISM_CLIENT", "pc")
	if !pcClient() {
		t.Fatal("marker ignored")
	}
}

func TestPCClientsLeaveAMarkAndErrorsFlipIt(t *testing.T) {
	swapRouter(t)
	pcTemp(t)
	routerRuntime = "codex"
	lines := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"load_skill","arguments":{"skill_id":""}}}` + "\n"
	fromPC = true
	var out strings.Builder
	routerMain(nil, strings.NewReader(lines), &out, nil)
	state := host.ReadJSONFile(filepath.Join(pcLinkDir, "codex.json"), 1<<20)
	if !truthy(state["last_ok"]) || !truthy(state["last_error"]) {
		t.Fatal(state, out.String())
	}
	pcTemp(t)
	fromPC = false
	routerMain(nil, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out, nil)
	if _, err := os.Stat(pcLinkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("non-PC client left a mark")
	}
}

// ST-824 / TC-SEC-011: recall by id never shows another project's project-reach memory.
func TestRecallByIDKeepsOtherProjectsOut(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_REPO", "owner/mine")
	mine := `{"id":"a","kind":"procedural","reach":"project","reach_project":"owner/mine","status":"active","approved_by":"human","text":"Go로 작성","sources":[{"source_id":"s"}]}`
	other := `{"id":"b","kind":"procedural","reach":"project","reach_project":"owner/other","status":"active","approved_by":"human","text":"Rust로 작성","sources":[{"source_id":"s"}]}`
	fakeRequest(t, mine, other)
	out := structured(t, call(t, "recall", `{"ids":["a","b"]}`))
	got := asList(field(out, "memories"))
	if len(got) != 1 || !strings.Contains(host.MustDumps(got, host.DumpOpts{Compact: true, NoASCII: true}), `"reach":"project"`) {
		t.Fatal(got)
	}
	t.Setenv("ARKISM_REPO", "")
	fakeRequest(t, mine)
	if got := asList(field(structured(t, call(t, "recall", `{"ids":["a"]}`)), "memories")); len(got) != 0 {
		t.Fatal("empty project key showed a project memory", got)
	}
}

// ST-818: a recalled memory naming a file that is not in the working folder is marked, never hidden or reordered.
func TestRecallMarksMissingPaths(t *testing.T) {
	swapRouter(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "here.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	stale := `{"id":"m1","kind":"lesson","status":"active","text":"docs/here.md 와 docs/gone.md 를 본다 https://x.dev/a/b.html","sources":[{"source_id":"s","locator":"/nonexistent-st818/x.go"}]}`
	fresh := `{"id":"m2","kind":"lesson","status":"active","text":"docs/here.md 만 본다","sources":[{"source_id":"s","locator":"original"}]}`
	fakeRequest(t, `{"items":[`+stale+`,`+fresh+`]}`)
	memories := asList(field(structured(t, call(t, "recall", `{"query":"문서"}`)), "memories"))
	if len(memories) != 2 {
		t.Fatal(memories)
	}
	got := host.MustDumps(memories, host.DumpOpts{Compact: true, NoASCII: true})
	if !strings.Contains(got, `"id":"m1"`) || strings.Index(got, `"id":"m1"`) > strings.Index(got, `"id":"m2"`) ||
		!strings.Contains(got, `"path_check":"missing","missing_paths":["docs/gone.md","/nonexistent-st818/x.go"]}`) || strings.Count(got, "path_check") != 1 {
		t.Fatal(got)
	}
}
