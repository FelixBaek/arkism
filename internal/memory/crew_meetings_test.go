package memory

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const tokHost = "tok-discord-host"

func crewAgent(name, handle string) Crew {
	return Crew{Type: "agent", Name: name, Handle: handle, Role: "코드 리뷰", Instructions: "꼼꼼히 본다", ModelMode: "fixed", Model: "opus 5.5",
		FallbackModel: "sonnet 5", Effort: "medium", OutputFormat: "markdown", FileEdits: "never", Enabled: true,
		Budget: &CrewBudget{MonthlyUSD: 20}, Agents: []string{"claude_code", "discord"}}
}

func crewTeam(handle string, members ...CrewMember) Crew {
	return Crew{Type: "team", Name: "리뷰팀", Handle: handle, Role: "PR을 같이 본다", Members: members, Mode: "round_robin",
		Ending:    &CrewEnding{MaxRounds: 2, TokenCap: 50000, EndOnAllApproved: true},
		Templates: []CrewMeetingTemplate{{Key: "quick", Name: "빠른 검토", Prompt: "짧게", Overrides: &CrewTemplateOverrides{Ending: &CrewEnding{MaxRounds: 1}}}}}
}

type meetingFixture struct {
	*fixture
	a, b, team Record
}

func newMeetingFixture(t *testing.T) meetingFixture {
	t.Helper()
	f := newFixture(t)
	f.auth.actors[tokEngine] = actor("crew-engine", "service", true, "read", "write")
	f.auth.actors[tokHost] = actor("discord-host", "service", true, "read", "write", "handoff")
	a := f.crew(crewAgent("리뷰어", "리뷰어"))
	b := f.crew(crewAgent("작성자", "작성자"))
	team := f.crew(crewTeam("리뷰팀", CrewMember{AgentID: a.ID, Order: 1, Role: "worker"}, CrewMember{AgentID: b.ID, Order: 2, Role: "reviewer"}))
	return meetingFixture{f, a, b, team}
}

func (f meetingFixture) meeting(token string) Record {
	f.t.Helper()
	r, err := f.svc.CreateCrewMeeting(testCtx, token, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.team.ID, Title: "PR 12", Topic: "이 PR 괜찮나?"}})
	if err != nil {
		f.t.Fatal("create meeting", err)
	}
	return r
}

func (f meetingFixture) progress(r Record, lease string, change func(m *CrewMeeting)) (Record, error) {
	m := *r.CrewMeeting
	m.Turns = append([]MeetingTurn{}, m.Turns...)
	m.NotesPending = append([]MeetingNote{}, m.NotesPending...)
	change(&m)
	return f.svc.UpdateCrewMeeting(testCtx, tokEngine, r.ID, CrewMeetingUpdate{Scope: scopeA, ExpectedRevision: r.Revision, LeaseToken: lease, Meeting: m})
}

func turn(seq, round int, kind, text string) MeetingTurn {
	return MeetingTurn{Seq: seq, Round: round, Kind: kind, Speaker: "리뷰어", Text: text, OK: true}
}

const leaseA, leaseB = "lease-aaaaaaaaaaaaaaaa", "lease-bbbbbbbbbbbbbbbb"

func TestAgentsAndTeamsShareTheAtNamespace(t *testing.T) {
	f := newMeetingFixture(t)
	// @ and $ are separate: a workflow may be called 리뷰어 too
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewWorkflow("리뷰 워크플로", "리뷰어")}); err != nil {
		t.Fatal("workflow handle clashed with an agent's", err)
	}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewTeam("리뷰어", CrewMember{AgentID: f.a.ID, Role: "worker"})}); !errors.Is(err, ErrConflict) {
		t.Fatal("a team took an agent's @handle", err)
	}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewTeam("빈팀", CrewMember{AgentID: "nobody", Role: "worker"})}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a team of a missing agent", err)
	}
	res, err := f.svc.ResolveCrew(testCtx, f.human, scopeA, "@리뷰어")
	if err != nil || res.Item.ID != f.a.ID {
		t.Fatal("@ resolve", res.Item.ID, err)
	}
	if res, err = f.svc.ResolveCrew(testCtx, f.human, scopeA, "$리뷰어"); err != nil || res.Item.Crew.Type != "workflow" {
		t.Fatal("$ resolve", err)
	}
	if res, err = f.svc.ResolveCrew(testCtx, f.human, scopeA, "@리뷰팀"); err != nil || res.Item.ID != f.team.ID {
		t.Fatal("team resolve", err)
	}
}

