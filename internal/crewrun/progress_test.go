package crewrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestProgressNotices(t *testing.T) {
	var mu sync.Mutex
	var slack, tg []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/slack") {
			slack = append(slack, body["text"].(string))
		} else {
			tg = append(tg, body["text"].(string))
		}
	}))
	defer srv.Close()

	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "code", "lang", "sh", "code", `echo '"ok"'`),
		node("ok", "approval", "question", "보낼까요?"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "ok"), edge("ok", "end", "approve")}}
	it := wf("w", g)
	it.Crew.Notify = &CrewNotify{
		Channels: []NotifyChannel{{Kind: "slack", Channel: "#리서치", When: "events"}, {Kind: "telegram", Channel: "나에게", When: "approval"}, {Kind: "discord", Channel: "#x", When: "events"}},
		Moments:  []string{"start", "step", "approval", "fail", "end"},
	}
	e := testEngine(t, newFakeStore(it))
	e.TelegramAPI = srv.URL
	// "#리서치" has no destination of its own, so it falls back to slack "default".
	cfg := `{"telegram":{"default":{"bot_token":"1:a","chat_id":"9"}},"slack":{"default":{"webhook_url":"` + srv.URL + `/slack"}}}`
	_ = os.WriteFile(e.NotifyFile, []byte(cfg), 0o600)

	r := runOnce(t, e, "w", nil)
	e.Wait()
	if r.State.Status != "waiting" {
		t.Fatal("run", r.State.Status, statuses(r))
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"[w] 시작했어요", "[w] 단계 끝: a", "[w] 확인이 필요해요: 보낼까요?"}
	if !slices.Equal(slack, want) {
		t.Fatal("slack", slack)
	}
	if !slices.Equal(tg, []string{"[w] 확인이 필요해요: 보낼까요?"}) {
		t.Fatal("telegram (approval only)", tg)
	}
}

// A send that fails is logged; the run carries on.
func TestProgressNoticeFailureKeepsRun(t *testing.T) {
	g := &Graph{Nodes: []Node{node("start", "start"), node("boom", "code", "lang", "sh", "code", "exit 3"), node("end", "end")},
		Edges: []Edge{edge("start", "boom"), edge("boom", "end")}}
	it := wf("w", g)
	it.Crew.Notify = &CrewNotify{Channels: []NotifyChannel{{Kind: "slack", When: "events"}}}
	e := testEngine(t, newFakeStore(it)) // no notify.json: every send fails
	r := runOnce(t, e, "w", nil)
	e.Wait()
	if r.State.Status != "failed" || r.State.Nodes["boom"].Status != "failed" {
		t.Fatal("run", r.State.Status, statuses(r))
	}
}

// A refused output ends the run; no notice queued before that may carry it out.
func TestProgressNoticesDropRefusedText(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, body["text"].(string))
	}))
	defer srv.Close()
	g := &Graph{Nodes: []Node{node("start", "start"), node("ok", "code", "lang", "sh", "code", `echo fine`),
		node("leak", "code", "lang", "sh", "code", `echo sk-live-abc >&2; exit 3`), node("end", "end")},
		Edges: []Edge{edge("start", "ok"), edge("ok", "leak"), edge("leak", "end")}}
	it := wf("w", g)
	it.Crew.Notify = &CrewNotify{Channels: []NotifyChannel{{Kind: "slack", Channel: "#x", When: "events"}}, Moments: []string{"start", "step", "fail", "end"}}
	e := testEngine(t, newFakeStore(it))
	_ = os.WriteFile(e.NotifyFile, []byte(`{"slack":{"default":{"webhook_url":"`+srv.URL+`"}}}`), 0o600)
	r := runOnce(t, e, "w", nil)
	e.Wait()
	mu.Lock()
	defer mu.Unlock()
	if r.State.Status != "failed" || len(sent) == 0 {
		t.Fatal("run", r.State.Status, sent)
	}
	for _, s := range sent {
		if strings.Contains(s, "sk-live-") {
			t.Fatal("notice leaked", s)
		}
	}
}
