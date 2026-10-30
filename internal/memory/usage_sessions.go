package memory

import (
	"context"
	"errors"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 사용량 (magpie Usage 화면): the host collector reads every agent's session log,
// keeps only counts (tokens, cost, tools, skills, timing) and reports one row per
// session. No prompt, output or file content is stored here.
const (
	colUsageSessions = "usage_sessions"
	usageBatchMax    = 200
)

type UsageTokens struct {
	Input      int64    `json:"input" bson:"input"`
	Output     int64    `json:"output" bson:"output"`
	CacheRead  int64    `json:"cache_read" bson:"cache_read"`
	CacheWrite int64    `json:"cache_write" bson:"cache_write"`
	Requests   int      `json:"requests" bson:"requests"`
	CostUSD    *float64 `json:"cost_usd,omitempty" bson:"cost_usd,omitempty"`
}

// total is in+out, as magpie counts tokens; cache reads and writes are reported beside it.
func (t UsageTokens) total() int64 { return t.Input + t.Output }

func (t *UsageTokens) add(o UsageTokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Requests += o.Requests
	if o.CostUSD != nil {
		c := *o.CostUSD
		if t.CostUSD != nil {
			c += *t.CostUSD
		}
		t.CostUSD = &c
	}
}

// UsageSession is one agent session as counted on the host.
// Days is "YYYY-MM-DD" (Asia/Seoul) → model → tokens, so every range is exact by day.
type UsageSession struct {
	Agent         string                            `json:"agent" bson:"agent"`
	Device        string                            `json:"device" bson:"device"`
	SessionID     string                            `json:"session_id" bson:"session_id"`
	Title         string                            `json:"title,omitempty" bson:"title,omitempty"`
	Project       string                            `json:"project,omitempty" bson:"project,omitempty"`
	Folder        string                            `json:"folder,omitempty" bson:"folder,omitempty"` // working folder as the agent reported it
	File          string                            `json:"file,omitempty" bson:"file,omitempty"`     // the agent's own log file on that device
	StartedAt     time.Time                         `json:"started_at" bson:"started_at"`
	EndedAt       time.Time                         `json:"ended_at" bson:"ended_at"`
	Messages      int                               `json:"messages" bson:"messages"`
	ActiveSeconds int64                             `json:"active_seconds" bson:"active_seconds"`
	Days          map[string]map[string]UsageTokens `json:"days" bson:"days"`
	Hours         []int                             `json:"hours,omitempty" bson:"hours,omitempty"` // 168 = Mon 0시 … Sun 23시, Asia/Seoul
	Tools         map[string]int                    `json:"tools,omitempty" bson:"tools,omitempty"`
	Skills        map[string]int                    `json:"skills,omitempty" bson:"skills,omitempty"`
	Calls         []UsageCall                       `json:"calls,omitempty" bson:"calls,omitempty"` // MCP calls paired with their results
}

// UsageCall is one MCP tool call the collector paired with its result: the tool, when it was asked,
// how long the result took and a fixed error code when it failed. Never arguments or results.
type UsageCall struct {
	Tool string    `json:"tool" bson:"tool"`
	At   time.Time `json:"at" bson:"at"`
	MS   int64     `json:"ms" bson:"ms"`
	Err  string    `json:"err,omitempty" bson:"err,omitempty"`
}

const usageCallsMax = 2000

var usageCallErrs = []string{"timeout", "permission", "auth", "not_found", "conflict", "rate_limit", "error"}

func validUsageCall(c UsageCall) bool {
	return strings.HasPrefix(c.Tool, "mcp__") && usageNameRe.MatchString(c.Tool) && !c.At.IsZero() && c.MS >= 0 && c.MS <= 86400000 &&
		(c.Err == "" || slices.Contains(usageCallErrs, c.Err))
}

type UsageSessionsInput struct {
	Scope Scope          `json:"scope"`
	Items []UsageSession `json:"items"`
}

var (
	usageNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:/@+-]{1,120}$`)
	usageDayRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func validUsageSession(u UsageSession) bool {
	if !validUsageSessionHead(u) {
		return false
	}
	for day, models := range u.Days {
		if !validUsageDay(day, models) {
			return false
		}
	}
	for _, m := range []map[string]int{u.Tools, u.Skills} {
		if !validUsageCounts(m) {
			return false
		}
	}
	for _, c := range u.Calls {
		if !validUsageCall(c) {
			return false
		}
	}
	return true
}

// validUsageSessionHead checks the session's own fields and the sizes of its tables.
func validUsageSessionHead(u UsageSession) bool {
	return usageNameRe.MatchString(u.Agent) && usageNameRe.MatchString(u.Device) && usageNameRe.MatchString(u.SessionID) &&
		len(u.Title) <= 300 && len(u.Project) <= 200 && usagePath(u.Folder) && usagePath(u.File) && u.Messages >= 0 && u.ActiveSeconds >= 0 &&
		!u.StartedAt.IsZero() && !u.EndedAt.Before(u.StartedAt) && len(u.Days) <= 400 &&
		(len(u.Hours) == 0 || len(u.Hours) == 168) && len(u.Tools) <= 500 && len(u.Skills) <= 500 && len(u.Calls) <= usageCallsMax
}

// usagePath is an optional absolute-looking path: no control characters, at most 1 KiB.
func usagePath(p string) bool {
	return len(p) <= 1024 && utf8.ValidString(p) && strings.IndexFunc(p, unicode.IsControl) < 0
}

func validUsageCounts(m map[string]int) bool {
	for k, n := range m {
		if len(k) == 0 || len(k) > 200 || n < 0 {
			return false
		}
	}
	return true
}

func validUsageDay(day string, models map[string]UsageTokens) bool {
	if !usageDayRe.MatchString(day) || len(models) > 50 {
		return false
	}
	for m, t := range models {
		if !usageNameRe.MatchString(m) || t.Input < 0 || t.Output < 0 || t.CacheRead < 0 || t.CacheWrite < 0 || t.Requests < 0 ||
			t.CostUSD != nil && (math.IsNaN(*t.CostUSD) || *t.CostUSD < 0 || *t.CostUSD > 1e6) {
			return false
		}
	}
	return true
}

func usageSessionID(u UsageSession) string {
	return ContentHash(strings.Join([]string{u.Device, u.Agent, u.SessionID}, "\x00"))
}

// RecordUsageSessions upserts each session by device+agent+session id.
func (s *Service) RecordUsageSessions(ctx context.Context, token string, in UsageSessionsInput) (map[string]int, error) {
	if !validScope(in.Scope) || len(in.Items) == 0 || len(in.Items) > usageBatchMax {
		return nil, ErrInvalid
	}
	for _, u := range in.Items {
		if !validUsageSession(u) {
			return nil, ErrInvalid
		}
	}
	d, err := s.authorize(ctx, token, "write:"+colRoutingCalls, in.Scope, "", "")
	if err != nil {
		return nil, err
	}
	saved := 0
	for _, u := range in.Items {
		u := u
		if containsSecret(u.Title) || containsSecret(u.Project) {
			u.Title, u.Project = "", "" // a pasted key in a title must not stop the rest of the batch
		}
		id := usageSessionID(u)
		if s.usageSessionDeleted(ctx, in.Scope, id) {
			continue // the user deleted this session (세션 지우기); the collector must not bring it back
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		cur, err := s.Repo.Get(ctx, colUsageSessions, in.Scope, id)
		switch {
		case errors.Is(err, ErrNotFound):
			r := Record{ID: id, Scope: in.Scope, Kind: colUsageSessions, Status: statusActive, Revision: 1, Text: u.Title, UsageSession: &u,
				CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
			err = s.Repo.Insert(ctx, colUsageSessions, r)
		case err == nil:
			prev := cur.Revision
			cur.UsageSession, cur.Text, cur.UpdatedAt, cur.Revision = &u, u.Title, now, prev+1
			err = s.Repo.Replace(ctx, colUsageSessions, cur, prev)
		}
		if err != nil {
			return nil, err
		}
		saved++
	}
	return map[string]int{"saved": saved}, nil
}

// ------------------------------------------------------------------ summary

var usageRanges = map[string]int{"today": 1, "7d": 7, "30d": 30, "90d": 90, "all": 0}

type usageAcc struct {
	UsageTokens
	sessions map[string]bool
}

func (a *usageAcc) put(id string, t UsageTokens) {
	a.add(t)
	if a.sessions == nil {
		a.sessions = map[string]bool{}
	}
	a.sessions[id] = true
}

func cost(c *float64) float64 {
	if c == nil {
		return 0
	}
	return math.Round(*c*1e4) / 1e4
}

type usageRow struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Agent    string    `json:"agent"`
	Device   string    `json:"device"`
	Project  string    `json:"project"`
	Models   []string  `json:"models"`
	Tokens   int64     `json:"tokens"`
	Input    int64     `json:"input"`
	Output   int64     `json:"output"`
	Cached   int64     `json:"cached"`
	CostUSD  float64   `json:"cost_usd"`
	Priced   bool      `json:"priced"`
	Messages int       `json:"messages"`
	Active   int64     `json:"active_seconds"`
	Started  time.Time `json:"started_at"`
	Ended    time.Time `json:"ended_at"`
}

func ranked(m map[string]*usageAcc, limit int) []map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := m[keys[i]].total(), m[keys[j]].total()
		return a > b || a == b && keys[i] < keys[j]
	})
	out := []map[string]any{}
	for _, k := range keys {
		if limit > 0 && len(out) == limit {
			break
		}
		a := m[k]
		out = append(out, map[string]any{"name": k, "tokens": a.total(), "input": a.Input, "output": a.Output, "cache_read": a.CacheRead,
			"cache_write": a.CacheWrite, "requests": a.Requests, "cost_usd": cost(a.CostUSD), "priced": a.CostUSD != nil, "sessions": len(a.sessions)})
	}
	return out
}

func counted(m map[string][2]int, limit int) []map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m[keys[i]][0] > m[keys[j]][0] || m[keys[i]][0] == m[keys[j]][0] && keys[i] < keys[j]
	})
	out := []map[string]any{}
	for _, k := range keys {
		if len(out) == limit {
			break
		}
		out = append(out, map[string]any{"name": k, "count": m[k][0], "sessions": m[k][1]})
	}
	return out
}

// zone is the reader's day and month boundary (nil: Asia/Seoul, the collector's day).
func (s *Service) UsageSummary(ctx context.Context, token string, scope Scope, rangeKey, agent string, zone *time.Location) (map[string]any, error) {
	if zone == nil {
		zone = usageZone()
	}
	days, ok := usageRanges[rangeKey]
	if rangeKey == "month" { // 이번 달: from the 1st in the reader's zone
		days, ok = time.Now().In(zone).Day(), true
	}
	if !validScope(scope) || !ok || agent != "" && !usageNameRe.MatchString(agent) {
		return nil, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colRouting, scope, routingID(scope), ""); err != nil {
		return nil, err
	}
	rows, err := s.usageRows(ctx, scope)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(zone)
	t := newUsageSummaryTally(zone, days)
	if days > 0 {
		t.from = now.AddDate(0, 0, 1-days).Format("2006-01-02")
		t.prevFrom = now.AddDate(0, 0, 1-2*days).Format("2006-01-02")
	}
	for _, r := range rows {
		t.add(r, agent)
	}
	out := t.summary(rangeKey, agent, now)
	req, err := s.usageRequests(ctx, scope, days, now)
	if err != nil {
		return nil, err
	}
	out["requests"] = req
	return out, nil
}

// usageSummaryTally gathers UsageSummary's sums over the sessions in the range.
type usageSummaryTally struct {
	seoul          *time.Location
	days           int
	from, prevFrom string

	all                                usageAcc
	byDay, byModel, byProject, byAgent map[string]*usageAcc
	allAgents, dayMsgs                 map[string]int
	tools, skills                      map[string][2]int
	calls                              []usageCallOf
	callsNow, callsPrev                int // tool calls of sessions that ended in this window and in the one before it
	hours, shape                       []int
	sessions                           []usageRow
	active                             int64
	activeDays                         map[string]bool
}

func newUsageSummaryTally(seoul *time.Location, days int) *usageSummaryTally {
	return &usageSummaryTally{
		seoul: seoul, days: days,
		byDay: map[string]*usageAcc{}, byModel: map[string]*usageAcc{}, byProject: map[string]*usageAcc{}, byAgent: map[string]*usageAcc{},
		allAgents: map[string]int{}, dayMsgs: map[string]int{},
		tools: map[string][2]int{}, skills: map[string][2]int{},
		calls: []usageCallOf{}, hours: make([]int, 168), shape: make([]int, 6),
		sessions: []usageRow{}, activeDays: map[string]bool{},
	}
}

// add counts one usage record; agent, when set, keeps only that agent's sessions.
func (t *usageSummaryTally) add(r Record, agent string) {
	u := r.UsageSession
	if u == nil {
		return
	}
	t.allAgents[u.Agent]++
	if agent != "" && u.Agent != agent {
		return
	}
	t.countToolCalls(u)
	sum, models := usageSessionTokens(r.ID, u, t.from, t.byDay, t.byModel)
	inRange := t.from == "" || u.EndedAt.In(t.seoul).Format("2006-01-02") >= t.from
	if !inRange && sum.total()+sum.CacheRead+sum.CacheWrite == 0 {
		return
	}
	t.addSession(r, u, sum, models)
}

// countToolCalls adds the session's tool calls to this window or the one before it.
func (t *usageSummaryTally) countToolCalls(u *UsageSession) {
	end := u.EndedAt.In(t.seoul).Format("2006-01-02")
	if t.days <= 0 || end < t.prevFrom {
		return
	}
	n := 0
	for _, c := range u.Tools {
		n += c
	}
	if end >= t.from {
		t.callsNow += n
	} else {
		t.callsPrev += n
	}
}

func (t *usageSummaryTally) addSession(r Record, u *UsageSession, sum UsageTokens, models map[string]bool) {
	row := usageRow{ID: r.ID, Title: u.Title, Agent: u.Agent, Device: u.Device, Project: u.Project, Messages: u.Messages,
		Active: u.ActiveSeconds, Started: u.StartedAt, Ended: u.EndedAt}
	row.Tokens, row.Input, row.Output, row.Cached = sum.total(), sum.Input, sum.Output, sum.CacheRead+sum.CacheWrite
	row.CostUSD, row.Priced = cost(sum.CostUSD), sum.CostUSD != nil
	row.Models = usageSortedModels(models)
	t.sessions = append(t.sessions, row)
	t.all.put(r.ID, sum)
	project := u.Project
	if project == "" {
		project = "(없음)"
	}
	usageAccOf(t.byProject, project).put(r.ID, sum)
	usageAccOf(t.byAgent, u.Agent).put(r.ID, sum)
	t.active += u.ActiveSeconds
	usageMarkActiveDays(t.activeDays, u, t.seoul, t.from)
	t.dayMsgs[u.EndedAt.In(t.seoul).Format("2006-01-02")] += u.Messages
	usageAddHours(t.hours, u.Hours)
	usageAddCounts(t.tools, u.Tools)
	usageAddCounts(t.skills, u.Skills)
	for _, c := range u.Calls {
		if t.from == "" || c.At.In(t.seoul).Format("2006-01-02") >= t.from {
			t.calls = append(t.calls, usageCallOf{c, u.Agent})
		}
	}
	t.shape[usageShapeBucket(u.Messages)]++
}

// summary is UsageSummary's response, without the requests.
func (t *usageSummaryTally) summary(rangeKey, agent string, now time.Time) map[string]any {
	// by day: every day of the range, empty ones included
	series := usageDaySeries(t.byDay, t.dayMsgs, t.from, now, t.seoul)

	tokensOf := make([]int64, 0, len(t.sessions))
	for _, r := range t.sessions {
		tokensOf = append(tokensOf, r.Tokens)
	}
	slices.Sort(tokensOf)
	pick := func(q float64) int64 {
		if len(tokensOf) == 0 {
			return 0
		}
		return tokensOf[int(math.Ceil(q*float64(len(tokensOf))))-1]
	}
	all := t.all
	hit := usageCacheHit(all.UsageTokens)
	top := slices.Clone(t.sessions)
	sort.SliceStable(top, func(i, j int) bool { return top[i].Tokens > top[j].Tokens })
	latest := slices.Clone(t.sessions)
	sort.SliceStable(latest, func(i, j int) bool { return latest[i].Ended.After(latest[j].Ended) })
	agents := []map[string]any{}
	for name, n := range t.allAgents {
		agents = append(agents, map[string]any{"name": name, "all_sessions": n})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i]["all_sessions"].(int) > agents[j]["all_sessions"].(int) })
	inRoot := 0
	if a := t.byProject["root"]; a != nil {
		inRoot = len(a.sessions)
	}

	toolCalls := 0
	for _, v := range t.tools {
		toolCalls += v[0]
	}
	var change any // nil when the window before has nothing to compare with
	if t.callsPrev > 0 {
		change = int(math.Round(float64(t.callsNow-t.callsPrev) / float64(t.callsPrev) * 100))
	}
	return map[string]any{
		"range": rangeKey, "agent": agent, "agents": agents, "authority": AuthorityDataOnly,
		"kpi": map[string]any{
			"sessions": len(t.sessions), "tokens": all.total(), "input": all.Input, "output": all.Output, "cache_read": all.CacheRead,
			"cache_write": all.CacheWrite, "cost_usd": cost(all.CostUSD), "priced": all.CostUSD != nil, "cache_hit_pct": hit,
			"active_seconds": t.active, "active_days": len(t.activeDays), "projects": len(t.byProject), "root_sessions": inRoot,
			"median_tokens": pick(0.5), "p90_tokens": pick(0.9),
		},
		"by_day": series, "projects": ranked(t.byProject, 0), "models": ranked(t.byModel, 0), "by_agent": ranked(t.byAgent, 0),
		"hours": t.hours, "shape": t.shape, "tools": counted(t.tools, 12), "skills": counted(t.skills, 12),
		"top_sessions": top[:min(len(top), 8)], "latest": latest[:min(len(latest), 50)],
		"tool_kinds": len(t.tools), "tool_calls": toolCalls, "tool_calls_change_pct": change, "tool_health": usageToolHealth(t.calls, series, t.seoul),
	}
}

type usageCallOf struct {
	UsageCall
	agent string
}

// usageMS is the nearest-rank q-quantile of sorted response times, nil when there is none.
func usageMS(sorted []int64, q float64) any {
	if len(sorted) == 0 {
		return nil
	}
	return sorted[int(math.Ceil(q*float64(len(sorted))))-1]
}

// usageToolHealth is 도구 › 사용량's response time and failures (Figma 325:34690): every MCP call the
// collectors paired with its result in the range, per server and per day, and the latest failures.
func usageToolHealth(calls []usageCallOf, series []map[string]any, seoul *time.Location) map[string]any {
	type acc struct {
		ms     []int64
		failed int
	}
	all, byServer, byDay, byDayServer := &acc{}, map[string]*acc{}, map[string]*acc{}, map[string]map[string]*acc{}
	add := func(m map[string]*acc, k string, c usageCallOf) {
		if m[k] == nil {
			m[k] = &acc{}
		}
		m[k].ms = append(m[k].ms, c.MS)
		if c.Err != "" {
			m[k].failed++
		}
	}
	var sum int64
	fails := []usageCallOf{}
	for _, c := range calls {
		all.ms = append(all.ms, c.MS)
		sum += c.MS
		if c.Err != "" {
			all.failed++
			fails = append(fails, c)
		}
		server, day := strings.SplitN(c.Tool, "__", 3)[1], c.At.In(seoul).Format("2006-01-02")
		add(byServer, server, c)
		add(byDay, day, c)
		if byDayServer[day] == nil {
			byDayServer[day] = map[string]*acc{}
		}
		add(byDayServer[day], server, c)
	}
	for _, a := range byServer {
		slices.Sort(a.ms)
	}
	slices.Sort(all.ms)
	servers := []map[string]any{}
	for name, a := range byServer {
		servers = append(servers, map[string]any{"name": name, "calls": len(a.ms), "failed": a.failed, "p95_ms": usageMS(a.ms, .95)})
	}
	sort.Slice(servers, func(i, j int) bool {
		a, b := servers[i], servers[j]
		return a["calls"].(int) > b["calls"].(int) || a["calls"] == b["calls"] && a["name"].(string) < b["name"].(string)
	})
	days := []map[string]any{}
	for _, p := range series {
		k := p["date"].(string)
		a := byDay[k]
		if a == nil {
			a = &acc{}
		}
		slices.Sort(a.ms)
		perServer := []map[string]any{}
		for _, name := range slices.Sorted(maps.Keys(byDayServer[k])) {
			sa := byDayServer[k][name]
			slices.Sort(sa.ms)
			perServer = append(perServer, map[string]any{"name": name, "calls": len(sa.ms), "failed": sa.failed, "p95_ms": usageMS(sa.ms, .95)})
		}
		days = append(days, map[string]any{"date": k, "calls": len(a.ms), "failed": a.failed, "p95_ms": usageMS(a.ms, .95), "servers": perServer})
	}
	sort.SliceStable(fails, func(i, j int) bool { return fails[i].At.After(fails[j].At) })
	recent := []map[string]any{}
	for _, c := range fails[:min(len(fails), 8)] {
		recent = append(recent, map[string]any{"tool": c.Tool, "at": c.At, "ms": c.MS, "err": c.Err, "agent": c.agent})
	}
	var avg any
	if len(all.ms) > 0 {
		avg = sum / int64(len(all.ms))
	}
	return map[string]any{"calls": len(all.ms), "failed": all.failed, "p50_ms": usageMS(all.ms, .5), "p95_ms": usageMS(all.ms, .95), "avg_ms": avg,
		"servers": servers, "by_day": days, "fails": recent}
}

func usageAccOf(m map[string]*usageAcc, k string) *usageAcc {
	if m[k] == nil {
		m[k] = &usageAcc{}
	}
	return m[k]
}

// usageSessionTokens sums one session's tokens from the range start on and
// adds them to the per-day and per-model totals.
func usageSessionTokens(id string, u *UsageSession, from string, byDay, byModel map[string]*usageAcc) (UsageTokens, map[string]bool) {
	var sum UsageTokens
	models := map[string]bool{}
	for day, perModel := range u.Days {
		if day < from {
			continue
		}
		for m, t := range perModel {
			if t.total()+t.CacheRead+t.CacheWrite == 0 && t.Requests == 0 {
				continue
			}
			sum.add(t)
			models[m] = true
			usageAccOf(byDay, day).put(id, t)
			usageAccOf(byModel, m).put(id, t)
		}
	}
	return sum, models
}

func usageMarkActiveDays(activeDays map[string]bool, u *UsageSession, seoul *time.Location, from string) {
	for d := u.StartedAt.In(seoul); !d.After(u.EndedAt.In(seoul).Add(24 * time.Hour)); d = d.AddDate(0, 0, 1) {
		if k := d.Format("2006-01-02"); k >= from && k <= u.EndedAt.In(seoul).Format("2006-01-02") {
			activeDays[k] = true
		}
	}
}

func usageSortedModels(models map[string]bool) []string {
	var out []string
	for m := range models {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func usageAddHours(hours, add []int) {
	for i, n := range add {
		if i < 168 {
			hours[i] += n
		}
	}
}

func usageCacheHit(all UsageTokens) float64 {
	hitBase := all.Input + all.CacheRead + all.CacheWrite
	hit := 0.0
	if hitBase > 0 {
		hit = math.Round(1000*float64(all.CacheRead)/float64(hitBase)) / 10
	}
	return hit
}

func usageAddCounts(into map[string][2]int, counts map[string]int) {
	for k, n := range counts {
		v := into[k]
		into[k] = [2]int{v[0] + n, v[1] + 1}
	}
}

func usageShapeBucket(messages int) int {
	switch m := messages; {
	case m <= 5:
		return 0
	case m <= 15:
		return 1
	case m <= 30:
		return 2
	case m <= 60:
		return 3
	case m <= 120:
		return 4
	default:
		return 5
	}
}

func usageDaySeries(byDay map[string]*usageAcc, dayMsgs map[string]int, from string, now time.Time, seoul *time.Location) []map[string]any {
	series := []map[string]any{}
	first := from
	if first == "" {
		for k := range byDay {
			if first == "" || k < first {
				first = k
			}
		}
	}
	if first != "" {
		start, _ := time.ParseInLocation("2006-01-02", first, seoul)
		for d := start; d.Format("2006-01-02") <= now.Format("2006-01-02"); d = d.AddDate(0, 0, 1) {
			k := d.Format("2006-01-02")
			a := usageAccOf(byDay, k)
			series = append(series, map[string]any{"date": k, "tokens": a.total(), "output": a.Output, "cost_usd": cost(a.CostUSD),
				"sessions": len(a.sessions), "messages": dayMsgs[k]})
		}
	}
	return series
}

// UsageSessionList is 세션 메뉴: newest first, optionally one agent and a literal title/project/id match.
func (s *Service) UsageSessionList(ctx context.Context, token string, scope Scope, rangeKey, agent, q string, limit int) (map[string]any, error) {
	return s.UsageSessionListPage(ctx, token, scope, rangeKey, agent, q, "recent", "", limit)
}

func pairs(m map[string]int) map[string][2]int {
	out := make(map[string][2]int, len(m))
	for k, n := range m {
		out[k] = [2]int{n, 1}
	}
	return out
}

// usageTZ reads ?tz=<minutes east of UTC> (the browser's -getTimezoneOffset()); empty keeps the default zone.
// shortcut: a fixed offset ignores a DST change inside the range, fine for day and month boundaries.
func usageTZ(v string) (*time.Location, bool) {
	if v == "" {
		return nil, true
	}
	m, err := strconv.Atoi(v)
	if err != nil || m < -14*60 || m > 14*60 {
		return nil, false
	}
	return time.FixedZone("", m*60), true
}

func usageZone() *time.Location {
	if z, err := time.LoadLocation("Asia/Seoul"); err == nil {
		return z
	}
	return time.FixedZone("KST", 9*3600)
}

func (s *Service) usageRows(ctx context.Context, scope Scope) ([]Record, error) {
	if all, ok := s.Repo.(interface {
		UsageSessionRows(context.Context, Scope) ([]Record, error)
	}); ok {
		return all.UsageSessionRows(ctx, scope)
	}
	return collectCatalogRecords(ctx, s.Repo, scope, colUsageSessions)
}

func (p *Postgres) UsageSessionRows(ctx context.Context, scope Scope) ([]Record, error) {
	return collectCatalogRecords(ctx, p, scope, colUsageSessions)
}

// usageRequests is 요청 탭: the routed calls the host runner reported (POST /v1/routing/calls).
func (s *Service) usageRequests(ctx context.Context, scope Scope, days int, now time.Time) (map[string]any, error) {
	rows, err := s.Repo.ListRecords(ctx, scope, colRoutingCalls, routingCallsRead)
	if err != nil {
		return nil, err
	}
	var since time.Time
	if days > 0 {
		y, m, d := now.AddDate(0, 0, 1-days).Date()
		since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}
	items := []map[string]any{}
	var n, fails int
	var in, outTok int64
	var total float64
	byModel := map[string]*usageAcc{}
	for _, r := range rows {
		c := r.RoutingCall
		if c == nil || systemCall(c) || r.CreatedAt.Before(since) {
			continue
		}
		n++
		if !c.OK {
			fails++
		}
		in += c.InputTokens
		outTok += c.OutputTokens
		t := UsageTokens{Input: c.InputTokens, Output: c.OutputTokens, Requests: 1, CostUSD: c.CostUSD}
		if byModel[c.Model] == nil {
			byModel[c.Model] = &usageAcc{}
		}
		byModel[c.Model].put(r.ID, t)
		if c.CostUSD != nil {
			total += *c.CostUSD
		}
		if len(items) < 200 {
			items = append(items, map[string]any{"at": r.CreatedAt, "model": c.Model, "replaced": c.Replaced, "decision": c.Decision, "rule": c.Rule,
				"ok": c.OK, "latency_ms": c.LatencyMS, "input": c.InputTokens, "output": c.OutputTokens, "cost_usd": cost(c.CostUSD), "error": c.Error})
		}
	}
	return map[string]any{"count": n, "failed": fails, "input": in, "output": outTok, "cost_usd": math.Round(total*1e4) / 1e4,
		"models": ranked(byModel, 0), "items": items}, nil
}

func usageRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/usage/summary", func(w http.ResponseWriter, r *http.Request) {
		rg := r.URL.Query().Get("range")
		if rg == "" {
			rg = "30d"
		}
		zone, ok := usageTZ(r.URL.Query().Get("tz"))
		if !ok {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.UsageSummary(r.Context(), bearer(r), scopeQuery(r), rg, r.URL.Query().Get("source"), zone)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/usage/sessions", func(w http.ResponseWriter, r *http.Request) {
		v, err := parseUsageSessionQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		limit, _ := strconv.Atoi(v.Get("limit"))
		out, e := s.usageSessionListPage(r.Context(), bearer(r), scopeQuery(r), v.Get("range"), v.Get("source"), v.Get("project"), v.Get("q"), v.Get("sort"), v.Get("cursor"), limit)
		respond(w, usageSessionResponse(out), e)
	})
	mux.HandleFunc("POST /v1/usage/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in UsageSessionsInput
		if !decodeLimit(w, r, &in, 8<<20) {
			return
		}
		out, e := s.RecordUsageSessions(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
