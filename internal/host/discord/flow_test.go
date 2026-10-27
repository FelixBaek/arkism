package discord

import (
	"io"
	"strings"
	"testing"
	"time"
)

func startLegacyAssist(t *testing.T, h *harness, topic string) {
	t.Helper()
	b := h.m.bots["bot1"]
	ch, _ := b.sess.Channel("c2")
	thread := h.m.replyTarget(b, ch, "legacy-message", topic)
	if e := h.m.startAssist(b, pendingStart{botID: "bot1", targetID: "ag1", threadID: thread, topic: topic}); e != nil {
		t.Fatal(e)
	}
}

func TestExpertAndTeamMentionsUseDurableFlowUntilFinalDelivery(t *testing.T) {
	for _, target := range []struct{ handle, id string }{{"리서처", "ag1"}, {"기획팀", "tm1"}} {
		t.Run(target.id, func(t *testing.T) {
			h := setup(t)
			h.m.Tick()
			a := &deliveryAPI{API: h.m.api}
			h.m.api = a
			h.post("u1", "c1", "@"+target.handle+" 오래 걸리는 질문")
			calls := h.f.find("POST", "/v1/crew/runs")
			if len(calls) != 1 || len(h.f.find("POST", "/v1/crew/assists")) != 0 || len(h.f.find("POST", "/v1/crew/meetings")) != 0 {
				t.Fatal("mention did not use common lifecycle", calls)
			}
			run := obj(calls[0].Body["run"])
			if run["crew_id"] != target.id || run["invocation_mode"] != "flow" || len(obj(run["nodes"])) != 0 || len(h.m.st.Jobs) != 0 {
				t.Fatal(run, h.m.st.Jobs)
			}
			if len(h.f.find("GET", "/v1/flow/capabilities/"+target.id)) != 1 {
				t.Fatal("capability preflight missing")
			}
			h.now = h.now.Add(24 * time.Hour)
			h.m.Tick()
			if len(h.sess.sentTo("t1")) != 1 {
				t.Fatal("unfinished call produced a fake result")
			}
			full := strings.Repeat("완전한 결과 ", 800)
			h.f.runs["run1"]["crew_run"] = map[string]any{"status": "succeeded", "summary": full}
			a.record = map[string]any{"id": "delivery-1", "revision": 1, "delivery": map[string]any{"run_id": "run1", "result_revision": 1, "status": "pending", "part": 0, "message_ids": []any{}, "origin": run["origin"]}}
			h.m = NewManager(a, func(string) (Session, error) { return h.sess, nil }, io.Discard)
			h.m.Tick()
			h.m.Tick()
			posted := h.sess.sentTo("t1")
			if !strings.Contains(strings.Join(posted[1:], ""), full) || body(a.record, "delivery")["status"] != "delivered" || len(h.f.find("POST", "/v1/crew/runs")) != 1 {
				t.Fatal("restart lost final result or reran invocation", len(posted))
			}
		})
	}
}

type unsupportedFlowAPI struct{ API }

func (a unsupportedFlowAPI) Get(path string) (map[string]any, error) {
	if strings.HasPrefix(path, "/v1/flow/capabilities/") {
		return map[string]any{"supported": false, "unsupported": []any{"tools"}}, nil
	}
	return a.API.Get(path)
}

func TestDiscordUnsupportedFlowDoesNotFallBackToLegacyAssist(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.m.api = unsupportedFlowAPI{h.m.api}
	h.post("u1", "c1", "@리서처 질문")
	for _, path := range []string{"/v1/crew/runs", "/v1/crew/assists", "/v1/crew/meetings"} {
		if len(h.f.find("POST", path)) != 0 {
			t.Fatal("unsupported field was ignored", path)
		}
	}
	if !strings.Contains(strings.Join(h.sess.sentTo("t1"), ""), "tools") {
		t.Fatal("unsupported setting not explained")
	}
}

