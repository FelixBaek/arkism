package memory

import (
	"context"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/prices"
)

// 사용량 for workflows, agents and teams. Costs are estimates at public list
// prices (internal/prices), also for subscription CLIs: the screens mark them 추정.
// shortcut: every figure reads the newest 1000 runs, meetings and calls; aggregate in SQL past that.

var crewUsageRanges = map[string]time.Duration{"today": 0, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "all": -1}
var crewUsageKinds = []string{"all", "workflow", "agent", "team"}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.In(usageZone()).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, usageZone())
}

func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// callCost is a call's cost: as reported, else its tokens at list price.
// The bool says whether it is an estimate.
func callCost(c RoutingCall) (float64, bool) {
	if c.CostUSD != nil {
		return *c.CostUSD, c.Estimated
	}
	if cost, ok := prices.Cost(c.Model, c.InputTokens, c.OutputTokens); ok {
		return cost, true
	}
	return 0, c.Estimated
}

type UsageKPI struct {
	Runs            int        `json:"runs"`
	SuccessRate     *float64   `json:"success_rate"`
	CostUSD         float64    `json:"cost_usd"`
	CostEstimated   bool       `json:"cost_estimated"`
	Waiting         int        `json:"waiting"`
	OldestWaitingAt *time.Time `json:"oldest_waiting_at"`
}

type UsageDay struct {
	Date   string `json:"date"`
	OK     int    `json:"ok"`
	Failed int    `json:"failed"`
	// ok runs by kind, plus a person's direct agent calls (Agent ok, AgentFailed failed).
	Workflow    int `json:"workflow"`
	Team        int `json:"team"`
	Agent       int `json:"agent"`
	AgentFailed int `json:"agent_failed"`
	TeamFailed  int `json:"team_failed"` // part of Failed; the rest of Failed is workflow runs
}

type UsageWorkflow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Runs        int      `json:"runs"`
	SuccessRate *float64 `json:"success_rate"`
	CostUSD     float64  `json:"cost_usd"`
	Tokens      int64    `json:"tokens"`
}

type UsageFailingNode struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	Failures   int    `json:"failures"`
}

type UsageAgent struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Calls    int      `json:"calls"`
	Direct   int      `json:"direct"` // calls a person made straight to the agent
	Failed   int      `json:"failed"`
	Tokens   int64    `json:"tokens"`
	CostUSD  float64  `json:"cost_usd"`
	LimitUSD *float64 `json:"limit_usd"`
	Pct      *float64 `json:"pct"`
}

type UsageTeam struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Meetings      int      `json:"meetings"`
	AvgRounds     float64  `json:"avg_rounds"`
	AvgCostUSD    float64  `json:"avg_cost_usd"`
	CostUSD       float64  `json:"cost_usd"`
	ConcludedRate *float64 `json:"concluded_rate"`
}

type CrewUsage struct {
	KPI          UsageKPI           `json:"kpi"`
	ByDay        []UsageDay         `json:"by_day"`
	Workflows    []UsageWorkflow    `json:"workflows"`
	FailingNodes []UsageFailingNode `json:"failing_nodes"`
	Agents       []UsageAgent       `json:"agents"`
	Teams        []UsageTeam        `json:"teams"`
}

func rate(ok, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := math.Round(1000*float64(ok)/float64(total)) / 10
	return &v
}

type crewUsageData struct {
	crew     map[string]Record
	runs     []Record
	meetings []Record
	calls    []Record
}

func (s *Service) crewUsageRows(ctx context.Context, scope Scope) (crewUsageData, error) {
	var u crewUsageData
	var err error
	if u.crew, err = s.liveCrew(ctx, scope); err != nil {
		return u, err
	}
	if u.runs, err = s.Repo.ListRecords(ctx, scope, colCrewRuns, 1000); err != nil {
		return u, err
	}
	if u.meetings, err = s.Repo.ListRecords(ctx, scope, colCrewMeetings, 1000); err != nil {
		return u, err
	}
	u.calls, err = s.Repo.ListRecords(ctx, scope, colRoutingCalls, routingCallsRead)
	return u, err
}

func callKind(o *RoutingOrigin) string {
	switch {
	case o == nil || o.Kind == "system" || o.Kind == "gateway" || o.Kind == "side": // not crew work
		return ""
	case o.Kind == "meeting" || o.TeamID != "" || o.MeetingID != "":
		return "team"
	case o.Kind == "agent" || o.Kind == "assist":
		return "agent"
	}
	return "workflow"
}

