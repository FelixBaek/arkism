package memory

import (
	"context"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UsageSessionCatalogPage is the HTTP envelope; items retain the existing
// session projection used by the dashboard and export consumers.
type UsageSessionCatalogPage struct {
	Items      []map[string]any `json:"items"`
	Total      int              `json:"total"`
	NextCursor string           `json:"next_cursor"`
	Authority  string           `json:"authority"`
	// Facets counts sessions per agent and per project (folder name) for the same range and search.
	Facets map[string]map[string]int `json:"facets,omitempty"`
}

func usageSessionResponse(out map[string]any) UsageSessionCatalogPage {
	facets, _ := out["facets"].(map[string]map[string]int)
	items, _ := out["items"].([]map[string]any)
	total, _ := out["total"].(int)
	cursor, _ := out["next_cursor"].(string)
	return UsageSessionCatalogPage{Items: items, Total: total, NextCursor: cursor, Authority: AuthorityDataOnly, Facets: facets}
}

func (s *Service) UsageSessionListPage(ctx context.Context, token string, scope Scope, rangeKey, agent, q, order, cursor string, limit int) (map[string]any, error) {
	return s.usageSessionListPage(ctx, token, scope, rangeKey, agent, "", q, order, cursor, limit)
}

// usageSessionListPage adds an exact project (folder name) filter to the session list.
func (s *Service) usageSessionListPage(ctx context.Context, token string, scope Scope, rangeKey, agent, project, q, order, cursor string, limit int) (map[string]any, error) {
	days, err := normalizeUsageSessionCatalog(scope, rangeKey, agent, q, order, cursor, limit)
	if err == nil && len(project) > 200 {
		err = ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	ctx, actor, err := s.catalogActor(ctx, token, "read:"+colRouting, scope)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(q)
	query := CatalogQueryInput{Scope: scope, Query: q, Model: agent, Repository: project, Sort: order, State: rangeKey, Limit: limit, Cursor: cursor}
	binding := catalogBinding(query, "sessions", actor.PrincipalID+"/"+actor.Role)
	if _, err = decodeCatalogCursor(query, binding); err != nil {
		return nil, err
	}
	rows, err := s.usageRows(ctx, scope)
	if err != nil {
		return nil, err
	}
	var since time.Time
	if days > 0 {
		y, m, d := time.Now().In(usageZone()).AddDate(0, 0, 1-days).Date()
		since = time.Date(y, m, d, 0, 0, 0, 0, usageZone())
	}
	items := []map[string]any{}
	total := 0
	// facets: each count ignores its own filter, so the agent and project switches show what picking them would give
	agents, projects := map[string]int{}, map[string]int{}
	for _, r := range rows {
		u := r.UsageSession
		if r.PrivacyQuarantined || r.ContentErased || u == nil || u.EndedAt.Before(since) ||
			q != "" && !strings.Contains(strings.ToLower(u.Title+"\x00"+u.Project+"\x00"+u.SessionID+"\x00"+u.Folder), q) {
			continue
		}
		agentOK, projectOK := agent == "" || u.Agent == agent, project == "" || u.Project == project
		if projectOK {
			agents[u.Agent]++
		}
		if agentOK && u.Project != "" {
			projects[u.Project]++
		}
		if !agentOK || !projectOK {
			continue
		}
		total++
		items = append(items, usageCatalogItem(r))
	}
	sort.SliceStable(items, func(i, j int) bool { return usageCatalogLess(items[i], items[j], order) })
	selected, next, err := catalogWindow(items, query, binding)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": selected, "total": total, "next_cursor": next, "authority": AuthorityDataOnly,
		"facets": map[string]map[string]int{"agents": agents, "projects": projects}}, nil
}
func usageCatalogNumber(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int64:
		return number
	}
	return 0
}

func catalogSortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeUsageSessionCatalog(scope Scope, rangeKey, agent, q, order, cursor string, limit int) (int, error) {
	days, ok := usageRanges[rangeKey]
	if !validScope(scope) || !ok || agent != "" && !usageNameRe.MatchString(agent) || !catalogText(q, 200) || limit < 1 || limit > 500 || !slices.Contains([]string{"recent", "tokens", "cost", "messages"}, order) || len(cursor) > 2048 {
		return 0, ErrInvalid
	}
	return days, nil
}
func usageCatalogItem(r Record) map[string]any {
	u := r.UsageSession
	var sum UsageTokens
	models := map[string]*UsageTokens{}
	for _, day := range catalogSortedKeys(u.Days) {
		perModel := u.Days[day]
		for _, m := range catalogSortedKeys(perModel) {
			t := perModel[m]
			sum.add(t)
			if models[m] == nil {
				models[m] = &UsageTokens{}
			}
			models[m].add(t)
		}
	}
	byModel := []map[string]any{}
	for m, t := range models {
		byModel = append(byModel, map[string]any{"name": m, "tokens": t.total(), "input": t.Input, "output": t.Output, "cache_read": t.CacheRead,
			"cache_write": t.CacheWrite, "requests": t.Requests, "cost_usd": cost(t.CostUSD), "priced": t.CostUSD != nil})
	}
	sort.Slice(byModel, func(i, j int) bool {
		if byModel[i]["tokens"] != byModel[j]["tokens"] {
			return byModel[i]["tokens"].(int64) > byModel[j]["tokens"].(int64)
		}
		return byModel[i]["name"].(string) < byModel[j]["name"].(string)
	})
	return map[string]any{"id": r.ID, "agent": u.Agent, "device": u.Device, "session_id": u.SessionID, "title": u.Title,
		"project": u.Project, "folder": u.Folder, "file": u.File, "started_at": u.StartedAt, "ended_at": u.EndedAt, "messages": u.Messages, "active_seconds": u.ActiveSeconds,
		"tokens": sum.total(), "input": sum.Input, "output": sum.Output, "cache_read": sum.CacheRead, "cache_write": sum.CacheWrite,
		"requests": sum.Requests, "cost_usd": cost(sum.CostUSD), "priced": sum.CostUSD != nil, "models": byModel,
		"tools": counted(pairs(u.Tools), len(u.Tools)), "skills": counted(pairs(u.Skills), len(u.Skills))}
}

func parseUsageSessionQuery(raw string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	allowed := []string{"workspace_id", "project_id", "user_id", "agent_id", "range", "source", "project", "q", "sort", "cursor", "limit"}
	for key, entries := range values {
		if len(entries) != 1 || !slices.Contains(allowed, key) {
			return nil, ErrInvalid
		}
	}
	for key, value := range map[string]string{"range": "30d", "sort": "recent", "limit": "200"} {
		if values.Get(key) == "" {
			values.Set(key, value)
		}
	}
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	return values, nil
}

func usageCatalogLess(a, b map[string]any, order string) bool {
	if order == "cost" && a["cost_usd"].(float64) != b["cost_usd"].(float64) {
		return a["cost_usd"].(float64) > b["cost_usd"].(float64)
	}
	key := map[string]string{"tokens": "tokens", "messages": "messages"}[order]
	if key != "" {
		av, bv := usageCatalogNumber(a[key]), usageCatalogNumber(b[key])
		if av != bv {
			return av > bv
		}
	}
	if !a["ended_at"].(time.Time).Equal(b["ended_at"].(time.Time)) {
		return a["ended_at"].(time.Time).After(b["ended_at"].(time.Time))
	}
	return a["id"].(string) < b["id"].(string)
}
