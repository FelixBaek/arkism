package memory

import (
	"context"
	"errors"
	"time"
)

type MemoryUse struct {
	Memory Ref `json:"memory" bson:"memory"`
	Rank   int `json:"rank" bson:"rank"`
	// Score and Lanes are the fused score and the lanes that found it (ST-811); pinned memories have neither.
	Score float64  `json:"score,omitempty" bson:"score,omitempty"`
	Lanes []string `json:"lanes,omitempty" bson:"lanes,omitempty"`
}
type ContextUsage struct {
	Run     *Ref        `json:"run,omitempty" bson:"run,omitempty"`
	Purpose string      `json:"purpose,omitempty" bson:"purpose,omitempty"`
	Items   []MemoryUse `json:"items" bson:"items"`
	// ProjectKey (ST-825) is the search's project key, so a memory's uses can be counted per project.
	ProjectKey string `json:"project_key,omitempty" bson:"project_key,omitempty"`
}

// pinnedUsage is a receipt body that starts with the pinned memories, in order.
func pinnedUsage(run *Ref, purpose string, pinned []Ref) *ContextUsage {
	u := &ContextUsage{Run: run, Purpose: purpose, Items: []MemoryUse{}}
	for _, ref := range pinned {
		if ref.Collection == colMemories && !usedRef(u, ref.ID) {
			u.Items = append(u.Items, MemoryUse{Memory: ref, Rank: len(u.Items) + 1})
		}
	}
	return u
}

func usedRef(u *ContextUsage, id string) bool {
	for _, it := range u.Items {
		if it.Memory.ID == id {
			return true
		}
	}
	return false
}

// insertUsageReceipt stores the context_usage receipt: which memories one
// context handed out (ids and revisions only), for 사용 기록 and utility.
func insertUsageReceipt(ctx context.Context, tx Tx, scope Scope, id, principal string, usage *ContextUsage) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return tx.Insert(ctx, "jobs", Record{ID: id, Scope: scope, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: principal, Usage: usage})
}

type UsageEntry struct {
	ContextID   string    `json:"context_id"`
	CreatedAt   time.Time `json:"created_at"`
	RequestedBy string    `json:"requested_by"`
	Memory      Ref       `json:"memory"`
	Rank        int       `json:"rank"`
	Score       float64   `json:"score,omitempty"`
	Lanes       []string  `json:"lanes,omitempty"`
	Run         *Ref      `json:"run,omitempty"`
}
type UsagePage struct {
	MeasurementBasis string       `json:"measurement_basis"`
	ActualUseStatus  string       `json:"actual_use_status"`
	Items            []UsageEntry `json:"items"`
	Authority        string       `json:"authority"`
}