// CrewUsage is the 사용량 tab of 작업 에이전트·팀·워크플로우.
func (s *Service) CrewUsage(ctx context.Context, token string, scope Scope, rangeKey, kind, agentID, teamID string) (CrewUsage, error) {
	span, ok := crewUsageRanges[rangeKey]
	if rangeKey == "" {
		span, ok = crewUsageRanges["30d"], true
	}
	if kind == "" {
		kind = "all"
	}
	if !validScope(scope) || !ok || !slices.Contains(crewUsageKinds, kind) || agentID != "" && !validRunLabel(agentID, 256) || teamID != "" && !validRunLabel(teamID, 256) {
		return CrewUsage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return CrewUsage{}, err
	}
	u, err := s.crewUsageRows(ctx, scope)
	if err != nil {
		return CrewUsage{}, err
	}
	now := time.Now()
	var from time.Time
	switch {
	case span == 0:
		from = startOfDay(now)
	case span > 0:
		from = now.Add(-span)
	}
	t := &crewUsageTally{
		from: from, kind: kind, agentID: agentID, teamID: teamID,
		out:  CrewUsage{ByDay: []UsageDay{}, Workflows: []UsageWorkflow{}, FailingNodes: []UsageFailingNode{}, Agents: []UsageAgent{}, Teams: []UsageTeam{}},
		days: map[string]*UsageDay{},
		wf:   map[string]*UsageWorkflow{}, wfOK: map[string]int{}, wfDone: map[string]int{}, failing: map[[2]string]int{},
		tm: map[string]*UsageTeam{}, tmRounds: map[string]int{}, tmCost: map[string]float64{}, tmConcluded: map[string]int{}, tmEnded: map[string]int{},
		ag: map[string]*UsageAgent{},
	}
	t.addRuns(u)
	t.addMeetings(u)
	t.addCalls(u)
	return t.finish(), nil
}

// crewUsageTally accumulates CrewUsage over the runs, meetings and calls of one request.
type crewUsageTally struct {
	from                  time.Time
	kind, agentID, teamID string
	out                   CrewUsage
	days                  map[string]*UsageDay
	okN, doneN            int
	oldest                *time.Time
	wf                    map[string]*UsageWorkflow
	wfOK, wfDone          map[string]int
	failing               map[[2]string]int
	tm                    map[string]*UsageTeam
	tmRounds              map[string]int
	tmCost                map[string]float64
	tmConcluded, tmEnded  map[string]int
	ag                    map[string]*UsageAgent
}

func (t *crewUsageTally) in(at time.Time) bool { return !at.Before(t.from) }

func (t *crewUsageTally) want(k string) bool { return t.kind == "all" || t.kind == k }

func (t *crewUsageTally) day(at time.Time) *UsageDay {
	k := at.In(usageZone()).Format("2006-01-02")
	if t.days[k] == nil {
		t.days[k] = &UsageDay{Date: k}
	}
	return t.days[k]
}

func (t *crewUsageTally) waiting(at time.Time) {
	t.out.KPI.Waiting++
	if t.oldest == nil || at.Before(*t.oldest) {
		w := at
		t.oldest = &w
	}
}

// addRuns counts workflow runs.
func (t *crewUsageTally) addRuns(u crewUsageData) {
	if !t.want("workflow") || t.agentID != "" || t.teamID != "" {
		return
	}
	for _, r := range u.runs {
		run := r.CrewRun
		if run == nil || run.Trigger == "test" { // 시험 실행 fakes AI steps: it would skew runs, rates and tokens per run
			continue
		}
		if run.Status == "waiting" {
			t.waiting(r.UpdatedAt)
		}
		if !t.in(r.CreatedAt) {
			continue
		}
		t.out.KPI.Runs++
		w := t.wf[run.CrewID]
		if w == nil {
			w = &UsageWorkflow{ID: run.CrewID, Name: run.CrewID}
			if it, ok := u.crew[run.CrewID]; ok {
				w.Name = it.Crew.Name
			}
			t.wf[run.CrewID] = w
		}
		w.Runs++
		switch run.Status {
		case "succeeded":
			t.okN, t.doneN = t.okN+1, t.doneN+1
			t.wfOK[run.CrewID]++
			t.wfDone[run.CrewID]++
			t.day(r.UpdatedAt).OK++
			t.day(r.UpdatedAt).Workflow++
		case "failed":
			t.doneN++
			t.wfDone[run.CrewID]++
			t.day(r.UpdatedAt).Failed++
			for id, n := range run.Nodes {
				if n.Status == "failed" {
					t.failing[[2]string{run.CrewID, id}]++
				}
			}
		}
	}
}