func TestAgentAndTeamShapes(t *testing.T) {
	good := crewAgent("x", "x")
	if !validCrew(good) {
		t.Fatal("good agent refused")
	}
	for i, mutate := range []func(c *Crew){
		func(c *Crew) { c.Instructions = strings.Repeat("가", crewAgentInstructions+1) },
		func(c *Crew) { c.ModelMode = "random" },
		func(c *Crew) { c.ModelMode, c.Model = "auto", "opus 5.5" },
		func(c *Crew) { c.Temperature = ptr(1.5) },
		func(c *Crew) { c.OutputFormat = "yaml" },
		func(c *Crew) { c.FileEdits = "always" },
		func(c *Crew) { c.Budget = &CrewBudget{MonthlyUSD: -1} },
		func(c *Crew) { c.Agents = []string{"slack"} },
		func(c *Crew) { c.Members = []CrewMember{{AgentID: "a", Role: "worker"}} },
		func(c *Crew) { c.Graph = crewGraph() },
	} {
		c := good
		mutate(&c)
		if validCrew(c) {
			t.Fatal("bad agent accepted", i)
		}
	}
	auto := good
	auto.ModelMode, auto.Model = "auto", ""
	if !validCrew(auto) {
		t.Fatal("auto agent refused")
	}
	team := crewTeam("t", CrewMember{AgentID: "a", Role: "worker"}, CrewMember{AgentID: "b", Role: "moderator"})
	if !validCrew(team) {
		t.Fatal("good team refused")
	}
	for i, mutate := range []func(c *Crew){
		func(c *Crew) { c.Members = nil },
		func(c *Crew) { c.Members = append(c.Members, c.Members[0]) },
		func(c *Crew) { c.Members[0].Role = "boss" },
		func(c *Crew) { c.Mode = "chaos" },
		func(c *Crew) { c.Ending = &CrewEnding{MaxRounds: 11} },
		func(c *Crew) { c.Handle = "" },
		func(c *Crew) { c.Instructions = "x" },
		func(c *Crew) { c.Members[0].Role = "moderator" }, // two moderators
		func(c *Crew) { c.Context = []CrewContextRef{{Collection: "nope", ID: "x", Share: "team"}} },
		func(c *Crew) { c.Templates = append(c.Templates, c.Templates[0]) },
	} {
		c := team
		c.Members = append([]CrewMember{}, team.Members...)
		mutate(&c)
		if validCrew(c) {
			t.Fatal("bad team accepted", i)
		}
	}
	picks := crewTeam("t", CrewMember{AgentID: "a", Role: "worker"})
	picks.Mode = "moderator_picks"
	if validCrew(picks) {
		t.Fatal("moderator_picks without a moderator")
	}
}

func TestTemplateBecomesAgentOnSave(t *testing.T) {
	f := newFixture(t)
	tpl := f.crew(crewTemplate("릴리스"))
	c := *tpl.Crew
	c.Type, c.Handle, c.Budget, c.Enabled = "agent", "릴리스", &CrewBudget{MonthlyUSD: 5}, true
	up, err := f.svc.UpdateCrew(testCtx, f.human, tpl.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 1, Crew: &c})
	if err != nil || up.Crew.Type != "agent" || up.Crew.Category != "개발" {
		t.Fatal("template → agent", err)
	}
	back := *up.Crew
	back.Type, back.Handle, back.Budget = "template", "", nil
	if _, err := f.svc.UpdateCrew(testCtx, f.human, tpl.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 2, Crew: &back}); !errors.Is(err, ErrInvalid) {
		t.Fatal("agent went back to template", err)
	}
	// a template may still be a team member until it is saved again
	old := f.crew(crewTemplate("옛 전문가"))
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewTeam("옛팀", CrewMember{AgentID: old.ID, Role: "worker"})}); err != nil {
		t.Fatal("template as member", err)
	}
	st, err := f.svc.AgentsStatus(testCtx, f.human, scopeA)
	if err != nil || st.Items[old.ID] != "idle" || st.Items[tpl.ID] != "idle" {
		t.Fatal("status", st, err)
	}
}

