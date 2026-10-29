package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMeetings keeps meetings like the Arkism meetings API: revisions, a
// lease, turns only appended, notes kept until they become human turns,
// a stop the engine cannot undo, and a budget wait only a person ends.
type fakeMeetings struct {
	mu       sync.Mutex
	recs     map[string]MeetingRecord
	lease    map[string]string
	writes   int
	captured [][]CaptureClaim
	spend    map[string]float64
	context  map[string]string
	// onWrite runs after each accepted write (tests add notes or stop there).
	onWrite func(f *fakeMeetings, r MeetingRecord)
}

func cloneRec(r MeetingRecord) MeetingRecord {
	b, _ := json.Marshal(r)
	var c MeetingRecord
	_ = json.Unmarshal(b, &c)
	return c
}

func (f *fakeMeetings) ListMeetings(_ context.Context, status string) ([]MeetingRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []MeetingRecord
	for _, r := range f.recs {
		if !terminalMeeting(r.Meeting.Status) {
			out = append(out, cloneRec(r))
		}
	}
	return out, nil
}

func (f *fakeMeetings) GetMeeting(_ context.Context, id string) (MeetingRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[id]
	if !ok {
		return MeetingRecord{}, &APIError{404, "not_found"}
	}
	return cloneRec(r), nil
}

func noteSet(ns []MeetingNote) map[string]int {
	m := map[string]int{}
	for _, n := range ns {
		m[n.By+"\x00"+n.Text]++
	}
	return m
}

func (f *fakeMeetings) UpdateMeeting(_ context.Context, r MeetingRecord, lease string) (MeetingRecord, error) {
	f.mu.Lock()
	old, ok := f.recs[r.ID]
	if !ok {
		f.mu.Unlock()
		return MeetingRecord{}, &APIError{404, "not_found"}
	}
	fail := func(code string) (MeetingRecord, error) { f.mu.Unlock(); return MeetingRecord{}, &APIError{409, code} }
	o, n := old.Meeting, r.Meeting
	if old.Revision != r.Revision || terminalMeeting(o.Status) {
		return fail("revision_conflict")
	}
	if l := f.lease[r.ID]; l != "" && l != lease {
		return fail("revision_conflict")
	}
	if len(n.Turns) < len(o.Turns) {
		return fail("invalid_request")
	}
	for i := range o.Turns {
		a, _ := json.Marshal(o.Turns[i])
		b, _ := json.Marshal(n.Turns[i])
		if string(a) != string(b) || n.Turns[i].Seq != i+1 {
			return fail("invalid_request")
		}
	}
	consumed := noteSet(o.NotesPending)
	for k, v := range noteSet(n.NotesPending) {
		if consumed[k] < v {
			return fail("invalid_request") // a made-up note
		}
		consumed[k] -= v
	}
	for _, t := range n.Turns[len(o.Turns):] {
		if t.Kind == "human" {
			consumed[t.Speaker+"\x00"+t.Text]--
		}
	}
	for _, v := range consumed {
		if v != 0 {
			return fail("invalid_request") // a vanished note or a made-up human turn
		}
	}
	switch {
	case n.Status == "pending":
		return fail("invalid_request")
	case o.Status == "stopping" && n.Status != "stopping" && n.Status != "stopped" && n.Status != "failed":
		return fail("revision_conflict")
	case o.Status != "stopping" && n.Status == "stopped":
		return fail("revision_conflict")
	case terminalMeeting(n.Status) != (n.EndedReason != ""):
		return fail("invalid_request")
	case n.Status == "waiting" && len(o.BudgetApproved) > 0:
		return fail("invalid_request")
	}
	n.Discord, n.BudgetApproved = o.Discord, o.BudgetApproved
	if terminalMeeting(n.Status) {
		delete(f.lease, r.ID)
	} else {
		f.lease[r.ID] = lease
	}
	c := cloneRec(MeetingRecord{ID: r.ID, Revision: old.Revision + 1, Meeting: n})
	f.recs[r.ID] = c
	f.writes++
	hook := f.onWrite
	f.mu.Unlock()
	if hook != nil {
		hook(f, c)
	}
	return cloneRec(c), nil
}

// person changes a meeting as the API's notes, stop and decision endpoints do.
func (f *fakeMeetings) person(id string, change func(m *Meeting)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.recs[id]
	change(&r.Meeting)
	if terminalMeeting(r.Meeting.Status) || r.Meeting.Status == "running" && r.Meeting.Waiting == nil {
		delete(f.lease, id)
	}
	r.Revision++
	f.recs[id] = cloneRec(r)
}

func (f *fakeMeetings) Capture(_ context.Context, eventID string, claims []CaptureClaim, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = append(f.captured, claims)
	ids := []string{}
	for i := range claims {
		ids = append(ids, fmt.Sprintf("cand-%s-%d", eventID, i))
	}
	return ids, nil
}

func (f *fakeMeetings) AgentSpend(context.Context) (map[string]float64, error) { return f.spend, nil }

func (f *fakeMeetings) ContextText(_ context.Context, collection, id string) (string, error) {
	return f.context[collection+"/"+id], nil
}

// fakeTurns answers meeting calls by who speaks and what is asked.
type fakeTurns struct {
	mu    sync.Mutex
	specs []aiSpec
	// answer by speaker agent id; "" is the default answer
	answer func(s aiSpec) (string, error)
}

func (ft *fakeTurns) call(_ context.Context, s aiSpec) (aiAnswer, error) {
	ft.mu.Lock()
	ft.specs = append(ft.specs, s)
	ft.mu.Unlock()
	out, err := ft.answer(s)
	if err != nil {
		return aiAnswer{}, err
	}
	cost := 0.01
	return aiAnswer{OK: true, Output: out, Model: s.Model, Usage: &aiUsage{InputTokens: 100, OutputTokens: 50, CostUSD: &cost, Estimated: true}}, nil
}

func defaultAnswer(s aiSpec) (string, error) {
	switch {
	case strings.Contains(s.Task, "JSON 하나로만"):
		return `정리: {"summary":"머지한다","decisions":["PR 12를 머지한다"],"todos":["테스트 추가"],"open_questions":[]}`, nil
	case strings.Contains(s.Task, "짧은 브리핑을 써"):
		return "배경: PR 12", nil
	case strings.Contains(s.Task, "STANCE"):
		return "괜찮아 보여요\nSTANCE: approve", nil
	}
	return "의견: " + s.Origin["agent_id"], nil
}

func agentItem(id, name string, extra func(c *Crew)) CrewItem {
	c := Crew{Type: "agent", Name: name, Handle: name, Model: "opus 5.5", Instructions: name + "의 지시"}
	if extra != nil {
		extra(&c)
	}
	return CrewItem{ID: id, Revision: 1, Crew: c}
}