func (s *Service) RecentMemoryUsage(ctx context.Context, token string, scope Scope, id string) (UsagePage, error) {
	if id == "" || len(id) > 256 {
		return UsagePage{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "read:memories", scope, id, "")
	if e != nil {
		return UsagePage{}, e
	}
	if s.Repo == nil {
		return UsagePage{}, ErrUnavailable
	}
	if r, e := s.Repo.Get(ctx, "memories", scope, id); e != nil {
		return UsagePage{}, e
	} else if r.PrivacyQuarantined || r.ContentErased {
		return UsagePage{}, ErrNotFound
	}
	allowed, e := s.authorize(ctx, token, "read:usage", scope, "", "")
	if e != nil {
		return UsagePage{}, e
	}
	if allowed.PrincipalID != who.PrincipalID || allowed.Role != who.Role {
		return UsagePage{}, ErrDenied
	}
	rows, e := s.Repo.MemoryUsage(ctx, scope, id)
	if e != nil {
		return UsagePage{}, e
	}
	if len(rows) > 10 {
		return UsagePage{}, ErrUnavailable
	}
	out := UsagePage{Items: []UsageEntry{}, Authority: AuthorityDataOnly, MeasurementBasis: "server_return", ActualUseStatus: "not_measured"}
	for _, r := range rows {
		if r.Scope != scope || r.Kind != "context_usage" || r.Status != "done" || r.Usage == nil {
			return UsagePage{}, ErrUnavailable
		}
		d, e := s.authorize(ctx, token, "read:usage", scope, r.ID, "")
		if e != nil {
			return UsagePage{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return UsagePage{}, ErrDenied
		}
		if r.Usage.Run != nil {
			if r.Usage.Run.Collection != "agent_runs" || r.Usage.Run.Revision < 1 {
				return UsagePage{}, ErrUnavailable
			}
			d, e = s.authorize(ctx, token, "read:agent_runs", scope, r.Usage.Run.ID, "")
			if e != nil {
				return UsagePage{}, e
			}
			if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
				return UsagePage{}, ErrDenied
			}
		}
		found := false
		for _, item := range r.Usage.Items {
			if item.Memory.Collection == "memories" && item.Memory.ID == id {
				if found || item.Memory.Revision < 1 || item.Rank < 1 || item.Rank > 50 {
					return UsagePage{}, ErrUnavailable
				}
				out.Items = append(out.Items, UsageEntry{ContextID: r.ID, CreatedAt: r.CreatedAt, RequestedBy: r.CreatedBy, Memory: item.Memory, Rank: item.Rank, Score: item.Score, Lanes: item.Lanes, Run: r.Usage.Run})
				found = true
			}
		}
		if !found {
			return UsagePage{}, ErrUnavailable
		}
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, scope, who.PrincipalID, "read:memory_usage", id)
	}); e != nil {
		return UsagePage{}, e
	}
	return out, nil
}

// MemoryUsageCount is one memory's use in a recent window: Recent counts uses in the shorter window.
type MemoryUsageCount struct {
	MemoryID string    `json:"id"`
	Recent   int       `json:"d7"`
	Uses     int       `json:"d30"`
	Last     time.Time `json:"last"`
}
type UsageSummaryPage struct {
	MeasurementBasis string             `json:"measurement_basis"`
	ActualUseStatus  string             `json:"actual_use_status"`
	Items            []MemoryUsageCount `json:"items"`
	Authority        string             `json:"authority"`
}

// MemoryUsageSummary counts, for every memory used in the last 30 days, its uses in 7 and 30 days and the last use,
// so a list screen reads usage in one call instead of one call per memory. Memories never used are absent.
func (s *Service) MemoryUsageSummary(ctx context.Context, token string, scope Scope) (UsageSummaryPage, error) {
	who, e := s.authorize(ctx, token, "read:memories", scope, "", "")
	if e != nil {
		return UsageSummaryPage{}, e
	}
	allowed, e := s.authorize(ctx, token, "read:usage", scope, "", "")
	if e != nil {
		return UsageSummaryPage{}, e
	}
	if allowed.PrincipalID != who.PrincipalID || allowed.Role != who.Role {
		return UsageSummaryPage{}, ErrDenied
	}
	if s.Repo == nil {
		return UsageSummaryPage{}, ErrUnavailable
	}
	now := time.Now().UTC()
	rows, e := s.Repo.MemoryUsageWindow(ctx, scope, now.Add(-30*24*time.Hour), now.Add(-7*24*time.Hour))
	if e != nil {
		return UsageSummaryPage{}, e
	}
	out := UsageSummaryPage{Items: []MemoryUsageCount{}, Authority: AuthorityDataOnly, MeasurementBasis: "server_return", ActualUseStatus: "not_measured"}
	// The Postgres and local authorizers decide per scope and action, so the decision above already covers every row;
	// asking again per memory would be one database query per row.
	perRow := !scopeOnly(s.Auth)
	for _, r := range rows {
		if ctx.Err() != nil {
			return UsageSummaryPage{}, ErrUnavailable
		}
		if !validRunLabel(r.MemoryID, 256) || containsSecret(r.MemoryID) || r.Uses < 1 || r.Recent < 0 || r.Recent > r.Uses {
			return UsageSummaryPage{}, ErrUnavailable
		}
		if !perRow {
			out.Items = append(out.Items, r)
			continue
		}
		d, e := s.authorize(ctx, token, "read:memories", scope, r.MemoryID, "")
		if errors.Is(e, ErrDenied) {
			continue
		}
		if e != nil {
			return UsageSummaryPage{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return UsageSummaryPage{}, ErrDenied
		}
		out.Items = append(out.Items, r)
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, scope, who.PrincipalID, "read:memory_usage", "")
	}); e != nil {
		return UsageSummaryPage{}, e
	}
	return out, nil
}

