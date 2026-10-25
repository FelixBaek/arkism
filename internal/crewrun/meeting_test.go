package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
