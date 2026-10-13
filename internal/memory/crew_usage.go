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
}

type UsageWorkflow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Runs        int      `json:"runs"`
	SuccessRate *float64 `json:"success_rate"`
	CostUSD     float64  `json:"cost_usd"`
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
	in := func(t time.Time) bool { return !t.Before(from) }
	want := func(k string) bool { return kind == "all" || kind == k }
	out := CrewUsage{ByDay: []UsageDay{}, Workflows: []UsageWorkflow{}, FailingNodes: []UsageFailingNode{}, Agents: []UsageAgent{}, Teams: []UsageTeam{}}
	days := map[string]*UsageDay{}
	day := func(t time.Time) *UsageDay {
		k := t.In(usageZone()).Format("2006-01-02")
		if days[k] == nil {
			days[k] = &UsageDay{Date: k}
		}
		return days[k]
	}
	var okN, doneN int
	var oldest *time.Time
	waiting := func(at time.Time) {
		out.KPI.Waiting++
		if oldest == nil || at.Before(*oldest) {
			t := at
			oldest = &t
		}
	}
	// workflow runs
	wf := map[string]*UsageWorkflow{}
	wfOK := map[string]int{}
	wfDone := map[string]int{}
	failing := map[[2]string]int{}
	if want("workflow") && agentID == "" && teamID == "" {
		for _, r := range u.runs {
			run := r.CrewRun
			if run == nil {
				continue
			}
			if run.Status == "waiting" {
				waiting(r.UpdatedAt)
			}
			if !in(r.CreatedAt) {
				continue
			}
			out.KPI.Runs++
			w := wf[run.CrewID]
			if w == nil {
				w = &UsageWorkflow{ID: run.CrewID, Name: run.CrewID}
				if it, ok := u.crew[run.CrewID]; ok {
					w.Name = it.Crew.Name
				}
				wf[run.CrewID] = w
			}
			w.Runs++
			switch run.Status {
			case "succeeded":
				okN, doneN = okN+1, doneN+1
				wfOK[run.CrewID]++
				wfDone[run.CrewID]++
				day(r.UpdatedAt).OK++
			case "failed":
				doneN++
				wfDone[run.CrewID]++
				day(r.UpdatedAt).Failed++
				for id, n := range run.Nodes {
					if n.Status == "failed" {
						failing[[2]string{run.CrewID, id}]++
					}
				}
			}
		}
	}
	// meetings
	tm := map[string]*UsageTeam{}
	tmRounds, tmCost, tmConcluded, tmEnded := map[string]int{}, map[string]float64{}, map[string]int{}, map[string]int{}
	if want("team") && agentID == "" {
		for _, r := range u.meetings {
			m := r.CrewMeeting
			if m == nil || teamID != "" && m.TeamID != teamID {
				continue
			}
			if m.Status == "waiting" && m.Waiting != nil {
				waiting(m.Waiting.At)
			}
			if !in(r.CreatedAt) {
				continue
			}
			out.KPI.Runs++
			t := tm[m.TeamID]
			if t == nil {
				t = &UsageTeam{ID: m.TeamID, Name: m.TeamID}
				if it, ok := u.crew[m.TeamID]; ok {
					t.Name = it.Crew.Name
				}
				tm[m.TeamID] = t
			}
			t.Meetings++
			tmRounds[m.TeamID] += m.Round
			tmCost[m.TeamID] += m.CostUSD
			if terminalMeeting(m.Status) {
				tmEnded[m.TeamID]++
				doneN++
				if m.Status == "concluded" {
					okN++
					tmConcluded[m.TeamID]++
					day(r.UpdatedAt).OK++
				} else if m.Status == "failed" || m.Status == "no_conclusion" {
					day(r.UpdatedAt).Failed++
				}
			}
		}
	}
	// calls: cost for every kind, and the agents table
	ag := map[string]*UsageAgent{}
	for id, it := range u.crew {
		if isAgent(it.Crew) && (agentID == "" || id == agentID) {
			a := &UsageAgent{ID: id, Name: it.Crew.Name}
			if b := it.Crew.Budget; b != nil && b.MonthlyUSD > 0 {
				limit := b.MonthlyUSD
				a.LimitUSD = &limit
			}
			ag[id] = a
		}
	}
	for _, r := range u.calls {
		c := r.RoutingCall
		if c == nil || !in(r.CreatedAt) {
			continue
		}
		o := c.Origin
		k := callKind(o)
		if k == "" || !want(k) && !(kind == "agent" && o.AgentID != "") || agentID != "" && o.AgentID != agentID || teamID != "" && o.TeamID != teamID {
			continue
		}
		cost, est := callCost(*c)
		out.KPI.CostUSD += cost
		out.KPI.CostEstimated = out.KPI.CostEstimated || est
		if w := wf[o.WorkflowID]; w != nil && o.WorkflowID != "" {
			w.CostUSD += cost
		}
		if a := ag[o.AgentID]; a != nil {
			a.Calls++
			a.Tokens += c.InputTokens + c.OutputTokens
			a.CostUSD += cost
		}
	}
	out.KPI.CostUSD = roundUSD(out.KPI.CostUSD)
	out.KPI.SuccessRate = rate(okN, doneN)
	out.KPI.OldestWaitingAt = oldest
	for _, d := range days {
		out.ByDay = append(out.ByDay, *d)
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Date < out.ByDay[j].Date })
	for id, w := range wf {
		w.SuccessRate, w.CostUSD = rate(wfOK[id], wfDone[id]), roundUSD(w.CostUSD)
		out.Workflows = append(out.Workflows, *w)
	}
	sort.Slice(out.Workflows, func(i, j int) bool { return out.Workflows[i].Runs > out.Workflows[j].Runs })
	for k, n := range failing {
		out.FailingNodes = append(out.FailingNodes, UsageFailingNode{WorkflowID: k[0], NodeID: k[1], Failures: n})
	}
	sort.Slice(out.FailingNodes, func(i, j int) bool {
		a, b := out.FailingNodes[i], out.FailingNodes[j]
		return a.Failures > b.Failures || a.Failures == b.Failures && a.WorkflowID+a.NodeID < b.WorkflowID+b.NodeID
	})
	out.FailingNodes = out.FailingNodes[:min(len(out.FailingNodes), 10)]
	if want("agent") || agentID != "" {
		for _, a := range ag {
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
	for id, t := range tm {
		t.AvgRounds = math.Round(10*float64(tmRounds[id])/float64(t.Meetings)) / 10
		t.AvgCostUSD = roundUSD(tmCost[id] / float64(t.Meetings))
		t.ConcludedRate = rate(tmConcluded[id], tmEnded[id])
		out.Teams = append(out.Teams, *t)
	}
	sort.Slice(out.Teams, func(i, j int) bool { return out.Teams[i].Meetings > out.Teams[j].Meetings })
	return out, nil
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

func crewUsageRoutes(mux *http.ServeMux, s *Service) {
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