// addMeetings counts team meetings.
func (t *crewUsageTally) addMeetings(u crewUsageData) {
	if !t.want("team") || t.agentID != "" {
		return
	}
	for _, r := range u.meetings {
		m := r.CrewMeeting
		if m == nil || t.teamID != "" && m.TeamID != t.teamID {
			continue
		}
		if m.Status == "waiting" && m.Waiting != nil {
			t.waiting(m.Waiting.At)
		}
		if !t.in(r.CreatedAt) {
			continue
		}
		t.out.KPI.Runs++
		team := t.tm[m.TeamID]
		if team == nil {
			team = &UsageTeam{ID: m.TeamID, Name: m.TeamID}
			if it, ok := u.crew[m.TeamID]; ok {
				team.Name = it.Crew.Name
			}
			t.tm[m.TeamID] = team
		}
		team.Meetings++
		t.tmRounds[m.TeamID] += m.Round
		t.tmCost[m.TeamID] += m.CostUSD
		if terminalMeeting(m.Status) {
			t.tmEnded[m.TeamID]++
			t.doneN++
			if m.Status == "concluded" {
				t.okN++
				t.tmConcluded[m.TeamID]++
				t.day(r.UpdatedAt).OK++
				t.day(r.UpdatedAt).Team++
			} else if m.Status == "failed" || m.Status == "no_conclusion" {
				t.day(r.UpdatedAt).Failed++
				t.day(r.UpdatedAt).TeamFailed++
			}
		}
	}
}

// addCalls adds cost for every kind, and fills the agents table.
func (t *crewUsageTally) addCalls(u crewUsageData) {
	for id, it := range u.crew {
		if isAgent(it.Crew) && (t.agentID == "" || id == t.agentID) {
			a := &UsageAgent{ID: id, Name: it.Crew.Name}
			if b := it.Crew.Budget; b != nil && b.MonthlyUSD > 0 {
				limit := b.MonthlyUSD
				a.LimitUSD = &limit
			}
			t.ag[id] = a
		}
	}
	for _, r := range u.calls {
		c := r.RoutingCall
		if c == nil || !t.in(r.CreatedAt) {
			continue
		}
		o := c.Origin
		k := callKind(o)
		if k == "" || !t.want(k) && !(t.kind == "agent" && o.AgentID != "") || t.agentID != "" && o.AgentID != t.agentID || t.teamID != "" && o.TeamID != t.teamID {
			continue
		}
		t.addCall(r, c, o, k)
	}
}

func (t *crewUsageTally) addCall(r Record, c *RoutingCall, o *RoutingOrigin, k string) {
	cost, est := callCost(*c)
	t.out.KPI.CostUSD += cost
	t.out.KPI.CostEstimated = t.out.KPI.CostEstimated || est
	if w := t.wf[o.WorkflowID]; w != nil && o.WorkflowID != "" {
		w.CostUSD += cost
		w.Tokens += c.InputTokens + c.OutputTokens
	}
	if k == "agent" {
		if c.OK {
			t.day(r.CreatedAt).Agent++
		} else {
			t.day(r.CreatedAt).AgentFailed++
		}
	}
	if a := t.ag[o.AgentID]; a != nil {
		a.Calls++
		if !c.OK {
			a.Failed++
		}
		if k == "agent" {
			a.Direct++
		}
		a.Tokens += c.InputTokens + c.OutputTokens
		a.CostUSD += cost
	}
}