func teamItem(mode string, ending *TeamEnding, members ...TeamMember) CrewItem {
	return CrewItem{ID: "team", Revision: 3, Crew: Crew{Type: "team", Name: "리뷰팀", Handle: "리뷰팀", Role: "PR을 본다", Mode: mode, Ending: ending, Members: members,
		Context: []TeamContext{{Collection: "knowledge_documents", ID: "k1", Title: "규칙", Share: "team"}, {Collection: "memories", ID: "m1", Share: "shared"}}}}
}

type meetingRig struct {
	e     *Engine
	store *fakeMeetings
	ai    *fakeTurns
}

func newMeetingRig(t *testing.T, items ...CrewItem) meetingRig {
	t.Helper()
	e := testEngine(t, newFakeStore(items...))
	ms := &fakeMeetings{recs: map[string]MeetingRecord{}, lease: map[string]string{}, spend: map[string]float64{},
		context: map[string]string{"knowledge_documents/k1": "테스트 없는 PR은 받지 않는다", "memories/m1": "펠릭스는 짧은 답을 좋아한다"}}
	ft := &fakeTurns{answer: defaultAnswer}
	e.Meetings, e.MeetingAI = ms, ft.call
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	return meetingRig{e, ms, ft}
}

func (r meetingRig) add(id string, m Meeting) {
	if m.Status == "" {
		m.Status = "pending"
	}
	m.TeamID, m.TeamRevision, m.Title = "team", 3, "PR 12"
	if m.Topic == "" {
		m.Topic = "이 PR 괜찮나?"
	}
	if m.Trigger == "" {
		m.Trigger = "manual"
	}
	if m.MaxRounds == 0 {
		m.MaxRounds = 2
	}
	m.Turns, m.NotesPending = []MeetingTurn{}, append([]MeetingNote{}, m.NotesPending...)
	r.store.recs[id] = MeetingRecord{ID: id, Revision: 1, Meeting: m}
}

func (r meetingRig) drive(t *testing.T, id string) Meeting {
	t.Helper()
	if err := r.e.DriveMeeting(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return r.store.recs[id].Meeting
}

func kinds(m Meeting) string {
	var out []string
	for _, t := range m.Turns {
		out = append(out, t.Kind+":"+cmpOr(t.AgentID, t.Speaker))
	}
	return strings.Join(out, " ")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func threeMembers() []TeamMember {
	return []TeamMember{{AgentID: "s", Order: 0, Role: "scribe"}, {AgentID: "w", Order: 1, Role: "worker", Focus: "성능"},
		{AgentID: "r", Order: 2, Role: "reviewer", ModelOverride: "sonnet 5"}}
}

func baseItems(extra ...CrewItem) []CrewItem {
	return append([]CrewItem{agentItem("s", "서기", nil), agentItem("w", "작성자", nil), agentItem("r", "검토자", nil)}, extra...)
}

func TestMeetingRoundRobinConcludesAndCaptures(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 2}, threeMembers()...))...)
	rig.add("m1", Meeting{NotesPending: []MeetingNote{{By: "human-1", Text: "보안도 봐 줘", At: time.Now().UTC()}}})
	m := rig.drive(t, "m1")
	if got := kinds(m); got != "brief:s human:human-1 turn:w turn:r turn:w turn:r conclusion:s" {
		t.Fatal(got)
	}
	if m.Status != "concluded" || m.EndedReason != "max_rounds" || m.Round != 2 || m.Conclusion == nil || m.Conclusion.Decisions[0] != "PR 12를 머지한다" {
		t.Fatalf("%+v", m)
	}
	if m.TeamSnapshot == nil || m.TeamSnapshot.Agents["w"].Name != "작성자" || len(m.NotesPending) != 0 {
		t.Fatal("snapshot or notes", m.TeamSnapshot, m.NotesPending)
	}
	if m.Turns[3].Stance != "approve" || strings.Contains(m.Turns[3].Text, "STANCE") || m.Turns[2].Stance != "" {
		t.Fatal("stance", m.Turns[3], m.Turns[2])
	}
	if m.TokensUsed != 6*150 || m.CostUSD < 0.059 || !m.Turns[2].Estimated || m.Turns[2].InputTokens != 100 {
		t.Fatal("usage", m.TokensUsed, m.CostUSD)
	}
	if m.Saved == nil || m.Saved.CaptureEventID != "m1" || len(m.Saved.CandidateIDs) != 1 || rig.store.captured[0][0].Kind != "decision" {
		t.Fatal("capture", m.Saved, rig.store.captured)
	}
	for _, s := range rig.ai.specs {
		if s.Access != "read" || !s.Once || s.Origin["meeting_id"] != "m1" || s.Origin["kind"] != "meeting" || s.Origin["team_id"] != "team" {
			t.Fatal("spec", s)
		}
	}
	brief, w, r := rig.ai.specs[0], rig.ai.specs[1], rig.ai.specs[2]
	if !strings.Contains(brief.Task, "테스트 없는 PR") || !strings.Contains(brief.Task, "짧은 답") {
		t.Fatal("brief context", brief.Task)
	}
	if !strings.Contains(w.Instructions, "작성자의 지시") || !strings.Contains(w.Instructions, "성능") || !strings.Contains(w.Instructions, "PR을 본다") ||
		!strings.Contains(w.Task, "보안도 봐 줘") || !strings.Contains(w.Task, "배경: PR 12") {
		t.Fatal("turn input", w.Instructions, w.Task)
	}
	if r.Model != "sonnet 5" || w.Model != "opus 5.5" || !strings.Contains(r.Task, "STANCE") {
		t.Fatal("models", r.Model, w.Model)
	}
}

func TestDiscordMeetingsLeavePrivateContextOut(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.add("d", Meeting{Trigger: "discord", MaxRounds: 1})
	rig.drive(t, "d")
	brief := rig.ai.specs[0].Task
	if strings.Contains(brief, "테스트 없는 PR") || strings.Contains(brief, "짧은 답") {
		t.Fatal("discord meeting read team-only or personal context", brief)
	}
	if rig.ai.specs[1].Origin["trigger"] != "discord" {
		t.Fatal(rig.ai.specs[1].Origin)
	}
}

func TestMeetingEndsWhenReviewersApprove(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 5, EndOnAllApproved: true}, threeMembers()...))...)
	rig.add("m", Meeting{MaxRounds: 5})
	m := rig.drive(t, "m")
	if m.EndedReason != "all_approved" || m.Round != 1 || m.Status != "concluded" {
		t.Fatal(m.EndedReason, m.Round, kinds(m))
	}
}