func TestMeetingCreateListAndNotes(t *testing.T) {
	f := newMeetingFixture(t)
	if _, err := f.svc.CreateCrewMeeting(testCtx, f.human, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.a.ID, Title: "x", Topic: "y"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("meeting of an agent", err)
	}
	quick, err := f.svc.CreateCrewMeeting(testCtx, f.human, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.team.ID, TemplateKey: "quick", Title: "x", Topic: "y"}})
	if err != nil || quick.CrewMeeting.MaxRounds != 1 || quick.CrewMeeting.Status != "pending" || quick.CrewMeeting.Trigger != "manual" || quick.CrewMeeting.CostEstimateUSD <= 0 {
		t.Fatal("template meeting", quick.CrewMeeting, err)
	}
	if _, err := f.svc.CreateCrewMeeting(testCtx, f.human, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.team.ID, TemplateKey: "nope", Title: "x", Topic: "y"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown template", err)
	}
	m := f.meeting(f.human)
	if m.CrewMeeting.MaxRounds != 2 || m.CrewMeeting.TokenCap != 50000 {
		t.Fatal("team ending", m.CrewMeeting)
	}
	disc, err := f.svc.CreateCrewMeeting(testCtx, tokHost, CrewMeetingInput{Scope: scopeA, Meeting: CrewMeeting{TeamID: f.team.ID, Title: "디코", Topic: "z",
		Trigger: "discord", Discord: &MeetingDiscord{BotID: "bot1", GuildID: "123456", ChannelID: "234567", ThreadID: "345678", ThreadURL: "https://discord.com/channels/123456/345678"}}})
	if err != nil || disc.CrewMeeting.Discord.ThreadID != "345678" {
		t.Fatal("discord meeting", err)
	}
	n, err := f.svc.NoteCrewMeeting(testCtx, f.human, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 1, Text: "보안도 봐 줘"})
	if err != nil || len(n.CrewMeeting.NotesPending) != 1 || n.CrewMeeting.NotesPending[0].By != "human-1" {
		t.Fatal("note", err)
	}
	if _, err := f.svc.NoteCrewMeeting(testCtx, f.human, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 1, Text: "늦은 메모"}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale note", err)
	}
	if n, err = f.svc.NoteCrewMeeting(testCtx, tokHost, disc.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 1, Text: "철수: 좋아요"}); err != nil || n.CrewMeeting.NotesPending[0].By != "discord" {
		t.Fatal("discord note", err)
	}
	if _, err := f.svc.NoteCrewMeeting(testCtx, f.agent, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 2, Text: "x"}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent wrote a note", err)
	}
	page, err := f.svc.ListCrewMeetings(testCtx, f.human, scopeA, "running", "", 0, true)
	if err != nil || len(page.Items) != 3 || page.Counts["running"] != 3 || page.Counts["ended"] != 0 || page.Items[0].CrewMeeting.TeamSnapshot != nil {
		t.Fatal("list", page.Counts, err)
	}
	if page, _ = f.svc.ListCrewMeetings(testCtx, f.human, scopeB, "all", "", 0, false); len(page.Items) != 0 {
		t.Fatal("another project saw the meetings")
	}
	if _, err := f.svc.GetCrewMeeting(testCtx, f.human, scopeB, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-scope get", err)
	}
}