func TestFlowBudgetButtonCannotApproveChangedRevision(t *testing.T) {
	h, _, s := waitingFlow(t, "budget")
	h.m.followDeliveries()
	old := s.lastButtons()[0].ID
	h.f.runs["r1"]["revision"] = 5
	obj(body(h.f.runs["r1"], "crew_run")["waiting"])["question"] = "다른 예산 요청"
	s.h.Button(Press{InteractionID: "old", CustomID: old, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
		t.Fatal("old button adopted current revision")
	}
	h.m.followDeliveries()
	fresh := s.lastButtons()[0].ID
	if fresh == old || len(fresh) > 100 {
		t.Fatal("unbound or oversized custom id", fresh)
	}
	s.h.Button(Press{InteractionID: "new", CustomID: fresh, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	s.h.Button(Press{InteractionID: "duplicate", CustomID: fresh, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	calls := h.f.find("POST", "/v1/crew/runs/r1/decision")
	if len(calls) != 1 || num(calls[0].Body["expected_revision"]) != 5 {
		t.Fatal("decision not bound to displayed revision", calls)
	}
}

func TestFlowBudgetButtonRechecksCurrentChannelConfiguration(t *testing.T) {
	h, _, s := waitingFlow(t, "budget")
	h.m.followDeliveries()
	button := s.lastButtons()[0].ID
	h.m.bots["bot1"].cfg.Channels = nil
	s.h.Button(Press{InteractionID: "revoked", CustomID: button, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
		t.Fatal("removed channel could still approve")
	}
}

func TestFlowChildApprovalUsesAppAndChildBudgetUsesParentRevision(t *testing.T) {
	for _, kind := range []string{"approval", "budget"} {
		t.Run(kind, func(t *testing.T) {
			h, a, s := waitingFlow(t, "child")
			w := obj(body(h.f.runs["r1"], "crew_run")["waiting"])
			w["decision_kind"], w["child_run_id"], w["child_kind"], w["child_revision"] = kind, "nested-child", "meeting", 9
			h.m.Tick()
			if len(s.sent) != 1 {
				t.Fatal("child decision was not announced")
			}
			if kind == "approval" {
				if len(s.lastButtons()) != 0 || !strings.Contains(s.sent[0].Out.Content, "Arkism 앱") {
					t.Fatal("general child approval used service controls")
				}
				return
			}
			button := s.lastButtons()[0].ID
			h.m = NewManager(a, func(string) (Session, error) { return s, nil }, io.Discard)
			h.m.Tick()
			if len(s.sent) != 1 {
				t.Fatal("restart repeated child budget notice")
			}
			s.h.Button(Press{InteractionID: "wrong", CustomID: button, ChannelID: "c1", GuildID: "g1", UserID: "u1"})
			if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
				t.Fatal("decision escaped origin")
			}
			s.h.Button(Press{InteractionID: "child", CustomID: button, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
			calls := h.f.find("POST", "/v1/crew/runs/r1/decision")
			if len(calls) != 1 || num(calls[0].Body["expected_revision"]) != 4 || calls[0].Body["node_id"] != "approval" || len(h.f.find("POST", "/v1/crew/meetings/nested-child/decision")) != 0 {
				t.Fatal("child decision bypassed parent binding", calls)
			}
		})
	}
}

func TestFlowRedirectChangesSendNonceWhileUncertainRetryKeepsIt(t *testing.T) {
	h, a, s := readyDelivery(t, "completed report")
	h.m.followDeliveries()
	if len(s.sent) != 1 {
		t.Fatal("initial delivery", s.sent)
	}
	initial := s.sent[0].Out.Nonce
	s.channels["t-new"] = Channel{ID: "t-new", GuildID: "g1", ParentID: "c1", Thread: true}
	d := body(a.record, "delivery")
	obj(d["origin"])["thread_id"] = "t-new"
	d["status"], d["part"], d["message_ids"] = "pending", 0, []any{}
	d["recovery_history"] = []any{map[string]any{"action": "redirect"}}
	h.m.followDeliveries()
	if len(s.sent) != 2 || s.sent[1].Channel != "t-new" || s.sent[1].Out.Nonce == initial {
		t.Fatal("redirect reused the old destination nonce", s.sent)
	}
	redirected := s.sent[1].Out.Nonce
	d["status"], d["part"], d["message_ids"] = "pending", 0, []any{}
	d["recovery_history"] = append(list(d["recovery_history"]), map[string]any{"action": "retry"})
	h.m.followDeliveries()
	if len(s.sent) != 3 || s.sent[2].Out.Nonce != redirected {
		t.Fatal("retry changed the ambiguous send identity", s.sent)
	}
}

func TestFlowLateAccountingDoesNotChangeFinalResultDelivery(t *testing.T) {
	h, a, s := readyDelivery(t, "immutable completed result")
	h.f.runs["r1"]["revision"] = 5
	body(h.f.runs["r1"], "crew_run")["result_revision"] = 4
	h.m.followDeliveries()
	if len(s.sent) != 1 || body(a.record, "delivery")["status"] != "delivered" {
		t.Fatal("late accounting stranded immutable result", s.sent)
	}
	other, _, session := readyDelivery(t, "different result")
	body(other.f.runs["r1"], "crew_run")["result_revision"] = 3
	other.m.followDeliveries()
	if len(session.sent) != 0 {
		t.Fatal("current record revision overrode pinned result revision")
	}
}
