package memory

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// SkillUse is how often one skill ran lately (skill.usage trace events), per runtime.
type SkillUse struct {
	SkillID   string         `json:"skill_id"`
	Count     int            `json:"count"`
	Failures  int            `json:"failures"` // status failure or validation_failed
	LastUsed  time.Time      `json:"last_used_at"`
	ByRuntime map[string]int `json:"by_runtime"`
}

type SkillUsagePage struct {
	Days  int        `json:"days"`
	Items []SkillUse `json:"items"`
}

// SkillUsage sums skill.usage events of the workspace, project and user over
// every agent, for the skills table (최근 사용·많이 쓴 순·안 씀).
func (s *Service) SkillUsage(ctx context.Context, token string, scope Scope, days int) (SkillUsagePage, error) {
	if !validScope(scope) || days < 1 || days > 365 {
		return SkillUsagePage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:skills", scope, "", ""); err != nil {
		return SkillUsagePage{}, err
	}
	out := SkillUsagePage{Days: days, Items: []SkillUse{}}
	counter, ok := s.Repo.(interface {
		SkillUsageSince(context.Context, Scope, time.Time) ([]SkillUse, error)
	})
	if !ok {
		return out, nil // only Postgres keeps trace events
	}
	items, err := counter.SkillUsageSince(ctx, scope, time.Now().Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return SkillUsagePage{}, err
	}
	out.Items = items
	return out, nil
}

func (p *Postgres) SkillUsageSince(ctx context.Context, scope Scope, since time.Time) ([]SkillUse, error) {
	rows, err := p.q().Query(ctx, `SELECT payload->>'skill_id', COALESCE(payload->>'runtime',''), count(*), count(*) FILTER (WHERE payload->>'status' IN ('failure','validation_failed')), max(occurred_at) FROM source_events
WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND kind=$4 AND occurred_at>=$5 AND NOT content_erased AND NOT privacy_quarantined
GROUP BY 1, 2`, scope.WorkspaceID, scope.ProjectID, scope.UserID, skillUsageEventKind, since)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	by := map[string]*SkillUse{}
	order := []string{}
	for rows.Next() {
		var id, runtime string
		var n, failed int
		var last time.Time
		if err := rows.Scan(&id, &runtime, &n, &failed, &last); err != nil {
			return nil, pgErr(err)
		}
		u := by[id]
		if u == nil {
			u = &SkillUse{SkillID: id, ByRuntime: map[string]int{}}
			by[id] = u
			order = append(order, id)
		}
		u.Count += n
		u.Failures += failed
		if runtime != "" {
			u.ByRuntime[runtime] += n
		}
		if last.After(u.LastUsed) {
			u.LastUsed = last.UTC()
		}
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	out := make([]SkillUse, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	return out, nil
}

func skillUsageRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/skills/usage", func(w http.ResponseWriter, r *http.Request) {
		days := 60
		if v := r.URL.Query().Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			days = n
		}
		out, e := s.SkillUsage(r.Context(), bearer(r), scopeQuery(r), days)
		respond(w, out, e)
	})
}