func TestMeetingProgressLeaseTurnsAndNotes(t *testing.T) {
	f := newMeetingFixture(t)
	m := f.meeting(f.human)
	m, _ = f.svc.NoteCrewMeeting(testCtx, f.human, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 1, Text: "보안도"})
	if _, err := f.progress(m, leaseA, func(x *CrewMeeting) { x.Status = "running" }); err != nil {
		t.Fatal("lease", err)
	}
	m, _ = f.svc.GetCrewMeeting(testCtx, f.human, scopeA, m.ID)
	if m.CrewMeeting.LeaseUntil == nil {
		t.Fatal("no lease_until")
	}
	if _, err := f.progress(m, leaseB, func(x *CrewMeeting) {}); !errors.Is(err, ErrConflict) {
		t.Fatal("another engine wrote over a live lease", err)
	}
	if _, err := f.svc.UpdateCrewMeeting(testCtx, f.human, m.ID, CrewMeetingUpdate{Scope: scopeA, ExpectedRevision: m.Revision, LeaseToken: leaseA, Meeting: *m.CrewMeeting}); !errors.Is(err, ErrDenied) {
		t.Fatal("a person wrote progress", err)
	}
	// brief + the note consumed into a human turn
	m, err := f.progress(m, leaseA, func(x *CrewMeeting) {
		x.Turns = append(x.Turns, turn(1, 0, "brief", "요약"), MeetingTurn{Seq: 2, Round: 1, Kind: "human", Speaker: "human-1", Text: "보안도", OK: true})
		x.NotesPending = nil
	})
	if err != nil {
		t.Fatal("note consumed", err)
	}
	for i, bad := range []func(x *CrewMeeting){
		func(x *CrewMeeting) { x.Turns = x.Turns[:1] },                              // deletes a turn
		func(x *CrewMeeting) { x.Turns[0].Text = "고친 요약" },                          // rewrites one
		func(x *CrewMeeting) { x.Turns = append(x.Turns, turn(4, 1, "turn", "x")) }, // skips a seq
		func(x *CrewMeeting) {
			x.Turns = append(x.Turns, MeetingTurn{Seq: 3, Round: 1, Kind: "human", Speaker: "human-1", Text: "지어낸 말", OK: true})
		},
		func(x *CrewMeeting) {
			x.NotesPending = []MeetingNote{{By: "human-1", Text: "지어낸 메모", At: time.Now()}}
		},
		func(x *CrewMeeting) { x.Topic = "다른 주제" },
		func(x *CrewMeeting) { x.Status = "pending" },
		func(x *CrewMeeting) { x.Status = "stopped" },
		func(x *CrewMeeting) { x.Status = "concluded" }, // no ended_reason
	} {
		if _, err := f.progress(m, leaseA, bad); err == nil {
			t.Fatal("bad progress accepted", i)
		}
	}
	// a note added while the engine works must not vanish
	m, _ = f.svc.NoteCrewMeeting(testCtx, f.human, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: m.Revision, Text: "하나 더"})
	if _, err := f.progress(m, leaseA, func(x *CrewMeeting) { x.NotesPending = nil }); err == nil {
		t.Fatal("engine dropped a note")
	}
	// lease expiry lets another engine take over
	f.expireLease(m.ID)
	m, _ = f.svc.GetCrewMeeting(testCtx, f.human, scopeA, m.ID)
	if m, err = f.progress(m, leaseB, func(x *CrewMeeting) { x.Turns = append(x.Turns, turn(3, 1, "turn", "의견")) }); err != nil {
		t.Fatal("takeover after expiry", err)
	}
	done, err := f.progress(m, leaseB, func(x *CrewMeeting) {
		x.Status, x.EndedReason = "concluded", "max_rounds"
		x.Conclusion = &MeetingConclusion{Summary: "괜찮다", Decisions: []string{"머지"}, Todos: []string{}, OpenQuestions: []string{}}
		x.Saved = &MeetingSaved{CaptureEventID: m.ID, CandidateIDs: []string{"c1"}}
	})
	if err != nil || done.CrewMeeting.LeaseUntil != nil {
		t.Fatal("conclude", err)
	}
	if _, err := f.progress(done, leaseB, func(x *CrewMeeting) {}); !errors.Is(err, ErrConflict) {
		t.Fatal("a finished meeting changed", err)
	}
	if _, err := f.svc.StopCrewMeeting(testCtx, f.human, done.ID, CrewMeetingStopInput{Scope: scopeA}); !errors.Is(err, ErrConflict) {
		t.Fatal("stopped a finished meeting", err)
	}
}