func TestMeetingTokenCap(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 5, TokenCap: 400}, threeMembers()...))...)
	rig.add("m", Meeting{MaxRounds: 5, TokenCap: 400})
	m := rig.drive(t, "m")
	// brief 150 + w 150 + r 150 = 450 ≥ 400
	if m.EndedReason != "token_cap" || kinds(m) != "brief:s turn:w turn:r conclusion:s" {
		t.Fatal(m.EndedReason, kinds(m))
	}
}

func TestModeratorPicksNextAndEnds(t *testing.T) {
	members := []TeamMember{{AgentID: "mod", Order: 0, Role: "moderator"}, {AgentID: "w", Order: 1, Role: "worker"}, {AgentID: "r", Order: 2, Role: "reviewer"}}
	rig := newMeetingRig(t, baseItems(agentItem("mod", "진행자", nil), teamItem("moderator_picks", &TeamEnding{MaxRounds: 3, EndOnModerator: true}, members...))...)
	picks := 0
	rig.ai.answer = func(s aiSpec) (string, error) {
		if strings.Contains(s.Task, "다음에 누가 말할지") {
			picks++
			switch picks {
			case 1:
				return "검토가 먼저\nNEXT: 검토자", nil
			case 2:
				return "**NEXT: 작성자**", nil
			default:
				return "충분해요\nEND", nil
			}
		}
		return defaultAnswer(s)
	}
	rig.add("m", Meeting{MaxRounds: 3})
	m := rig.drive(t, "m")
	if got := kinds(m); got != "brief:mod turn:r turn:w conclusion:mod" || m.EndedReason != "moderator" {
		t.Fatal(got, m.EndedReason)
	}
	// moderator picks are counted, not turns
	if m.TokensUsed != 4*150+3*150 {
		t.Fatal(m.TokensUsed)
	}
	if picked, _ := pickNext("NEXT: 없는이", []string{"a"}); picked != "" {
		t.Fatal(picked)
	}
}

func TestModeratorEndWithoutEndOnModeratorOnlyEndsTheRound(t *testing.T) {
	members := []TeamMember{{AgentID: "mod", Role: "moderator"}, {AgentID: "w", Order: 1, Role: "worker"}}
	rig := newMeetingRig(t, baseItems(agentItem("mod", "진행자", nil), teamItem("moderator_picks", &TeamEnding{MaxRounds: 2}, members...))...)
	rig.ai.answer = func(s aiSpec) (string, error) {
		if strings.Contains(s.Task, "다음에 누가 말할지") {
			return "END", nil
		}
		return defaultAnswer(s)
	}
	rig.add("m", Meeting{MaxRounds: 2})
	m := rig.drive(t, "m")
	if m.EndedReason != "max_rounds" || m.Round != 2 || slices.ContainsFunc(m.Turns, func(t MeetingTurn) bool { return t.Kind == "turn" }) {
		t.Fatal(m.EndedReason, m.Round, kinds(m))
	}
}

func TestPersonStopsARunningMeeting(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 5}, threeMembers()...))...)
	rig.add("m", Meeting{MaxRounds: 5})
	rig.store.onWrite = func(f *fakeMeetings, r MeetingRecord) {
		if n := len(r.Meeting.Turns); n == 2 && r.Meeting.Status == "running" {
			f.person("m", func(m *Meeting) { m.Status = "stopping" })
		}
	}
	m := rig.drive(t, "m")
	if m.Status != "stopped" || m.EndedReason != "human_stop" || len(m.Turns) != 2 || m.Conclusion != nil {
		t.Fatal(m.Status, m.EndedReason, kinds(m))
	}
}

func TestNotesDuringAMeetingJoinTheNextRound(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 2}, threeMembers()...))...)
	rig.add("m", Meeting{})
	added := false
	rig.store.onWrite = func(f *fakeMeetings, r MeetingRecord) {
		if !added && len(r.Meeting.Turns) == 2 {
			added = true // between the engine's reads and writes: its next save conflicts and merges
			f.person("m", func(m *Meeting) {
				m.NotesPending = append(m.NotesPending, MeetingNote{By: "human-1", Text: "캐시도", At: time.Now().UTC()})
			})
		}
	}
	m := rig.drive(t, "m")
	if got := kinds(m); got != "brief:s turn:w turn:r human:human-1 turn:w turn:r conclusion:s" || m.Turns[3].Round != 2 {
		t.Fatal(got)
	}
}

func TestBudgetWaitsThenGoesOnAfterApproval(t *testing.T) {
	items := baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))
	items[1] = agentItem("w", "작성자", func(c *Crew) { c.Budget = &AgentBudget{MonthlyUSD: 5} })
	rig := newMeetingRig(t, items...)
	rig.store.spend = map[string]float64{"w": 5.2}
	rig.add("m", Meeting{MaxRounds: 1})
	m := rig.drive(t, "m")
	if m.Status != "waiting" || m.Waiting == nil || m.Waiting.Kind != "budget" || !strings.Contains(m.Waiting.Reason, "작성자") || kinds(m) != "brief:s" {
		t.Fatal(m.Status, m.Waiting, kinds(m))
	}
	// waiting is left alone by the poller
	_ = rig.e.PollMeetings(context.Background())
	rig.e.Wait()
	if rig.store.recs["m"].Meeting.Status != "waiting" {
		t.Fatal("poller drove a waiting meeting")
	}
	rig.store.person("m", func(m *Meeting) {
		m.Status, m.Waiting, m.BudgetApproved = "running", nil, json.RawMessage(`{"by":"human-1","at":"2026-10-13T00:00:00Z"}`)
	})
	m = rig.drive(t, "m")
	if m.Status != "concluded" || kinds(m) != "brief:s turn:w turn:r conclusion:s" {
		t.Fatal(m.Status, kinds(m))
	}
}

func TestAskOverBudgetAndFallbackModel(t *testing.T) {
	items := baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))
	items[2] = agentItem("r", "검토자", func(c *Crew) { c.Budget = &AgentBudget{AskOverUSD: 0.015}; c.FallbackModel = "haiku 5" })
	rig := newMeetingRig(t, items...)
	rig.ai.answer = func(s aiSpec) (string, error) {
		if s.Model == "sonnet 5" { // r's override fails, its fallback answers
			return "", errors.New("ai_failed: quota")
		}
		return defaultAnswer(s)
	}
	rig.add("m", Meeting{MaxRounds: 1})
	m := rig.drive(t, "m")
	if m.Status != "waiting" || !strings.Contains(m.Waiting.Reason, "확인 기준") {
		t.Fatal(m.Status, m.Waiting)
	}
	rig.store.person("m", func(m *Meeting) {
		m.Status, m.Waiting, m.BudgetApproved = "running", nil, json.RawMessage(`{"by":"h"}`)
	})
	m = rig.drive(t, "m")
	if m.Turns[2].Model != "haiku 5" || !m.Turns[2].OK {
		t.Fatal(m.Turns[2])
	}
}

