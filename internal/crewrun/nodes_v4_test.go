package crewrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpertNodeUsesExpertDefaults(t *testing.T) {
	ex := CrewItem{ID: "ex1", Revision: 1, Crew: Crew{Type: "agent", Name: "리서처", Model: "deepseek-v4", Effort: "high", Instructions: "출처를 확인한다."}}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "expert", "expert", "ex1", "instructions", "짧게.", "goal", "조사: {{in.q}}"),
		node("b", "expert", "expert", "ex1", "model", "glm-5", "effort", "low"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "b"), edge("b", "end")}}
	e := testEngine(t, newFakeStore(ex, wf("w", g)))
	log := fakeAI(t, e)
	r := runOnce(t, e, "w", map[string]any{"q": "x"})
	got := specs(t, log)
	if r.State.Status != "succeeded" || len(got) != 2 {
		t.Fatal("expert run", r.State.Status, statuses(r), len(got))
	}
	if got[0].Model != "deepseek-v4" || got[0].Effort != "high" || got[0].Instructions != "출처를 확인한다.\n\n짧게." || got[0].Task != "조사: x" {
		t.Fatalf("defaults %+v", got[0])
	}
	if got[1].Model != "glm-5" || got[1].Effort != "low" {
		t.Fatalf("node wins %+v", got[1])
	}
	g.Nodes[1].Config["expert"] = "nope"
	if r = runOnce(t, e, "w", nil); r.State.Status != "failed" || !strings.HasPrefix(r.State.Nodes["a"].Error, "bad_config") {
		t.Fatal("missing expert", r.State.Nodes["a"].Error)
	}
}

type creatingMeetings struct{ *fakeMeetings }

func (c creatingMeetings) CreateMeeting(_ context.Context, teamID, title, topic string) (MeetingRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := MeetingRecord{ID: "m-new", Revision: 1, Meeting: Meeting{Status: "pending", TeamID: teamID, TeamRevision: 3, Title: title, Topic: topic,
		Trigger: "workflow", MaxRounds: 1, Turns: []MeetingTurn{}, NotesPending: []MeetingNote{}}}
	c.recs[rec.ID] = rec
	return rec, nil
}

func TestTeamNodeRunsMeetingAndOutputsConclusion(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.e.Meetings = creatingMeetings{rig.store}
	out, err := rig.e.runTeam(context.Background(), map[string]string{"team": "team", "topic": "검토: {{in.pr}}"}, map[string]any{"pr": 12})
	m, _ := out.(map[string]any)
	if err != nil || m["summary"] != "머지한다" || m["meeting_id"] != "m-new" {
		t.Fatal("team", out, err)
	}
	if got := rig.store.recs["m-new"].Meeting; got.Topic != "검토: 12" || got.Trigger != "workflow" {
		t.Fatalf("meeting %+v", got)
	}
	if _, err = rig.e.runTeam(context.Background(), map[string]string{"team": "agent-x"}, nil); err == nil || !strings.HasPrefix(err.Error(), "bad_config") {
		t.Fatal("unknown team", err)
	}
}

func TestN8NSyncAndAsync(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		if cb, _ := seen["callback_url"].(string); cb != "" {
			go func() { // n8n answers later, to the callback
				time.Sleep(50 * time.Millisecond)
				resp, err := http.Post(cb, "application/json", bytes.NewReader([]byte(`{"done":true}`)))
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			w.WriteHeader(200)
			return
		}
		_, _ = w.Write([]byte(`{"echo":"` + seen["input"].(string) + `"}`))
	}))
	defer srv.Close()
	e := testEngine(t, newFakeStore())
	out, err := e.runN8N(context.Background(), map[string]string{"url": srv.URL, "message": "안녕 {{in.a}}"}, map[string]any{"a": "x"})
	if m, _ := out.(map[string]any); err != nil || m["echo"] != "안녕 x" {
		t.Fatal("sync", out, err)
	}
	cb := httptest.NewServer(e.Handler(context.Background()))
	defer cb.Close()
	e.PublicURL = cb.URL
	out, err = e.runN8N(context.Background(), map[string]string{"url": srv.URL, "mode": "async", "timeout": "5s"}, "hi")
	if m, _ := out.(map[string]any); err != nil || m["done"] != true {
		t.Fatal("async", out, err)
	}
	if _, err = e.runN8N(context.Background(), map[string]string{"url": "ftp://x"}, nil); err == nil || !strings.HasPrefix(err.Error(), "bad_config") {
		t.Fatal("bad url", err)
	}
	resp, _ := http.Post(cb.URL+"/callbacks/unknown", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Fatal("unknown token", resp.StatusCode)
	}
	e.PublicURL = ""
	if _, err = e.runN8N(context.Background(), map[string]string{"url": srv.URL, "mode": "async"}, nil); err == nil {
		t.Fatal("async needs a public url")
	}
}