func (f meetingFixture) expireLease(id string) {
	f.t.Helper()
	r, err := f.repo.Get(testCtx, colCrewMeetings, scopeA, id)
	if err != nil {
		f.t.Fatal(err)
	}
	prev := r.Revision
	r.LeaseUntil = time.Now().Add(-time.Second)
	r.Revision++
	if err := f.repo.Replace(testCtx, colCrewMeetings, r, prev); err != nil {
		f.t.Fatal(err)
	}
}

func TestMeetingStopAndBudgetDecision(t *testing.T) {
	f := newMeetingFixture(t)
	pending := f.meeting(f.human)
	st, err := f.svc.StopCrewMeeting(testCtx, f.human, pending.ID, CrewMeetingStopInput{Scope: scopeA})
	if err != nil || st.CrewMeeting.Status != "stopped" || st.CrewMeeting.EndedReason != "human_stop" {
		t.Fatal("stop pending", err)
	}
	m := f.meeting(f.human)
	m, _ = f.progress(m, leaseA, func(x *CrewMeeting) { x.Status = "running" })
	if m, err = f.svc.StopCrewMeeting(testCtx, tokHost, m.ID, CrewMeetingStopInput{Scope: scopeA}); err != nil || m.CrewMeeting.Status != "stopping" {
		t.Fatal("stop running", err)
	}
	if _, err := f.progress(m, leaseA, func(x *CrewMeeting) { x.Status = "running" }); !errors.Is(err, ErrConflict) {
		t.Fatal("engine undid a stop", err)
	}
	if m, err = f.progress(m, leaseA, func(x *CrewMeeting) { x.Status, x.EndedReason = "stopped", "human_stop" }); err != nil {
		t.Fatal("engine closed the stop", err)
	}

	b := f.meeting(f.human)
	b, _ = f.progress(b, leaseA, func(x *CrewMeeting) { x.Status = "running" })
	wait := func(x *CrewMeeting) {
		x.Status, x.Waiting = "waiting", &MeetingWaiting{Kind: "budget", Reason: "리뷰어 월 예산", At: time.Now().UTC()}
	}
	if b, err = f.progress(b, leaseA, wait); err != nil {
		t.Fatal("budget wait", err)
	}
	if _, err := f.progress(b, leaseA, func(x *CrewMeeting) { x.Status, x.Waiting = "running", nil }); !errors.Is(err, ErrConflict) {
		t.Fatal("engine left a budget wait by itself", err)
	}
	if _, err := f.svc.DecideCrewMeeting(testCtx, f.agent, b.ID, CrewMeetingDecisionInput{Scope: scopeA, ExpectedRevision: b.Revision, Approve: true}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent approved spending", err)
	}
	ok, err := f.svc.DecideCrewMeeting(testCtx, f.human, b.ID, CrewMeetingDecisionInput{Scope: scopeA, ExpectedRevision: b.Revision, Approve: true})
	if err != nil || ok.CrewMeeting.Status != "running" || ok.CrewMeeting.BudgetApproved == nil || ok.CrewMeeting.Waiting != nil {
		t.Fatal("approve", err)
	}
	// approval cleared the lease: any engine may go on, and it may not ask again
	if _, err := f.progress(ok, leaseB, wait); !errors.Is(err, ErrInvalid) {
		t.Fatal("asked again after approval", err)
	}
	c := f.meeting(f.human)
	c, _ = f.progress(c, leaseA, func(x *CrewMeeting) { x.Status = "running" })
	c, _ = f.progress(c, leaseA, wait)
	no, err := f.svc.DecideCrewMeeting(testCtx, f.human, c.ID, CrewMeetingDecisionInput{Scope: scopeA, ExpectedRevision: c.Revision, Approve: false})
	if err != nil || no.CrewMeeting.Status != "stopped" || no.CrewMeeting.EndedReason != "budget" {
		t.Fatal("reject", err)
	}
}