func TestMeetingFailsAndOtherLeasesAreRespected(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", nil, threeMembers()...))...)
	rig.ai.answer = func(aiSpec) (string, error) { return "", errors.New("ai_failed: no CLI") }
	rig.add("f", Meeting{})
	if m := rig.drive(t, "f"); m.Status != "failed" || m.EndedReason != "error" || !strings.Contains(m.Turns[0].Error, "no CLI") {
		t.Fatal(m.Status, m.Turns)
	}
	// a team that is gone fails the meeting at once
	rig.add("g", Meeting{})
	rig.store.recs["g"] = func() MeetingRecord { r := rig.store.recs["g"]; r.Meeting.TeamID = "gone"; return r }()
	if m := rig.drive(t, "g"); m.Status != "failed" || len(m.Turns) != 0 {
		t.Fatal(m.Status)
	}
	// a running meeting another engine holds is not polled; its lease blocks a write anyway
	rig.ai.answer = defaultAnswer
	until := time.Now().Add(time.Minute)
	rig.add("h", Meeting{Status: "running"})
	r := rig.store.recs["h"]
	r.Meeting.LeaseUntil = &until
	rig.store.recs["h"] = r
	rig.store.lease["h"] = "other-engine-lease"
	before := rig.store.writes
	_ = rig.e.PollMeetings(context.Background())
	rig.e.Wait()
	if rig.store.writes != before || rig.store.recs["h"].Meeting.Status != "running" {
		t.Fatal("drove a meeting another engine holds")
	}
	// the conclusion without JSON is no_conclusion, and nothing is captured
	rig.ai.answer = func(s aiSpec) (string, error) {
		if strings.Contains(s.Task, "JSON 하나로만") {
			return "결론 없음", nil
		}
		return defaultAnswer(s)
	}
	rig.add("n", Meeting{MaxRounds: 1})
	if m := rig.drive(t, "n"); m.Status != "no_conclusion" || m.Saved != nil {
		t.Fatal(m.Status, m.Saved)
	}
}

func TestPollStartsPendingMeetings(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.add("p", Meeting{MaxRounds: 1})
	if err := rig.e.PollMeetings(context.Background()); err != nil {
		t.Fatal(err)
	}
	rig.e.Wait()
	if rig.store.recs["p"].Meeting.Status != "concluded" {
		t.Fatal(rig.store.recs["p"].Meeting.Status)
	}
}

func TestStanceAndConclusionParsing(t *testing.T) {
	if txt, st := stanceOf("좋아요\n**STANCE: Approve**"); st != "approve" || txt != "좋아요" {
		t.Fatal(txt, st)
	}
	if _, st := stanceOf("STANCE: maybe"); st != "" {
		t.Fatal(st)
	}
	if parseConclusion(`{"summary":" ","decisions":[]}`) != nil {
		t.Fatal("empty summary accepted")
	}
	if c := parseConclusion("```json\n{\"summary\":\"s\",\"decisions\":[\" a \",\"\"]}\n```"); c == nil || len(c.Decisions) != 1 || c.Decisions[0] != "a" || c.Todos == nil {
		t.Fatal(c)
	}
}

func TestRunBudgetWaitResumesOnDecision(t *testing.T) {
	g := &Graph{Nodes: []Node{node("s", "start"), node("c", "code", "lang", "sh", "code", "echo hi"), node("e", "end")},
		Edges: []Edge{{From: "s", To: "c"}, {From: "c", To: "e"}}}
	for _, approve := range []bool{true, false} {
		fs := newFakeStore(wf("w", g))
		e := testEngine(t, fs)
		_ = e.LoadCrew(context.Background())
		fs.runs["r"] = Run{ID: "r", Revision: 1, State: RunState{CrewID: "w", CrewRevision: 1, Trigger: "discord", Status: "waiting",
			Nodes:   map[string]*NodeState{"s": {Status: "done"}, "c": {Status: "waiting"}, "e": {Status: "pending"}},
			Waiting: &Waiting{NodeID: "c", Kind: "budget", Question: "오늘 예산"}}}
		if err := e.PollWaiting(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.Wait()
		if fs.runs["r"].State.Status != "waiting" {
			t.Fatal("resumed without a decision")
		}
		fs.decide(t, "r", "c", approve)
		_ = e.PollWaiting(context.Background())
		e.Wait()
		st := fs.runs["r"].State
		want := map[bool]string{true: "succeeded", false: "stopped"}[approve]
		if st.Status != want {
			t.Fatal(approve, st.Status, st.Summary, st.Nodes["c"])
		}
	}
}

func TestMeetingRefusesAMemberOnAnExternalRunner(t *testing.T) {
	items := baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))
	items[1].Crew.Backend = "mine" // 작성자 works on an agent-runner connection
	rig := newMeetingRig(t, items...)
	rig.add("m", Meeting{MaxRounds: 1})
	m := rig.drive(t, "m")
	for _, s := range rig.ai.specs {
		if strings.Contains(s.Instructions, "작성자의 지시") {
			t.Fatal("the member on a connection was run on Arkism's path", s)
		}
	}
	for _, turn := range m.Turns {
		if turn.Kind == "turn" && turn.AgentID == "w" && (turn.OK || !strings.Contains(turn.Error, "외부 연결")) {
			t.Fatalf("the member's turn should say why it was skipped: %+v", turn)
		}
	}
}

func meetingObservationFixture() (*meetingExec, speaker) {
	team := Crew{Type: "team", Name: "synthetic team"}
	snapshot := &MeetingSnapshot{Team: team, Agents: map[string]Crew{}}
	e := &Engine{Meetings: &Client{Scope: Scope{WorkspaceID: "ws", ProjectID: "proj", UserID: "usr", AgentID: "crew"}}}
	x := &meetingExec{e: e, base: MeetingRecord{ID: "observed-meeting", Revision: 7}, m: Meeting{TeamID: "observed-team", TeamRevision: 3, Topic: "stable synthetic topic", TeamSnapshot: snapshot}, team: team}
	sp := speaker{member: TeamMember{AgentID: "member", Role: "worker"}, agent: Crew{Type: "agent", Model: "synthetic-model"}}
	return x, sp
}

