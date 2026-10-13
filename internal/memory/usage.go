package memory

import (
	"context"
	"time"
)

type MemoryUse struct {
	Memory Ref `json:"memory" bson:"memory"`
	Rank   int `json:"rank" bson:"rank"`
}
type ContextUsage struct {
	Run     *Ref        `json:"run,omitempty" bson:"run,omitempty"`
	Purpose string      `json:"purpose,omitempty" bson:"purpose,omitempty"`
	Items   []MemoryUse `json:"items" bson:"items"`
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
	Run         *Ref      `json:"run,omitempty"`
}
type UsagePage struct {
	Items     []UsageEntry `json:"items"`
	Authority string       `json:"authority"`
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
	out := UsagePage{Items: []UsageEntry{}, Authority: AuthorityDataOnly}
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
				out.Items = append(out.Items, UsageEntry{ContextID: r.ID, CreatedAt: r.CreatedAt, RequestedBy: r.CreatedBy, Memory: item.Memory, Rank: item.Rank, Run: r.Usage.Run})
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