// finish rounds the totals and orders every table.
func (t *crewUsageTally) finish() CrewUsage {
	out := t.out
	out.KPI.CostUSD = roundUSD(out.KPI.CostUSD)
	out.KPI.SuccessRate = rate(t.okN, t.doneN)
	out.KPI.OldestWaitingAt = t.oldest
	for _, d := range t.days {
		out.ByDay = append(out.ByDay, *d)
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Date < out.ByDay[j].Date })
	for id, w := range t.wf {
		w.SuccessRate, w.CostUSD = rate(t.wfOK[id], t.wfDone[id]), roundUSD(w.CostUSD)
		out.Workflows = append(out.Workflows, *w)
	}
	sort.Slice(out.Workflows, func(i, j int) bool { return out.Workflows[i].Runs > out.Workflows[j].Runs })
	for k, n := range t.failing {
		out.FailingNodes = append(out.FailingNodes, UsageFailingNode{WorkflowID: k[0], NodeID: k[1], Failures: n})
	}
	sort.Slice(out.FailingNodes, func(i, j int) bool {
		a, b := out.FailingNodes[i], out.FailingNodes[j]
		return a.Failures > b.Failures || a.Failures == b.Failures && a.WorkflowID+a.NodeID < b.WorkflowID+b.NodeID
	})
	out.FailingNodes = out.FailingNodes[:min(len(out.FailingNodes), 10)]
	if t.want("agent") || t.agentID != "" {
		for _, a := range t.ag {
			a.CostUSD = roundUSD(a.CostUSD)
			if a.LimitUSD != nil {
				p := math.Round(1000*a.CostUSD / *a.LimitUSD) / 10
				a.Pct = &p
			}
			out.Agents = append(out.Agents, *a)
		}
		sort.Slice(out.Agents, func(i, j int) bool {
			return out.Agents[i].CostUSD > out.Agents[j].CostUSD || out.Agents[i].CostUSD == out.Agents[j].CostUSD && out.Agents[i].Name < out.Agents[j].Name
		})
	}
	for id, team := range t.tm {
		team.AvgRounds = math.Round(10*float64(t.tmRounds[id])/float64(team.Meetings)) / 10
		team.AvgCostUSD, team.CostUSD = roundUSD(t.tmCost[id]/float64(team.Meetings)), roundUSD(t.tmCost[id])
		team.ConcludedRate = rate(t.tmConcluded[id], t.tmEnded[id])
		out.Teams = append(out.Teams, *team)
	}
	sort.Slice(out.Teams, func(i, j int) bool { return out.Teams[i].Meetings > out.Teams[j].Meetings })
	return out
}

type AgentsStatus struct {
	Items map[string]string `json:"items"`
}