func TestMeetingObservationActualStandaloneContext(t *testing.T) {
	x, sp := meetingObservationFixture()
	seen := false
	x.e.MeetingAI = func(ctx context.Context, s aiSpec) (aiAnswer, error) {
		seen = true
		identity, ok := AttemptContextFrom(ctx)
		if !ok {
			t.Error("actual meeting has no observation context")
		} else {
			r := identity.Run
			if r.RunRef != (ObservationRef{Collection: "crew_meetings", ID: "observed-meeting", Revision: 7}) || r.RootRunCollection == nil || *r.RootRunCollection != "crew_meetings" || r.RootRunID == nil || *r.RootRunID != "observed-meeting" {
				t.Errorf("not actual explicit meeting root: %+v", r)
			}
			if r.Definition.Ref != (ObservationRef{Collection: "crew", ID: "observed-team", Revision: 3}) {
				t.Errorf("not actual team revision: %+v", r.Definition.Ref)
			}
		}
		return aiAnswer{OK: true, Output: "unchanged business answer"}, nil
	}
	ctx := WithAttemptObserver(context.Background(), NewAttemptObserver(nil))
	ans, err := x.call(withCallLabel(ctx, "brief"), sp, "synthetic role", "synthetic task")
	if err != nil || ans.Output != "unchanged business answer" || !seen {
		t.Fatal(ans, err, seen)
	}
}

func TestMeetingObservationOffAndMissingFacts(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		x, sp := meetingObservationFixture()
		x.e.MeetingAI = func(ctx context.Context, _ aiSpec) (aiAnswer, error) {
			if _, ok := AttemptContextFrom(ctx); ok {
				t.Error("observer off added context")
			}
			return aiAnswer{OK: true, Output: "same"}, nil
		}
		a, err := x.call(context.Background(), sp, "role", "task")
		if err != nil || a.Output != "same" {
			t.Fatal(a, err)
		}
	})
	for _, missing := range []string{"meeting_revision", "team_revision", "snapshot", "linked_parent", "root_conflict"} {
		t.Run(missing, func(t *testing.T) {
			x, sp := meetingObservationFixture()
			switch missing {
			case "meeting_revision":
				x.base.Revision = 0
			case "team_revision":
				x.m.TeamRevision = 0
			case "snapshot":
				x.m.TeamSnapshot = nil
			case "linked_parent":
				x.m.ParentID = "actual-unobserved-parent"
			case "root_conflict":
				x.m.RootID = "other-root"
			}
			o := NewAttemptObserver(nil)
			ctx := WithAttemptObserver(context.Background(), o)
			x.e.MeetingAI = func(_ context.Context, _ aiSpec) (aiAnswer, error) {
				return aiAnswer{OK: true, Output: "business success"}, nil
			}
			a, err := x.call(ctx, sp, "role", "task")
			if err != nil || a.Output != "business success" {
				t.Fatal(a, err)
			}
			if !slices.Contains(o.Snapshot().Issues, "identity_unavailable") {
				t.Fatal("missing identity became observed", o.Snapshot())
			}
		})
	}
}

