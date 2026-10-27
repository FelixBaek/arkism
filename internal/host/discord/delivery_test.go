package discord

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/bwmarrin/discordgo"
)

// deliveryAPI models memoryd's revision/token contract, including a committed
// ack whose HTTP response disappears. It deliberately has no local job state.
type deliveryAPI struct {
	API
	record        map[string]any
	token         string
	requests      map[string]map[string]any
	requestBodies map[string]string
	actions       []string
	loseAck       bool
	stale         bool
	claimCount    int
	failClaimAt   int
}

func copyMap(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (a *deliveryAPI) Get(path string) (map[string]any, error) {
	if strings.HasPrefix(path, "/v1/crew/deliveries?") {
		u, _ := url.Parse(path)
		wanted := u.Query().Get("status")
		items := []any{}
		if a.record != nil {
			s := str(body(a.record, "delivery")["status"])
			if wanted != "" && s == wanted || wanted == "" && (s == "pending" || s == "sending" || s == "retry_wait") {
				items = append(items, copyMap(a.record))
			}
		}
		return map[string]any{"items": items}, nil
	}
	return a.API.Get(path)
}

func (a *deliveryAPI) Post(path string, payload any) (map[string]any, error) {
	p := obj(payload)
	if path == "/v1/crew/runs" && str(p["request_id"]) != "" {
		if a.requests == nil {
			a.requests = map[string]map[string]any{}
			a.requestBodies = map[string]string{}
		}
		key := str(p["request_id"])
		raw, _ := json.Marshal(p)
		if old := a.requests[key]; old != nil {
			if a.requestBodies[key] != string(raw) {
				return nil, errors.New("request conflict")
			}
			return copyMap(old), nil
		}
		out, err := a.API.Post(path, payload)
		if err == nil {
			a.requests[key] = copyMap(out)
			a.requestBodies[key] = string(raw)
		}
		return out, err
	}
	if !strings.HasPrefix(path, "/v1/crew/deliveries/") {
		return a.API.Post(path, payload)
	}
	d := body(a.record, "delivery")
	action, token := str(p["action"]), str(p["lease_token"])
	a.actions = append(a.actions, action)
	if action == "ack_part" && token == a.token && num(p["part"])+1 == num(d["part"]) {
		ids := list(d["message_ids"])
		if len(ids) > 0 && str(ids[len(ids)-1]) == str(p["message_id"]) {
			return copyMap(a.record), nil
		}
	}
	if num(p["expected_revision"]) != num(a.record["revision"]) {
		return nil, errors.New("revision conflict")
	}
	if action == "claim" {
		if len(str(p["result_hash"])) != 64 {
			return nil, errors.New("missing result hash")
		}
		if hash := str(d["result_hash"]); hash != "" && hash != str(p["result_hash"]) {
			return nil, errors.New("result hash conflict")
		}
		a.claimCount++
		if a.claimCount == a.failClaimAt {
			return nil, errors.New("process interrupted between parts")
		}
		if a.stale {
			d["status"] = "unknown"
			return copyMap(a.record), nil
		}
		if d["status"] != "pending" && d["status"] != "retry_wait" {
			return nil, errors.New("already claimed")
		}
		if len(token) < 16 {
			return nil, errors.New("short token")
		}
		a.token, d["status"] = token, "sending"
		d["result_hash"] = p["result_hash"]
		d["lease_until"] = time.Now().Add(90 * time.Second).Format(time.RFC3339Nano)
	} else {
		if token != a.token || (d["status"] != "sending" && !(action == "complete" && d["status"] == "pending")) {
			return nil, errors.New("lease mismatch")
		}
		switch action {
		case "ack_part":
			if num(p["part"]) != num(d["part"]) {
				return nil, errors.New("part conflict")
			}
			d["part"] = num(d["part"]) + 1
			d["message_ids"] = append(list(d["message_ids"]), str(p["message_id"]))
			d["status"] = "pending"
		case "complete":
			d["status"] = "delivered"
		case "retry":
			d["status"] = "retry_wait"
		case "failed", "unknown":
			d["status"] = action
		default:
			return nil, errors.New("invalid action")
		}
	}
	a.record["revision"] = num(a.record["revision"]) + 1
	if action == "ack_part" && a.loseAck {
		a.loseAck = false
		return nil, errors.New("ack response lost")
	}
	return copyMap(a.record), nil
}

type deliverySession struct {
	*fakeSession
	calls     int
	failAt    int
	failure   error
	accepted  bool
	memberErr error
}

func (s *deliverySession) Send(channel string, out Out) (string, error) {
	s.calls++
	if s.calls == s.failAt {
		if s.accepted {
			_, _ = s.fakeSession.Send(channel, out)
		}
		return "", s.failure
	}
	return s.fakeSession.Send(channel, out)
}
func (s *deliverySession) MemberRoles(_, _ string) ([]string, error) { return nil, s.memberErr }

func readyDelivery(t *testing.T, summary string) (*harness, *deliveryAPI, *deliverySession) {
	t.Helper()
	h := setup(t)
	h.m.Tick()
	h.sess.channels["t-result"] = Channel{ID: "t-result", GuildID: "g1", ParentID: "c1", Thread: true}
	h.f.runs["r1"] = map[string]any{"id": "r1", "revision": 4, "crew_run": map[string]any{"status": "succeeded", "summary": summary}}
	a := &deliveryAPI{API: h.m.api, record: map[string]any{"id": "d1", "revision": 1, "delivery": map[string]any{
		"run_id": "r1", "result_revision": 4, "status": "pending", "part": 0, "message_ids": []any{},
		"origin": map[string]any{"connector": "discord", "bot_id": "bot1", "guild_id": "g1", "channel_id": "c1", "thread_id": "t-result", "user_id": "u1", "event_id": "e1"},
	}}}
	s := &deliverySession{fakeSession: h.sess}
	h.m.api, h.m.bots["bot1"].sess = a, s
	return h, a, s
}

func TestWorkflowMentionHasDurableIdentityAndNoLocalJob(t *testing.T) {
	h := setup(t)
	h.f.crew[2].(map[string]any)["crew"].(map[string]any)["handle"] = "딥리서치"
	h.m.Tick()
	a := &deliveryAPI{API: h.m.api}
	h.m.api = a
	h.post("u1", "c1", "@딥리서치 근거를 찾아줘")
	h.post("u1", "c1", "@딥리서치 근거를 찾아줘")
	calls := h.f.find("POST", "/v1/crew/runs")
	if len(calls) != 1 || h.sess.threads != 1 {
		t.Fatalf("duplicate created runs=%d threads=%d", len(calls), h.sess.threads)
	}
	p := calls[0].Body
	run := obj(p["run"])
	origin := obj(run["origin"])
	if run["invocation_mode"] != "flow" || origin["connector"] != "discord" || origin["user_id"] != "u1" || origin["thread_id"] != "t1" || origin["channel_id"] != "c1" || str(p["request_id"]) == "" {
		t.Fatalf("missing invocation contract: %v", p)
	}
	if len(h.m.st.Jobs) != 0 {
		t.Fatal("durable workflow was duplicated in local jobs")
	}
	// Recreating the manager does not change the same Discord event's identity.
	h.m = NewManager(a, func(string) (Session, error) { return h.sess, nil }, io.Discard)
	h.m.Tick()
	h.post("u1", "c1", "@딥리서치 근거를 찾아줘")
	if len(h.f.find("POST", "/v1/crew/runs")) != 1 {
		t.Fatal("restart reran workflow")
	}
}

func TestWorkflowLookupPreservesExpertsAndHidesOwnedGraph(t *testing.T) {
	h := setup(t)
	obj(obj(h.f.crew[0])["crew"])["workflow"] = "요약"
	h.m.Tick()
	if h.m.findCrew("요약") != nil {
		t.Fatal("expert-owned graph callable directly")
	}
	if str(h.m.findCrew("리서처")["id"]) != "ag1" || str(h.m.findCrew("기획팀")["id"]) != "tm1" {
		t.Fatal("legacy namespace changed")
	}
}

func TestResearchDeepAliasUsesSameWorkflowAndExplicitMode(t *testing.T) {
	h := setup(t)
	crew := obj(obj(h.f.crew[2])["crew"])
	crew["handle"], crew["name"] = "리서치", "리서치"
	crew["research"] = map[string]any{"deep_alias": "딥리서치"}
	h.m.Tick()
	h.post("u1", "c1", "@딥리서치 합성 질문 원문")
	h.post("u1", "c1", "@리서치 보통 조사")
	requests := h.f.find("POST", "/v1/crew/runs")
	if len(requests) != 2 {
		t.Fatalf("expected two runs of one saved workflow: %d", len(requests))
	}
	deep, normal := obj(requests[0].Body["run"]), obj(requests[1].Body["run"])
	if deep["crew_id"] != "wf1" || normal["crew_id"] != "wf1" || deep["research_mode"] != "deep" || str(normal["research_mode"]) != "" {
		t.Fatalf("wrong alias mode mapping: deep=%v normal=%v", deep, normal)
	}
	if !strings.Contains(str(deep["input"]), "합성 질문 원문") || strings.Contains(str(deep["input"]), "@딥리서치") || obj(deep["origin"])["thread_id"] != "t1" {
		t.Fatal("alias changed original input or destination")
	}
	if len(h.f.find("POST", "/v1/crew")) != 0 || len(h.f.crew) != 3 {
		t.Fatal("alias created a second workflow")
	}
}

func TestResearchAliasCannotShadowSavedHandlesOrExposeOwnedGraphs(t *testing.T) {
	for _, kind := range []string{"agent", "team", "workflow", "owned", "ambiguous"} {
		t.Run(kind, func(t *testing.T) {
			h := setup(t)
			c := obj(obj(h.f.crew[2])["crew"])
			c["research"] = map[string]any{"deep_alias": "deep"}
			other := map[string]any{"id": "other", "crew": map[string]any{"type": kind, "handle": "deep", "name": "other"}}
			expected := "other"
			if kind == "owned" {
				obj(other["crew"])["type"] = "workflow"
				obj(obj(h.f.crew[0])["crew"])["workflow"] = "deep"
				expected = ""
			}
			if kind == "ambiguous" {
				obj(other["crew"])["type"] = "workflow"
				obj(other["crew"])["handle"] = "other"
				obj(other["crew"])["research"] = map[string]any{"deep_alias": "deep"}
				expected = ""
			}
			h.f.crew = append(h.f.crew, other)
			h.m.Tick()
			if got := str(h.m.findCrew("deep")["id"]); got != expected {
				t.Fatalf("collision resolved %q want %q", got, expected)
			}
		})
	}
}

func TestWorkflowThreadFailureDoesNotFallBackToChannel(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.sess.failThread = true
	h.post("u1", "c1", "@요약 비공개 맥락")
	if len(h.f.find("POST", "/v1/crew/runs")) != 0 || len(h.sess.sent) != 0 {
		t.Fatal("failed thread widened destination")
	}
}

func TestDeliverySurvivesLostLocalStateAndLongRun(t *testing.T) {
	h, a, s := readyDelivery(t, strings.Repeat("근거 있는 결론. ", 500))
	h.now = h.now.Add(72 * time.Hour)
	h.m.st.Jobs = nil
	h.m.st.Threads = nil
	h.m.followDeliveries()
	if body(a.record, "delivery")["status"] != "delivered" || s.calls < 2 {
		t.Fatalf("delivery=%v calls=%d", a.record, s.calls)
	}
	for _, sent := range s.sent {
		if sent.Channel != "t-result" || len(sent.Out.Nonce) != 24 {
			t.Fatalf("bad destination or nonce %+v", sent)
		}
	}
	count := s.calls
	h.m.followDeliveries()
	if s.calls != count || len(h.f.find("POST", "/v1/crew/runs")) != 0 {
		t.Fatal("completed delivery resent or run restarted")
	}
}

func TestDeliveryRateLimitResumesOnlyRemainingParts(t *testing.T) {
	h, a, s := readyDelivery(t, strings.Repeat("x", 4200))
	s.failAt = 2
	s.failure = &discordgo.RESTError{Response: &http.Response{StatusCode: 429}}
	h.m.followDeliveries()
	if body(a.record, "delivery")["status"] != "retry_wait" || num(body(a.record, "delivery")["part"]) != 1 {
		t.Fatalf("bad partial state %v", a.record)
	}
	first := s.sent[0].Out
	h.m = NewManager(a, func(string) (Session, error) { return s, nil }, io.Discard)
	h.m.Tick()
	if body(a.record, "delivery")["status"] != "delivered" || len(s.sent) != 3 {
		t.Fatalf("restart lost parts %v sends=%d", a.record, len(s.sent))
	}
	if !reflect.DeepEqual(first, s.sent[0].Out) || s.sent[1].Out.Nonce == first.Nonce {
		t.Fatal("replayed first part")
	}
}

func TestDeliveryAmbiguousSendBecomesUnknown(t *testing.T) {
	h, a, s := readyDelivery(t, "answer")
	s.failAt = 1
	s.failure = errors.New("response lost")
	s.accepted = true
	h.m.followDeliveries()
	h.m.followDeliveries()
	if body(a.record, "delivery")["status"] != "unknown" || len(s.sent) != 1 || s.calls != 1 {
		t.Fatalf("uncertain send retried: %v calls=%d", a.record, s.calls)
	}
}

func TestDeliveryRestartBetweenAcknowledgedParts(t *testing.T) {
	h, a, s := readyDelivery(t, strings.Repeat("x", 4200))
	a.failClaimAt = 2
	h.m.followDeliveries()
	if body(a.record, "delivery")["status"] != "pending" || num(body(a.record, "delivery")["part"]) != 1 || len(s.sent) != 1 {
		t.Fatalf("missing durable checkpoint %v", a.record)
	}
	h.m = NewManager(a, func(string) (Session, error) { return s, nil }, io.Discard)
	h.m.Tick()
	if body(a.record, "delivery")["status"] != "delivered" || len(s.sent) != 3 {
		t.Fatalf("restart failed %v sends=%d", a.record, len(s.sent))
	}
}

func TestDeliveryIncludesCompleteResearchAnswerAndSources(t *testing.T) {
	h, a, s := readyDelivery(t, "truncated summary")
	answer := strings.Repeat("검증한 근거. ", 800)
	raw, _ := json.Marshal(map[string]any{"answer": answer, "sources": []map[string]string{{"title": "원문", "url": "https://example.com/source"}}, "limitations": []string{"확인하지 못한 지표"}})
	r := body(h.f.runs["r1"], "crew_run")
	r["definition"] = map[string]any{"graph": map[string]any{"nodes": []any{map[string]any{"id": "end", "type": "end"}}}}
	r["nodes"] = map[string]any{"end": map[string]any{"status": "done", "output": string(raw)}}
	h.m.followDeliveries()
	var text strings.Builder
	for _, sent := range s.sent {
		text.WriteString(sent.Out.Content)
	}
	if body(a.record, "delivery")["status"] != "delivered" || !strings.Contains(text.String(), answer) || !strings.Contains(text.String(), "https://example.com/source") || !strings.Contains(text.String(), "확인하지 못한 지표") {
		t.Fatalf("full research result missing, status=%v size=%d", body(a.record, "delivery")["status"], text.Len())
	}
}

func TestDeliveryLostAckIsIdempotent(t *testing.T) {
	h, a, s := readyDelivery(t, "answer")
	a.loseAck = true
	h.m.followDeliveries()
	if body(a.record, "delivery")["status"] != "delivered" || s.calls != 1 || num(body(a.record, "delivery")["part"]) != 1 {
		t.Fatalf("lost ack duplicated part: %v", a.record)
	}
	if strings.Join(a.actions, ",") != "claim,ack_part,ack_part,complete" {
		t.Fatalf("unexpected acknowledgement recovery %v", a.actions)
	}
}

func TestDeliveryExpiredSendingLeaseDoesNotSend(t *testing.T) {
	h, a, s := readyDelivery(t, "answer")
	a.stale = true
	h.m.followDeliveries()
	if s.calls != 0 || body(a.record, "delivery")["status"] != "unknown" {
		t.Fatal("stale lease sent")
	}
}

func TestDeliveryRevokedRequesterAndTransientMembership(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(itoa(int(boolInt(transient))), func(t *testing.T) {
			h, a, s := readyDelivery(t, "answer")
			want := "failed"
			if transient {
				s.memberErr = errors.New("network unavailable")
				want = "retry_wait"
			} else {
				h.m.bots["bot1"].cfg.AllowUsers = []string{"other"}
			}
			h.m.followDeliveries()
			if s.calls != 0 || body(a.record, "delivery")["status"] != want {
				t.Fatalf("authorization boundary %v calls=%d", a.record, s.calls)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDeliveryChangedResultRevisionDoesNotSend(t *testing.T) {
	h, a, s := readyDelivery(t, "answer")
	body(a.record, "delivery")["result_revision"] = 3
	h.m.followDeliveries()
	if s.calls != 0 || len(a.actions) != 0 {
		t.Fatal("unbound result was sent")
	}
}

func TestDeliveryChangedRenderingCannotResumeOldParts(t *testing.T) {
	h, a, s := readyDelivery(t, strings.Repeat("x", 4200))
	a.failClaimAt = 2
	h.m.followDeliveries()
	body(h.f.runs["r1"], "crew_run")["summary"] = "changed rendering" // Simulate an incompatible formatter upgrade.
	h.m.followDeliveries()
	if len(s.sent) != 1 || num(body(a.record, "delivery")["part"]) != 1 {
		t.Fatal("changed output resumed with old receipt indices")
	}
}

func TestDeliveryUnicodePartsPreserveEmojiWithinLimit(t *testing.T) {
	text := strings.Repeat("👩🏽‍💻한글\n", 1000)
	parts := deliveryParts(text)
	if strings.Join(parts, "") != text {
		t.Fatal("part split lost characters")
	}
	for _, part := range parts {
		if len(utf16.Encode([]rune(part))) > 1900 {
			t.Fatal("supplementary Unicode exceeded message limit")
		}
	}
}

func TestDiscordGeneralApprovalCannotUseServiceCallback(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.sess.channels["t1"] = Channel{ID: "t1", GuildID: "g1", ParentID: "c1", Thread: true}
	h.m.st.Jobs = []job{{Kind: "run", ID: "r1", BotID: "bot1", ChannelID: "t1", Since: h.now}}
	h.f.runs["r1"] = map[string]any{"id": "r1", "revision": 1, "crew_run": map[string]any{"status": "waiting", "waiting": map[string]any{"kind": "approval", "node_id": "n1", "question": "승인?"}}}
	h.m.followJobs()
	if len(h.sess.lastButtons()) != 0 || !strings.Contains(strings.Join(h.sess.sentTo("t1"), ""), "Arkism 앱") {
		t.Fatal("general approval exposed service-token button")
	}
	h.sess.h.Button(Press{InteractionID: "i1", CustomID: "ark:r:r1:n1:y", ChannelID: "t1", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
		t.Fatal("forged general approval reached API")
	}
	obj(h.f.runs["r1"]["crew_run"])["waiting"] = map[string]any{"kind": "budget", "node_id": "n1"}
	h.sess.h.Button(Press{InteractionID: "i2", CustomID: "ark:r:r1:n1:y", ChannelID: "c1", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
		t.Fatal("budget callback escaped original thread")
	}
}

func TestLegacyJobDoesNotExpireWhileRunIsPending(t *testing.T) {
	h := setup(t)
	h.m.Tick()
	h.m.st.Jobs = []job{{Kind: "run", ID: "r1", BotID: "bot1", ChannelID: "c1", Since: h.now.Add(-72 * time.Hour)}}
	h.f.runs["r1"] = map[string]any{"id": "r1", "revision": 1, "crew_run": map[string]any{"status": "running"}}
	h.m.followJobs()
	if len(h.m.st.Jobs) != 1 {
		t.Fatal("long run silently dropped")
	}
}

func waitingFlow(t *testing.T, kind string) (*harness, *deliveryAPI, *deliverySession) {
	t.Helper()
	h, a, s := readyDelivery(t, "")
	body(a.record, "delivery")["status"] = "waiting_run"
	body(a.record, "delivery")["result_revision"] = 0
	h.f.runs["r1"]["crew_run"] = map[string]any{"status": "waiting", "waiting": map[string]any{"kind": kind, "node_id": "approval", "question": "추가 조사를 진행할까요?"}, "nodes": map[string]any{"approval": map[string]any{"status": "waiting", "runs": 1}}}
	return h, a, s
}

func TestFlowApprovalNoticeRecoversAndFinalUsesOnlyOutbox(t *testing.T) {
	h, a, s := waitingFlow(t, "approval")
	// A new manager has no memory of the invocation; server waiting_run data
	// is enough to recover the approval notice in the original conversation.
	h.m = NewManager(a, func(string) (Session, error) { return s, nil }, io.Discard)
	h.m.Tick()
	if len(s.sent) != 1 || s.sent[0].Channel != "t-result" || !strings.Contains(s.sent[0].Out.Content, "FLOW") || !strings.Contains(s.sent[0].Out.Content, "r1") || len(s.sent[0].Out.Buttons) != 0 {
		t.Fatalf("missing approval recovery notice %+v", s.sent)
	}
	if len(a.actions) != 0 {
		t.Fatal("waiting notice claimed terminal delivery")
	}
	h.m = NewManager(a, func(string) (Session, error) { return s, nil }, io.Discard)
	h.m.Tick()
	if len(s.sent) != 1 {
		t.Fatal("persisted notice repeated after restart")
	}
	s.h.Button(Press{InteractionID: "forged", CustomID: "ark:r:r1:approval:y", ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 0 {
		t.Fatal("flow notice allowed service-token general approval")
	}
	h.f.runs["r1"]["crew_run"] = map[string]any{"status": "succeeded", "summary": "승인 후 최종 결과"}
	body(a.record, "delivery")["status"] = "pending"
	body(a.record, "delivery")["result_revision"] = 4
	h.m.Tick()
	h.m.Tick()
	if len(s.sent) != 2 || !strings.Contains(s.sent[1].Out.Content, "승인 후 최종 결과") || body(a.record, "delivery")["status"] != "delivered" {
		t.Fatalf("final answer was lost or duplicated %+v", s.sent)
	}
	for _, job := range h.m.st.Jobs {
		if job.Kind == "flow_wait" {
			t.Fatal("terminal flow retained notice monitor")
		}
	}
}

func TestFlowBudgetNoticeKeepsExistingDecisionPolicy(t *testing.T) {
	h, _, s := waitingFlow(t, "budget")
	h.m.followDeliveries()
	if len(s.sent) != 1 || len(s.sent[0].Out.Buttons) != 2 {
		t.Fatal("budget continuation controls disappeared")
	}
	s.h.Button(Press{InteractionID: "budget", CustomID: s.sent[0].Out.Buttons[0].ID, ChannelID: "t-result", GuildID: "g1", UserID: "u1"})
	if len(h.f.find("POST", "/v1/crew/runs/r1/decision")) != 1 {
		t.Fatal("bound budget continuation was not forwarded")
	}
}

func TestFlowNoticeDoesNotExposeRevokedOrAlreadyDecidedWait(t *testing.T) {
	for _, decided := range []bool{false, true} {
		t.Run(itoa(boolInt(decided)), func(t *testing.T) {
			h, _, s := waitingFlow(t, "approval")
			if decided {
				body(h.f.runs["r1"], "crew_run")["decisions"] = map[string]any{"approval": map[string]any{"approve": true}}
			} else {
				h.m.bots["bot1"].cfg.AllowUsers = []string{"other"}
			}
			h.m.followDeliveries()
			if len(s.sent) != 0 {
				t.Fatal("revoked or already answered wait was announced")
			}
		})
	}
}