func TestMeetingHTTPRoutes(t *testing.T) {
	f := newMeetingFixture(t)
	h := Handler(f.svc)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	q := "?workspace_id=ws-a&project_id=proj-a"
	scope := `"scope":{"workspace_id":"ws-a","project_id":"proj-a"}`
	w := call("POST", "/v1/crew/meetings", f.human, `{`+scope+`,"meeting":{"team_id":"`+f.team.ID+`","title":"t","topic":"주제"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"crew_meeting"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, p := range []string{"/v1/crew/meetings" + q + "&status=running", "/v1/crew/agents/status" + q, "/v1/crew/usage" + q + "&range=7d&kind=team",
		"/v1/crew/teams/" + f.team.ID + "/stats" + q + "&days=30"} {
		if w := call("GET", p, f.human, ""); w.Code != 200 {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/v1/crew/teams/estimate", f.human, `{`+scope+`,"team":{"type":"team","members":[{"agent_id":"`+f.a.ID+`","role":"worker"}],"mode":"round_robin","ending":{"max_rounds":3}}}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"minutes_max"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/v1/crew/usage"+q+"&range=1y", f.human, ""); w.Code != 400 {
		t.Fatal("bad range", w.Code)
	}
}

func TestEstimateGrowsWithRoundsAndModels(t *testing.T) {
	f := newMeetingFixture(t)
	crew, _ := f.svc.liveCrew(testCtx, scopeA)
	small := estimateTeam(*f.team.Crew, crew)
	big := *f.team.Crew
	big.Ending = &CrewEnding{MaxRounds: 6}
	more := estimateTeam(big, crew)
	if small.Tokens <= 0 || small.CostUSD <= 0 || more.Tokens <= small.Tokens || more.CostUSD <= small.CostUSD || more.MinutesMax < more.MinutesMin {
		t.Fatal(small, more)
	}
}

func TestRoutingCallOriginAndCrewUsage(t *testing.T) {
	f := newMeetingFixture(t)
	runner := "tok-runner"
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "write")
	call := func(c RoutingCall) {
		t.Helper()
		if _, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: c}); err != nil {
			t.Fatal(err)
		}
	}
	m := f.meeting(f.human)
	call(RoutingCall{Model: "anthropic/claude-opus-5-5", Decision: "fixed", OK: true, InputTokens: 1_000_000, OutputTokens: 0,
		Origin: &RoutingOrigin{Kind: "meeting", AgentID: f.a.ID, TeamID: f.team.ID, MeetingID: m.ID, Trigger: "manual"}})
	call(RoutingCall{Model: "openai/gpt-6.1-sol", Decision: "fixed", OK: true, InputTokens: 1000, OutputTokens: 1000,
		Origin: &RoutingOrigin{Kind: "assist", AgentID: f.b.ID, Trigger: "discord"}})
	if _, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "a/b", Decision: "fixed",
		Origin: &RoutingOrigin{Kind: "robot"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad origin", err)
	}
	u, err := f.svc.CrewUsage(testCtx, f.human, scopeA, "today", "all", "", "")
	if err != nil || u.KPI.CostUSD < 4 || !u.KPI.CostEstimated || len(u.Agents) != 2 || u.Agents[0].ID != f.a.ID || u.Agents[0].Pct == nil || *u.Agents[0].Pct != 20 {
		t.Fatal("usage", fmt.Sprintf("%+v", u.KPI), u.Agents, err)
	}
	// the assist call is a direct agent call; the meeting call is not
	if u.Agents[0].Direct != 0 || u.Agents[1].Direct != 1 || u.Agents[1].Failed != 0 || len(u.ByDay) != 1 || u.ByDay[0].Agent != 1 || u.ByDay[0].AgentFailed != 0 {
		t.Fatal("direct calls", u.Agents, u.ByDay)
	}
	if u, _ = f.svc.CrewUsage(testCtx, f.human, scopeA, "30d", "agent", f.b.ID, ""); len(u.Agents) != 1 || u.Agents[0].Calls != 1 {
		t.Fatal("agent filter", u.Agents)
	}
	if u, _ = f.svc.CrewUsage(testCtx, f.human, scopeA, "30d", "team", "", ""); len(u.Teams) != 1 || u.Teams[0].Meetings != 1 || u.KPI.Runs != 1 {
		t.Fatal("teams", u.Teams)
	}
	page, err := f.svc.Discord(testCtx, f.human, scopeA)
	if err != nil || page.Stats.CallsWeek != 1 || page.Stats.TodayUSD <= 0 || len(page.Stats.ByTarget) != 1 || page.Stats.ByTarget[0].Kind != "agent" {
		t.Fatal("discord stats", page.Stats, err)
	}
	// working while its team meets
	r, _ := f.progress(m, leaseA, func(x *CrewMeeting) { x.Status, x.Speaking = "running", f.a.ID })
	st, _ := f.svc.AgentsStatus(testCtx, f.human, scopeA)
	if st.Items[f.a.ID] != "working" || st.Items[f.b.ID] != "working" {
		t.Fatal(st)
	}
	_, _ = f.progress(r, leaseA, func(x *CrewMeeting) {
		x.Status, x.EndedReason, x.TokensUsed, x.CostUSD = "no_conclusion", "max_rounds", 1234, 0.5
	})
	ts, err := f.svc.TeamStats(testCtx, f.human, scopeA, f.team.ID, 30)
	if err != nil || ts.MeetingsDone != 1 || ts.Tokens != 1234 || ts.CostUSD != 0.5 {
		t.Fatal(ts, err)
	}
	off := *f.a.Crew
	off.Enabled = false
	_, _ = f.svc.UpdateCrew(testCtx, f.human, f.a.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 1, Crew: &off})
	if st, _ = f.svc.AgentsStatus(testCtx, f.human, scopeA); st.Items[f.a.ID] != "off" {
		t.Fatal(st)
	}
}