func TestMeetingObservationPreservesTaskAndActualLinkedParent(t *testing.T) {
	x, sp := meetingObservationFixture()
	x.m.ParentID = "run"
	x.m.RootID = "run"
	x.m.ParentNodeID = "team-node"
	ctx := observerTestContext()
	prior, _ := AttemptContextFrom(ctx)
	prior.Run.Scope = ObservationScope{"ws", "proj", "usr", "crew"}
	prior.Task.TaskID = "caller-task"
	prior.Task.TaskVersion = "caller-revision"
	parent := observerTestRecord(t, WithAttemptContext(ctx, prior), "main", "delegation", "failed", 1, 0)
	prior.ParentAttemptID = &parent.Identity.AttemptID
	o := NewAttemptObserver(nil)
	o.Observe(parent)
	ctx = WithAttemptObserver(WithAttemptContext(ctx, prior), o)
	x.e.MeetingAI = func(ctx context.Context, _ aiSpec) (aiAnswer, error) {
		next, ok := AttemptContextFrom(ctx)
		if !ok || next.Task != prior.Task || next.ParentAttemptID == nil || *next.ParentAttemptID != parent.Identity.AttemptID {
			t.Error("caller task/delegation identity replaced", next)
		}
		r := next.Run
		if r.RunRef.Collection != "crew_meetings" || r.ParentRunCollection == nil || *r.ParentRunCollection != "crew_runs" || r.ParentRunID == nil || *r.ParentRunID != "run" || r.RootRunCollection == nil || *r.RootRunCollection != "crew_runs" || r.InvocationMode != "flow" {
			t.Error("linked namespaces lost", r)
		}
		return aiAnswer{OK: true}, nil
	}
	if _, err := x.call(withCallLabel(ctx, "turn1-0"), sp, "role", "task"); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingObservationFailedFallbackSpendAndLabels(t *testing.T) {
	x, sp := meetingObservationFixture()
	sp.agent.Model = "primary"
	sp.agent.FallbackModel = "fallback"
	o := NewAttemptObserver(nil)
	ctx := WithAttemptObserver(context.Background(), o)
	var seen []AttemptContext
	x.e.MeetingAI = func(ctx context.Context, s aiSpec) (aiAnswer, error) {
		a, _ := AttemptContextFrom(ctx)
		seen = append(seen, a)
		u := observerKnownUsage()
		answer := aiAnswer{OK: true, Output: "success", observationUsage: &u}
		if s.Model == "primary" {
			answer.OK = false
			return answer, failf("ai_failed", "synthetic primary failure")
		}
		return answer, nil
	}
	answer, err := x.call(withCallLabel(ctx, "turn2-5"), sp, "role", "task")
	if err != nil || answer.Output != "success" {
		t.Fatal(answer, err)
	}
	snap := o.Snapshot()
	if len(snap.Latest) != 2 || snap.Totals.Cost.KnownSum != .5 || snap.Totals.StatusCounts["failed"] != 1 || snap.Totals.StatusCounts["succeeded"] != 1 {
		t.Fatalf("failed cost lost: %+v", snap)
	}
	if len(seen) != 2 || seen[1].ParentAttemptID == nil || seen[1].FallbackParentCallKey == nil {
		t.Fatalf("fallback lineage absent: %+v", seen)
	}
	var failedID string
	for _, r := range snap.Latest {
		if r.Status == "failed" {
			failedID = r.Identity.AttemptID
		}
		if !strings.Contains(r.Identity.CallKey, "turn2-5") {
			t.Error("caller label lost", r.Identity.CallKey)
		}
	}
	if *seen[1].ParentAttemptID != failedID {
		t.Fatal("fallback parent is not actual failed leaf")
	}
	if len(ObservationEvidenceFromContext(ctx).ParentLinks) != 1 {
		t.Fatal("actual fallback proof missing")
	}
}

func TestMeetingObservationSnapshotUsesActuallyReadRevision(t *testing.T) {
	x, _ := meetingObservationFixture()
	x.m.TeamSnapshot = nil
	x.e.crew = map[string]CrewItem{"observed-team": {ID: "observed-team", Revision: 9, Crew: Crew{Type: "team"}}}
	if err := x.snapshot(); err != nil {
		t.Fatal(err)
	}
	d, ok := x.meetingObservationDefinition()
	if !ok || d.Ref.Revision != 9 || x.m.TeamRevision != 3 {
		t.Fatal("snapshot borrowed stale revision or changed business record", d, x.m.TeamRevision)
	}
}

func TestMeetingObservationFullDriverOnOffAndStageLabels(t *testing.T) {
	var results []Meeting
	var specs [][]aiSpec
	var captured []AttemptObservation
	for _, enabled := range []bool{false, true} {
		members := []TeamMember{{AgentID: "mod", Order: 0, Role: "moderator"}, {AgentID: "w", Order: 1, Role: "worker"}, {AgentID: "r", Order: 2, Role: "reviewer"}}
		rig := newMeetingRig(t, baseItems(agentItem("mod", "진행자", nil), teamItem("moderator_picks", &TeamEnding{MaxRounds: 1}, members...))...)
		rig.e.Now = func() time.Time { return time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC) }
		rig.e.MeetingAI = func(ctx context.Context, s aiSpec) (aiAnswer, error) {
			a, err := rig.ai.call(ctx, s)
			u := observerKnownUsage()
			a.observationUsage = &u
			return a, err
		}
		rig.add("observed-full-meeting", Meeting{MaxRounds: 1})
		ctx := observerTestContext()
		o := NewAttemptObserver(nil)
		if enabled {
			ctx = WithAttemptObserver(ctx, o)
		}
		if err := rig.e.DriveMeeting(ctx, "observed-full-meeting"); err != nil {
			t.Fatal(err)
		}
		results = append(results, cloneMeeting(rig.store.recs["observed-full-meeting"].Meeting))
		specs = append(specs, append([]aiSpec(nil), rig.ai.specs...))
		if enabled {
			captured = o.Snapshot().Latest
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) || !reflect.DeepEqual(specs[0], specs[1]) {
		t.Fatal("observation changed meeting business result or dispatch input")
	}
	if len(captured) != len(specs[1]) || len(captured) < 6 {
		t.Fatal("actual meeting calls missing", len(captured), len(specs[1]))
	}
	for _, label := range []string{"brief", "pick1-", "turn1-", "conclusion"} {
		found := false
		for _, r := range captured {
			if strings.Contains(r.Identity.CallKey, label) {
				found = true
			}
			if r.Identity.Run.RunRef.Collection != "crew_meetings" || r.Identity.Run.Definition.Ref.Revision != 3 || r.Task.TaskID != "task" {
				t.Fatal("meeting identity/task lost", r.Identity)
			}
		}
		if !found {
			t.Fatal("stage not captured", label)
		}
	}
}

func TestMeetingObservationUnknownReadRevisionCannotBorrowPersistedRevision(t *testing.T) {
	x, _ := meetingObservationFixture()
	x.m.TeamSnapshot = nil
	x.e.crew = map[string]CrewItem{"observed-team": {ID: "observed-team", Revision: 0, Crew: Crew{Type: "team"}}}
	if err := x.snapshot(); err != nil {
		t.Fatal(err)
	}
	if _, ok := x.meetingObservationDefinition(); ok {
		t.Fatal("unknown actual read revision borrowed old persisted revision")
	}
}

// contextMeetingStore exercises the real driver with an optional rich reader.
// Its source and policy facts are explicit synthetic local inputs, never APIs.
type contextMeetingStore struct {
	*fakeMeetings
	factsMu     sync.Mutex
	gone        bool
	revision    int64
	richReads   int
	legacyReads int
}

func (f *contextMeetingStore) ContextText(ctx context.Context, collection, id string) (string, error) {
	f.factsMu.Lock()
	f.legacyReads++
	f.factsMu.Unlock()
	return f.fakeMeetings.ContextText(ctx, collection, id)
}
func (f *contextMeetingStore) ContextReceipt(ctx context.Context, ref TeamContext) (ContextReceiptRead, error) {
	f.factsMu.Lock()
	defer f.factsMu.Unlock()
	f.richReads++
	actual, _ := AttemptContextFrom(ctx)
	scope := actual.Run.Scope
	if validateObservation(scope) != nil {
		prior, _ := AttemptContextFrom(observerTestContext())
		scope = prior.Run.Scope
	}
	at := int64(100)
	revision := f.revision
	if revision == 0 {
		revision = 11
	}
	source := SourceObservation{Scope: scope, Collection: ref.Collection, ID: ref.ID, Revision: &revision, ReadAtNS: &at, ObservedAtNS: &at, ProvenanceStatus: "pinned", ReadAuthorization: "confirmed", Sharing: ref.Share, Authority: "data_only_untrusted"}
	text := f.fakeMeetings.context[ref.Collection+"/"+ref.ID]
	digest, _ := ContextTextSHA256(text)
	source.ContentSHA256 = &digest
	key := ContextSourceKey{Scope: scope, Collection: ref.Collection, ID: ref.ID}
	guard, _ := ContextTextSHA256("synthetic-native-premise:" + text)
	policy := ObservationRef{Collection: "crew", ID: "team", Revision: 3}
	facts := ContextUseFacts{Source: key, Ref: ObservationRef{Collection: ref.Collection, ID: ref.ID, Revision: revision}, ContentSHA256: digest, GuardSHA256: guard, ReadState: "confirmed", Lifecycle: "active", Sharing: ref.Share, CheckedAtNS: &at, PolicyScope: scope, PolicyRef: &policy, PolicyReadAtNS: &at, PolicyState: "confirmed"}
	if f.gone {
		source.Revision = nil
		source.ContentSHA256 = nil
		source.ReadAtNS = nil
		source.ProvenanceStatus = "unavailable"
		source.ReadAuthorization = "not_recorded"
		facts.ReadState = "unavailable"
		facts.Lifecycle = "unknown"
		return ContextReceiptRead{Observation: source, Facts: facts}, &APIError{404, "not_found"}
	}
	return ContextReceiptRead{Observation: source, Facts: facts, Text: text}, nil
}
func contextMeetingRig(t *testing.T) (meetingRig, *contextMeetingStore) {
	t.Helper()
	team := teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...)
	team.Crew.Context = []TeamContext{{Collection: "memories", ID: "m1", Share: "team"}}
	rig := newMeetingRig(t, baseItems(team)...)
	store := &contextMeetingStore{fakeMeetings: rig.store, revision: 11}
	rig.e.Meetings = store
	rig.add("context-meeting", Meeting{MaxRounds: 1})
	return rig, store
}
func contextMeetingSession(t *testing.T, mode ContextReceiptMode, sink func(ContextObservation) error) (context.Context, *ContextReceiptSession) {
	t.Helper()
	session, err := NewContextReceiptSession(mode, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAttemptObserver(observerTestContext(), NewAttemptObserver(nil))
	return WithContextReceiptSession(ctx, session), session
}
func TestMeetingContextGateDeletionAfterSavedBrief(t *testing.T) {
	rig, store := contextMeetingRig(t)
	store.onWrite = func(_ *fakeMeetings, rec MeetingRecord) {
		for _, turn := range rec.Meeting.Turns {
			if turn.Kind == "brief" {
				store.factsMu.Lock()
				store.gone = true
				store.factsMu.Unlock()
				return
			}
		}
	}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	if err == nil || len(rig.ai.specs) != 1 || len(rig.store.captured) != 0 {
		t.Fatalf("deleted source reused by downstream driver: calls=%d captures=%d err=%v", len(rig.ai.specs), len(rig.store.captured), err)
	}
	if store.richReads == 0 {
		t.Fatal("optional rich reader never used")
	}
}
func TestMeetingContextGateDeletionDuringModelReturn(t *testing.T) {
	rig, store := contextMeetingRig(t)
	rig.ai.answer = func(s aiSpec) (string, error) {
		store.factsMu.Lock()
		store.gone = true
		store.factsMu.Unlock()
		return defaultAnswer(s)
	}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	if err == nil || len(rig.ai.specs) != 1 || len(rig.store.recs["context-meeting"].Meeting.Turns) != 0 || len(rig.store.captured) != 0 {
		t.Fatal("stale returned result persisted or consumed", err)
	}
}
func TestMeetingContextGateMissingRichReaderBlocksBeforeDispatch(t *testing.T) {
	rig, _ := contextMeetingRig(t)
	rig.e.Meetings = rig.store
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	if err == nil || len(rig.ai.specs) != 0 || len(rig.store.captured) != 0 {
		t.Fatal("missing provenance permitted model dispatch", err)
	}
}
func TestMeetingContextGateDisplayChangeContinuesAndOffNoRichReads(t *testing.T) {
	for _, mode := range []ContextReceiptMode{ContextReceiptOff, ContextReceiptEnforceCurrentUse} {
		t.Run(string(mode), func(t *testing.T) {
			rig, store := contextMeetingRig(t)
			store.onWrite = func(_ *fakeMeetings, rec MeetingRecord) {
				if len(rec.Meeting.Turns) > 0 {
					store.factsMu.Lock()
					store.revision = 12
					store.factsMu.Unlock()
				}
			}
			ctx, _ := contextMeetingSession(t, mode, nil)
			if err := rig.e.DriveMeeting(ctx, "context-meeting"); err != nil {
				t.Fatal(err)
			}
			if len(rig.ai.specs) != 4 || rig.store.recs["context-meeting"].Meeting.Status != "concluded" {
				t.Fatal("irrelevant metadata caused rerun/block")
			}
			if mode == ContextReceiptOff && store.richReads != 0 {
				t.Fatal("off introduced new current reads")
			}
			if mode == ContextReceiptEnforceCurrentUse && store.richReads == 0 {
				t.Fatal("enforce failed to use current metadata")
			}
		})
	}
}
func TestMeetingContextGateObserveSinkFailurePreservesBusiness(t *testing.T) {
	rig, _ := contextMeetingRig(t)
	ctx, session := contextMeetingSession(t, ContextReceiptObserve, func(ContextObservation) error { return errors.New("synthetic sink failure") })
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err != nil {
		t.Fatal(err)
	}
	if len(rig.ai.specs) != 4 || rig.store.recs["context-meeting"].Meeting.Status != "concluded" {
		t.Fatal("observer changed work")
	}
	if session.Snapshot().Complete || !slices.Contains(session.Snapshot().Issues, "sink_error") {
		t.Fatal("failed delivery was hidden")
	}
}

func TestMeetingContextGateKnownEmptySourcesNeedNoRichReader(t *testing.T) {
	team := teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...)
	team.Crew.Context = nil
	rig := newMeetingRig(t, baseItems(team)...)
	rig.add("context-meeting", Meeting{MaxRounds: 1})
	ctx, session := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err != nil {
		t.Fatal("known empty source set treated as missing evidence", err)
	}
	if len(rig.ai.specs) != 4 || !session.Snapshot().Complete {
		t.Fatal("known-empty meeting changed")
	}
}
func TestMeetingContextGateBlocksRevokedFallback(t *testing.T) {
	worker := agentItem("s", "서기", func(c *Crew) { c.FallbackModel = "fallback-model" })
	team := teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...)
	team.Crew.Context = []TeamContext{{Collection: "memories", ID: "m1", Share: "team"}}
	rig := newMeetingRig(t, baseItems(worker, team)...)
	rig.add("context-meeting", Meeting{MaxRounds: 1})
	store := &contextMeetingStore{fakeMeetings: rig.store, revision: 11}
	rig.e.Meetings = store
	rig.ai.answer = func(aiSpec) (string, error) {
		store.factsMu.Lock()
		store.gone = true
		store.factsMu.Unlock()
		return "", errors.New("synthetic failed paid call")
	}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err == nil || len(rig.ai.specs) != 1 {
		t.Fatal("revoked source allowed fallback", err, len(rig.ai.specs))
	}
}
func TestMeetingContextGateResumedBriefCannotAcquireNewLineage(t *testing.T) {
	rig, store := contextMeetingRig(t)
	if err := rig.e.DriveMeeting(context.Background(), "context-meeting"); err != nil {
		t.Fatal(err)
	}
	rig.store.person("context-meeting", func(m *Meeting) {
		m.Status = "running"
		m.EndedReason = ""
		m.Round = 0
		m.Conclusion = nil
		m.Turns = m.Turns[:1]
	})
	rig.ai.specs = nil
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err == nil || len(rig.ai.specs) != 0 || store.richReads != 0 {
		t.Fatal("historical summary rebound to new current reads", err)
	}
}
func TestMeetingContextGateExactTaskHashesAndSavedSummaryCAS(t *testing.T) {
	rig, _ := contextMeetingRig(t)
	snapshots := map[int64]MeetingRecord{}
	rig.store.onWrite = func(_ *fakeMeetings, rec MeetingRecord) { snapshots[rec.Revision] = cloneRec(rec) }
	ctx, session := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err != nil {
		t.Fatal(err)
	}
	inputHashes := map[string]bool{}
	for _, spec := range rig.ai.specs {
		hash, _ := ContextTextSHA256(spec.Task)
		inputHashes[hash] = true
	}
	stages := map[string]int{}
	for _, record := range session.Snapshot().Records {
		if err := ValidateContextObservation(record); err != nil {
			t.Fatal(err)
		}
		stages[record.ContextStage]++
		if record.ContextStage == "briefing" || record.ContextStage == "turn_input" {
			if record.ContextSHA256 == nil || !inputHashes[*record.ContextSHA256] {
				t.Fatal("receipt hashes partial or unsent task")
			}
		}
		if record.ContextStage == "summary_handoff" {
			if record.SummaryRef == nil || record.SummaryRef.Collection != "crew_meetings" || record.SummaryRef.ID != "context-meeting" {
				t.Fatal("summary identity missing")
			}
			snapshot, ok := snapshots[record.SummaryRef.Revision]
			if !ok {
				t.Fatal("summary CAS was not actually returned")
			}
			valid := false
			for _, turn := range snapshot.Meeting.Turns {
				hash, _ := ContextTextSHA256(turn.Text)
				if turn.Kind == "brief" && turn.OK && record.ContextSHA256 != nil && hash == *record.ContextSHA256 {
					valid = true
				}
			}
			if !valid {
				t.Fatal("claimed summary snapshot did not contain exact stored body")
			}
		}
	}
	if stages["source_read"] != 1 || stages["briefing"] != 1 || stages["summary_handoff"] != 1 || stages["turn_input"] != 3 {
		t.Fatal("source/summary/consumption stages conflated", stages)
	}
}

