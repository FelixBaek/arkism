package memory

import (
	"context"
	"net/url"
	"slices"
	"strconv"
)

type DerivedListInput struct {
	Scope      Scope
	Collection string
	Limit      int
	Cursor     string
}
type SourceDerived struct {
	Source     Ref      `json:"source"`
	Collection string   `json:"collection"`
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor"`
	Authority  string   `json:"authority"`
}

func normalizeDerivedList(in DerivedListInput) (DerivedListInput, error) {
	if in.Collection == "" {
		in.Collection = "memories"
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || !slices.Contains([]string{"memories", "memory_candidates"}, in.Collection) || in.Limit < 1 || in.Limit > 100 || in.Cursor != "" && (!validRunLabel(in.Cursor, 256) || containsSecret(in.Cursor)) {
		return in, ErrInvalid
	}
	return in, nil
}
func parseDerivedListQuery(raw string) (DerivedListInput, error) {
	var in DerivedListInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "collection", "limit", "cursor"}, key) {
			return in, ErrInvalid
		}
	}
	if values, ok := q["collection"]; ok && values[0] == "" {
		return in, ErrInvalid
	}
	in = DerivedListInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Collection: q.Get("collection"), Limit: 20, Cursor: q.Get("cursor")}
	if values, ok := q["limit"]; ok {
		in.Limit, e = strconv.Atoi(values[0])
		if e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	return normalizeDerivedList(in)
}

func directlyDerived(r Record, sourceID string) bool {
	for _, ref := range r.Sources {
		if ref.SourceID == sourceID {
			return true
		}
	}
	return false
}
func (s *Service) authorizeDerivedRows(ctx context.Context, token, sourceID string, in DerivedListInput, actor Decision, rows []Record) error {
	// shortcut: up to 102 sequential policy calls; batch only with equivalent per-record decisions.
	for _, r := range rows {
		if r.Scope != in.Scope || !validRunLabel(r.ID, 256) || r.Revision < 1 || r.ContentErased || !directlyDerived(r, sourceID) {
			return ErrUnavailable
		}
		d, e := s.authorize(ctx, token, "read:"+in.Collection, in.Scope, r.ID, structuredText(r))
		if e != nil {
			return e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return ErrDenied
		}
	}
	return nil
}
func (s *Service) ListSourceDerived(ctx context.Context, token, sourceID string, in DerivedListInput) (SourceDerived, error) {
	in, e := normalizeDerivedList(in)
	if e != nil {
		return SourceDerived{}, e
	}
	if !validRunLabel(sourceID, 256) {
		return SourceDerived{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "read:sources", in.Scope, sourceID, "")
	if e != nil {
		return SourceDerived{}, e
	}
	if s.Repo == nil {
		return SourceDerived{}, ErrUnavailable
	}
	src, e := s.Repo.Get(ctx, "sources", in.Scope, sourceID)
	if e != nil {
		return SourceDerived{}, e
	}
	if !live(src) {
		return SourceDerived{}, ErrNotFound
	}
	rows, e := s.Repo.ListSourceDerived(ctx, sourceID, in)
	if e != nil {
		return SourceDerived{}, e
	}
	if len(rows) > in.Limit+1 {
		return SourceDerived{}, ErrUnavailable
	}
	if e = s.authorizeDerivedRows(ctx, token, sourceID, in, actor, rows); e != nil {
		return SourceDerived{}, e
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		source, e := tx.Get(ctx, "sources", in.Scope, sourceID)
		if e != nil {
			return e
		}
		if !live(source) {
			return ErrNotFound
		}
		if source.Revision != src.Revision || source.Sensitivity != src.Sensitivity {
			return ErrConflict
		}
		sources := map[string]bool{sourceID: true}
		for _, r := range rows {
			current, e := tx.Get(ctx, in.Collection, in.Scope, r.ID)
			if e != nil {
				return e
			}
			if current.Revision != r.Revision || structuredText(current) != structuredText(r) {
				return ErrConflict
			}
			metadata, e := sourceMetadata(ctx, tx, in.Scope, current.Sources)
			if e != nil {
				return e
			}
			for _, ref := range metadata {
				sources[ref.ID] = true
			}
			if e = tx.FenceRelationMember(ctx, in.Scope, in.Collection, r.ID); e != nil {
				return e
			}
		}
		// A source shared by multiple records needs only one write in this snapshot.
		ids := make([]string, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if e = tx.FenceSource(ctx, in.Scope, id); e != nil {
				return e
			}
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "read:sources", sourceID); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:source_derived:"+in.Collection, sourceID)
	})
	if e != nil {
		return SourceDerived{}, e
	}
	out := SourceDerived{Source: Ref{Collection: "sources", ID: sourceID, Revision: src.Revision}, Collection: in.Collection, Items: []Record{}, Authority: AuthorityDataOnly}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		out.NextCursor = rows[len(rows)-1].ID
	}
	for _, r := range rows {
		if in.Collection == colMemories {
			r.ContentHash = ContentHash(recordContent(r))
		}
		out.Items = append(out.Items, r)
	}
	return out, nil
}