// scopeOnlyAuthorizer is an Authorizer whose decision never depends on the resource id or content.
type scopeOnlyAuthorizer interface{ ScopeOnly() bool }

func scopeOnly(a Authorizer) bool {
	x, ok := a.(scopeOnlyAuthorizer)
	return ok && x.ScopeOnly()
}

// RecallEntry is one context handed out: who asked, for what, and the memories returned with their fused score and lanes.
type RecallEntry struct {
	ContextID   string      `json:"id"`
	CreatedAt   time.Time   `json:"created_at"`
	RequestedBy string      `json:"requested_by"`
	Purpose     string      `json:"purpose,omitempty"`
	ProjectKey  string      `json:"project_key,omitempty"`
	Items       []MemoryUse `json:"items"`
}
type RecallPage struct {
	MeasurementBasis string        `json:"measurement_basis"`
	Items            []RecallEntry `json:"items"`
	Authority        string        `json:"authority"`
}

// RecentRecalls lists the newest context receipts (불러오기 window). Receipts keep what was returned, not what was
// left out below the score cut, so nothing about "안 넣음" can be shown from them.
func (s *Service) RecentRecalls(ctx context.Context, token string, scope Scope, limit int) (RecallPage, error) {
	if limit < 1 || limit > 50 {
		return RecallPage{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "read:memories", scope, "", "")
	if e != nil {
		return RecallPage{}, e
	}
	allowed, e := s.authorize(ctx, token, "read:usage", scope, "", "")
	if e != nil {
		return RecallPage{}, e
	}
	if allowed.PrincipalID != who.PrincipalID || allowed.Role != who.Role {
		return RecallPage{}, ErrDenied
	}
	if s.Repo == nil {
		return RecallPage{}, ErrUnavailable
	}
	rows, e := s.Repo.RecentContextUsage(ctx, scope, limit)
	if e != nil {
		return RecallPage{}, e
	}
	out := RecallPage{Items: []RecallEntry{}, Authority: AuthorityDataOnly, MeasurementBasis: "server_return"}
	perRow := !scopeOnly(s.Auth)
	for _, r := range rows {
		if r.Scope != scope || r.Kind != "context_usage" || r.Usage == nil {
			return RecallPage{}, ErrUnavailable
		}
		entry := RecallEntry{ContextID: r.ID, CreatedAt: r.CreatedAt, RequestedBy: r.CreatedBy, Purpose: r.Usage.Purpose, ProjectKey: r.Usage.ProjectKey, Items: []MemoryUse{}}
		for _, it := range r.Usage.Items {
			if it.Memory.Collection != colMemories || !validRunLabel(it.Memory.ID, 256) || containsSecret(it.Memory.ID) {
				continue
			}
			if perRow {
				d, e := s.authorize(ctx, token, "read:memories", scope, it.Memory.ID, "")
				if errors.Is(e, ErrDenied) {
					continue
				}
				if e != nil {
					return RecallPage{}, e
				}
				if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
					return RecallPage{}, ErrDenied
				}
			}
			entry.Items = append(entry.Items, it)
		}
		out.Items = append(out.Items, entry)
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, scope, who.PrincipalID, "read:memory_usage", "")
	}); e != nil {
		return RecallPage{}, e
	}
	return out, nil
}