type mergingContextMeetingStore struct {
	*contextMeetingStore
	injected bool
}

func (f *mergingContextMeetingStore) UpdateMeeting(ctx context.Context, record MeetingRecord, lease string) (MeetingRecord, error) {
	if !f.injected && len(record.Meeting.Turns) > 0 && record.Meeting.Turns[len(record.Meeting.Turns)-1].Kind == "turn" {
		f.injected = true
		f.fakeMeetings.person(record.ID, func(m *Meeting) {
			now := time.Now().UTC().Truncate(time.Millisecond)
			m.Turns = append(m.Turns, MeetingTurn{Seq: len(m.Turns) + 1, Round: 1, AgentID: "r", Speaker: "검토자", Kind: "turn", Text: "synthetic untracked derived material", StartedAt: &now, FinishedAt: &now, OK: true})
		})
	}
	return f.fakeMeetings.UpdateMeeting(ctx, record, lease)
}
func TestMeetingContextGateDoesNotProtectUntrackedMergedTurn(t *testing.T) {
	rig, store := contextMeetingRig(t)
	rig.e.Meetings = &mergingContextMeetingStore{contextMeetingStore: store}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	if err == nil || len(rig.ai.specs) != 2 || len(rig.store.captured) != 0 {
		t.Fatalf("untracked concurrent turn became trusted lineage: calls=%d capture=%d err=%v", len(rig.ai.specs), len(rig.store.captured), err)
	}
}