// AgentsStatus: off (switched off), working (in an open meeting) or idle.
func (s *Service) AgentsStatus(ctx context.Context, token string, scope Scope) (AgentsStatus, error) {
	if !validScope(scope) {
		return AgentsStatus{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return AgentsStatus{}, err
	}
	crew, err := s.liveCrew(ctx, scope)
	if err != nil {
		return AgentsStatus{}, err
	}
	meetings, err := s.Repo.ListRecords(ctx, scope, colCrewMeetings, 1000)
	if err != nil {
		return AgentsStatus{}, err
	}
	busy := map[string]bool{}
	for _, r := range meetings {
		m := r.CrewMeeting
		if m == nil || m.Status != "running" && m.Status != "stopping" {
			continue
		}
		busy[m.Speaking] = true
		if t, ok := crew[m.TeamID]; ok {
			for _, mem := range t.Crew.Members {
				busy[mem.AgentID] = true
			}
		}
	}
	out := AgentsStatus{Items: map[string]string{}}
	for id, it := range crew {
		if !isAgent(it.Crew) {
			continue
		}
		switch {
		case it.Crew.Type == "agent" && !it.Crew.Enabled:
			out.Items[id] = "off"
		case busy[id]:
			out.Items[id] = "working"
		default:
			out.Items[id] = "idle"
		}
	}
	return out, nil
}

type TeamStats struct {
	MeetingsDone int     `json:"meetings_done"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

func (s *Service) TeamStats(ctx context.Context, token string, scope Scope, id string, days int) (TeamStats, error) {
	if days == 0 {
		days = 30
	}
	if !validScope(scope) || !validRunLabel(id, 256) || days < 1 || days > 365 {
		return TeamStats{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return TeamStats{}, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colCrewMeetings, 1000)
	if err != nil {
		return TeamStats{}, err
	}
	from := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var out TeamStats
	for _, r := range rows {
		if m := r.CrewMeeting; m != nil && m.TeamID == id && terminalMeeting(m.Status) && !r.UpdatedAt.Before(from) {
			out.MeetingsDone++
			out.Tokens += m.TokensUsed
			out.CostUSD += m.CostUSD
		}
	}
	out.CostUSD = roundUSD(out.CostUSD)
	return out, nil
}

type TeamEstimate struct {
	Tokens     int64   `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	MinutesMin int     `json:"minutes_min"`
	MinutesMax int     `json:"minutes_max"`
}

type TeamEstimateInput struct {
	Scope Scope `json:"scope"`
	Team  Crew  `json:"team"`
}

// estimateModel is what a model costs in the estimate; auto (Clef picks) is priced like a mid model.
func estimateModel(m string) string {
	if m == "" || m == "auto" {
		return "sonnet 5"
	}
	return m
}

// estimateTeam guesses one meeting's tokens, cost and minutes: each turn reads
// the brief and the transcript so far (capped) and writes up to its agent's
// max_output_tokens (600 when unset); moderator_picks adds one short pick a turn.
func estimateTeam(team Crew, crew map[string]Record) TeamEstimate {
	rounds := 3
	if team.Ending != nil {
		rounds = team.Ending.MaxRounds
	}
	type speaker struct {
		model  string
		output int64
	}
	var speakers []speaker
	scribe, moderator := speaker{estimateModel(""), 600}, speaker{}
	for _, m := range team.Members {
		sp := speaker{estimateModel(""), 600}
		if it, ok := crew[m.AgentID]; ok && it.Crew != nil {
			sp.model = estimateModel(it.Crew.Model)
			if it.Crew.ModelMode == "auto" {
				sp.model = estimateModel("auto")
			}
			if n := it.Crew.MaxOutputTokens; n > 0 && n < 2000 {
				sp.output = int64(n)
			}
		}
		if m.ModelOverride != "" {
			sp.model = estimateModel(m.ModelOverride)
		}
		switch m.Role {
		case "scribe":
			scribe = sp
		case "moderator":
			moderator = sp
			speakers = append(speakers, sp)
		default:
			speakers = append(speakers, sp)
		}
	}
	var tokens int64
	var cost float64
	turns := 0
	transcript := int64(800) // brief
	call := func(sp speaker, in, out int64) {
		tokens += in + out
		if c, ok := prices.Cost(sp.model, in, out); ok {
			cost += c
		}
		turns++
	}
	call(scribe, 1500, 800)
	for r := 0; r < rounds; r++ {
		for _, sp := range speakers {
			in := 1200 + min(transcript, 12000)
			call(sp, in, sp.output)
			transcript += sp.output
			if team.Mode == "moderator_picks" && moderator.model != "" {
				call(moderator, 600+min(transcript, 12000), 30)
			}
		}
	}
	call(scribe, 1200+min(transcript, 12000), 900)
	return TeamEstimate{Tokens: tokens, CostUSD: math.Round(cost*1e4) / 1e4, MinutesMin: max(1, int(math.Ceil(float64(turns)*0.25))),
		MinutesMax: max(1, int(math.Ceil(float64(turns)*1.0)))}
}

func (s *Service) EstimateTeam(ctx context.Context, token string, in TeamEstimateInput) (TeamEstimate, error) {
	if !validScope(in.Scope) || in.Team.Type == "" && len(in.Team.Members) == 0 {
		return TeamEstimate{}, ErrInvalid
	}
	if in.Team.Type == "" {
		in.Team.Type = "team"
	}
	if in.Team.Type != "team" || len(in.Team.Members) > 12 || !validCrewEnding(in.Team.Ending) {
		return TeamEstimate{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, in.Scope, "", ""); err != nil {
		return TeamEstimate{}, err
	}
	crew, err := s.liveCrew(ctx, in.Scope)
	if err != nil {
		return TeamEstimate{}, err
	}
	return estimateTeam(in.Team, crew), nil
}

func crewUsageRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/crew/usage", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, e := s.CrewUsage(r.Context(), bearer(r), scopeQuery(r), q.Get("range"), q.Get("kind"), q.Get("agent_id"), q.Get("team_id"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/agents/status", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.AgentsStatus(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/crew/teams/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		days := 0
		if v := r.URL.Query().Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			days = n
		}
		out, e := s.TeamStats(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"), days)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/teams/estimate", func(w http.ResponseWriter, r *http.Request) {
		var in TeamEstimateInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.EstimateTeam(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