func TestCrewRunBudgetWaitAndDiscordOrigin(t *testing.T) {
	f, w := crewRunFixture(t)
	f.auth.actors[tokHost] = actor("discord-host", "service", true, "read", "write", "handoff")
	run := newCrewRun(w.ID)
	run.Trigger, run.Origin = "discord", &CrewRunOrigin{BotID: "bot1", ThreadID: "345678"}
	r, err := f.svc.CreateCrewRun(testCtx, tokHost, CrewRunInput{Scope: scopeA, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	bad := newCrewRun(w.ID)
	bad.Origin = &CrewRunOrigin{BotID: "bot1"}
	if _, err := f.svc.CreateCrewRun(testCtx, tokHost, CrewRunInput{Scope: scopeA, Run: bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal("origin on a schedule run", err)
	}
	st := *r.CrewRun
	st.Status = "waiting"
	st.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "waiting"}, "ok": {Status: "pending"}, "end": {Status: "pending"}}
	st.Waiting = &CrewRunWaiting{NodeID: "a", Kind: "budget", Question: "오늘 Discord 예산을 넘어요"}
	r, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: st})
	if err != nil {
		t.Fatal("budget wait", err)
	}
	if _, err := f.svc.DecideCrewRun(testCtx, tokHost, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: r.Revision, NodeID: "a", Approve: true}); err != nil {
		t.Fatal("discord host answered a budget wait", err)
	}
}

func TestSystemCallsStayOffTheRoutingScreens(t *testing.T) {
	f := newMeetingFixture(t)
	runner := "tok-runner"
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "write")
	for _, o := range []*RoutingOrigin{{Kind: "system"}, nil} {
		if _, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "anthropic/claude-opus-5-5", Decision: "fixed", OK: true, Origin: o}}); err != nil {
			t.Fatal(err)
		}
	}
	ov, err := f.svc.RoutingOverview(testCtx, f.human, scopeA, "24h")
	if err != nil {
		t.Fatal(err)
	}
	models, _ := ov["models"].([]map[string]any)
	if len(models) != 1 || models[0]["requests"] != 1 {
		t.Fatal("overview counted the system call", ov["models"])
	}
	req, err := f.svc.usageRequests(testCtx, scopeA, 7, time.Now())
	if err != nil || len(req["items"].([]map[string]any)) != 1 {
		t.Fatal("usage requests counted the system call", req, err)
	}
}