func TestMeetingContextGateRechecksBeforeCandidateCapture(t *testing.T) {
	rig, store := contextMeetingRig(t)
	rig.store.onWrite = func(_ *fakeMeetings, rec MeetingRecord) {
		if len(rec.Meeting.Turns) > 0 && rec.Meeting.Turns[len(rec.Meeting.Turns)-1].Kind == "conclusion" {
			store.factsMu.Lock()
			store.gone = true
			store.factsMu.Unlock()
		}
	}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	if err == nil || len(rig.ai.specs) != 4 || len(rig.store.captured) != 0 {
		t.Fatal("revoked material released to candidate capture", err, len(rig.store.captured))
	}
}

type scrubbingContextMeetingStore struct {
	*contextMeetingStore
	scrubbed bool
}

func (f *scrubbingContextMeetingStore) UpdateMeeting(ctx context.Context, record MeetingRecord, lease string) (MeetingRecord, error) {
	if !f.scrubbed && len(record.Meeting.Turns) > 0 && record.Meeting.Turns[len(record.Meeting.Turns)-1].Kind == "brief" {
		f.scrubbed = true
		return MeetingRecord{}, &APIError{409, "secret_detected"}
	}
	return f.fakeMeetings.UpdateMeeting(ctx, record, lease)
}
func TestMeetingContextGateHashesNativeScrubbedSummary(t *testing.T) {
	rig, store := contextMeetingRig(t)
	rig.e.Meetings = &scrubbingContextMeetingStore{contextMeetingStore: store}
	ctx, session := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	if err := rig.e.DriveMeeting(ctx, "context-meeting"); err != nil {
		t.Fatal("native scrub changed context admission", err)
	}
	stored := rig.store.recs["context-meeting"].Meeting.Turns[0].Text
	if stored != "(비밀값처럼 보이는 내용이 있어 저장하지 않았어요)" {
		t.Fatal("fixture did not scrub")
	}
	hash, _ := ContextTextSHA256(stored)
	found := false
	for _, record := range session.Snapshot().Records {
		if record.ContextStage == "summary_handoff" {
			found = true
			if record.ContextSHA256 == nil || *record.ContextSHA256 != hash {
				t.Fatal("raw answer falsely claimed persisted summary")
			}
		}
	}
	if !found {
		t.Fatal("scrubbed summary receipt missing")
	}
}
func TestMeetingContextGatePreservesNativeFlowControlFailure(t *testing.T) {
	rig, store := contextMeetingRig(t)
	calls := 0
	rig.e.MeetingAI = func(context.Context, aiSpec) (aiAnswer, error) {
		calls++
		store.factsMu.Lock()
		store.gone = true
		store.factsMu.Unlock()
		return aiAnswer{}, failf("flow_budget_limit", "synthetic budget refusal")
	}
	ctx, _ := contextMeetingSession(t, ContextReceiptEnforceCurrentUse, nil)
	err := rig.e.DriveMeeting(ctx, "context-meeting")
	var native *nodeError
	if !errors.As(err, &native) || native.code != "flow_budget_limit" || calls != 1 || store.richReads != 2 {
		t.Fatal("native budget control replaced by a later freshness diagnostic", err, calls, store.richReads)
	}
}
