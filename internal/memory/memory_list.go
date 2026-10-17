package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type MemoryListInput struct {
	Scope               Scope
	Query, Kind, Status string
	IncludeInactive     bool
	Limit               int
	Cursor              string
	// ProjectKey (ST-824), when set, lists only what recall would show for that project
	// (empty key: personal and general). Unset lists every reach, for management screens.
	ProjectKey *string
}
type MemoryPage struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor"`
	Authority  string   `json:"authority"`
}
type memoryCursor struct{ ID, Binding string }

func memoryListBinding(in MemoryListInput) string {
	in.Cursor, in.Limit = "", 0
	return ContentHash(structuredText(in))
}
func normalizeMemoryList(in MemoryListInput) (MemoryListInput, string, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.Limit < 1 || in.Limit > 100 || len(in.Query) > 256 || !utf8.ValidString(in.Query) || strings.IndexFunc(in.Query, unicode.IsControl) >= 0 || containsSecret(in.Query) || in.Query != "" && strings.TrimSpace(in.Query) == "" || in.Kind != "" && !validCreateKind("memory_candidates", in.Kind) || in.Status != "" && !slices.Contains([]string{"active", "superseded", "conflicted", "rejected", "expired"}, in.Status) || !in.IncludeInactive && in.Status != "" && in.Status != "active" || in.ProjectKey != nil && !validProjectKey(*in.ProjectKey) {
		return in, "", ErrInvalid
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Cursor == "" {
		return in, "", nil
	}
	if len(in.Cursor) > 2048 {
		return in, "", ErrInvalid
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	var c memoryCursor
	if e != nil || json.Unmarshal(raw, &c) != nil || c.Binding != memoryListBinding(in) || !validRunLabel(c.ID, 256) || containsSecret(c.ID) {
		return in, "", ErrInvalid
	}
	// Accept only our canonical two-field representation (no unknown or duplicate fields).
	if structuredText(c) != string(raw) {
		return in, "", ErrInvalid
	}
	return in, c.ID, nil
}
func memoryListCursor(in MemoryListInput, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(structuredText(memoryCursor{id, memoryListBinding(in)})))
}
func parseMemoryListQuery(raw string) (MemoryListInput, error) {
	var in MemoryListInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "q", "kind", "status", "include_inactive", "limit", "cursor", "project_key"}, k) {
			return in, ErrInvalid
		}
	}
	in = MemoryListInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Query: q.Get("q"), Kind: q.Get("kind"), Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: 20}
	for _, key := range []string{"q", "kind", "status"} {
		if v, ok := q[key]; ok && v[0] == "" {
			return in, ErrInvalid
		}
	}
	if v, ok := q["project_key"]; ok {
		in.ProjectKey = &v[0]
	}
	if v, ok := q["include_inactive"]; ok {
		if v[0] != "true" && v[0] != "false" {
			return in, ErrInvalid
		}
		in.IncludeInactive = v[0] == "true"
	}
	if v, ok := q["limit"]; ok {
		in.Limit, e = strconv.Atoi(v[0])
		if e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	in, _, e = normalizeMemoryList(in)
	return in, e
}
func memoryListMatches(r Record, in MemoryListInput) bool {
	return r.Scope == in.Scope && !r.ContentErased && !r.PrivacyQuarantined && (in.IncludeInactive || live(r)) && (in.Status == "" || r.Status == in.Status) && (in.Kind == "" || r.Kind == in.Kind) && strings.Contains(r.Text, in.Query) &&
		(in.ProjectKey == nil || reachVisible(memoryReach(r), r.ReachProject, *in.ProjectKey))
}

func (s *Service) ListMemories(ctx context.Context, token string, in MemoryListInput) (MemoryPage, error) {
	in, _, e := normalizeMemoryList(in)
	if e != nil {
		return MemoryPage{}, e
	}
	actor, e := s.authorize(ctx, token, "read:memories", in.Scope, "", in.Query)
	if e != nil {
		return MemoryPage{}, e
	}
	if s.Repo == nil {
		return MemoryPage{}, ErrUnavailable
	}
	rows := []Record{}
	scan := in
	// shortcut: denied rows may require a full scoped scan; batch policy only with equivalent per-row decisions.
	for len(rows) < in.Limit+1 {
		if ctx.Err() != nil {
			return MemoryPage{}, ErrUnavailable
		}
		batch, e := s.Repo.ListMemories(ctx, scan)
		if e != nil {
			return MemoryPage{}, e
		}
		if len(batch) > in.Limit+1 {
			return MemoryPage{}, ErrUnavailable
		}
		_, after, _ := normalizeMemoryList(scan)
		for _, r := range batch {
			if !memoryListMatches(r, in) || r.Revision < 1 || !validRunLabel(r.ID, 256) || containsSecret(r.ID) || r.ID <= after {
				return MemoryPage{}, ErrUnavailable
			}
			after = r.ID
			d, e := s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r))
			if errors.Is(e, ErrDenied) {
				continue
			}
			if e != nil {
				return MemoryPage{}, e
			}
			if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
				return MemoryPage{}, ErrDenied
			}
			rows = append(rows, r)
			if len(rows) == in.Limit+1 {
				break
			}
		}
		if len(batch) < in.Limit+1 || len(rows) == in.Limit+1 {
			break
		}
		scan.Cursor = memoryListCursor(in, batch[len(batch)-1].ID)
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if locker, ok := tx.(readLocker); ok && len(rows) > 0 {
			return listReadLocked(ctx, tx, locker, in, rows, actor.PrincipalID)
		}
		sources := map[string]bool{}
		for _, r := range rows {
			current, e := tx.Get(ctx, colMemories, in.Scope, r.ID)
			if e != nil {
				return e
			}
			if !memoryListMatches(current, in) || current.Revision != r.Revision || structuredText(current) != structuredText(r) {
				return ErrConflict
			}
			metadata, e := sourceMetadata(ctx, tx, in.Scope, current.Sources)
			if e != nil {
				return e
			}
			for _, ref := range metadata {
				sources[ref.ID] = true
			}
			if e = tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); e != nil {
				return e
			}
		}
		ids := make([]string, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if e := tx.FenceSource(ctx, in.Scope, id); e != nil {
				return e
			}
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:memory_list", "")
	})
	if e != nil {
		return MemoryPage{}, e
	}
	out := MemoryPage{Items: []Record{}, Authority: AuthorityDataOnly}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		out.NextCursor = memoryListCursor(in, rows[len(rows)-1].ID)
	}
	for _, r := range rows {
		normalizeMemoryProvenance(&r)
		r.ContentHash = ContentHash(recordContent(r))
		out.Items = append(out.Items, r)
	}
	return out, nil
}

type readLocker interface {
	LockForRead(context.Context, Scope, string, []string) (map[string]Record, error)
}

// One share lock over the listed memories and one over their sources replaces a fence write per
// row; the same checks decide (unchanged row, live sources, not quarantined or expired).
func listReadLocked(ctx context.Context, tx Tx, locker readLocker, in MemoryListInput, rows []Record, actor string) error {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	current, e := locker.LockForRead(ctx, in.Scope, colMemories, ids)
	if e != nil {
		return e
	}
	sources := map[string]bool{}
	for _, r := range rows {
		c, ok := current[r.ID]
		if !ok || !memoryListMatches(c, in) || c.Revision != r.Revision || structuredText(c) != structuredText(r) {
			return ErrConflict
		}
		metadata, e := sourceMetadata(ctx, tx, in.Scope, c.Sources)
		if e != nil {
			return e
		}
		for _, ref := range metadata {
			sources[ref.ID] = true
		}
	}
	if len(sources) > 0 {
		sourceIDs := make([]string, 0, len(sources))
		for id := range sources {
			sourceIDs = append(sourceIDs, id)
		}
		slices.Sort(sourceIDs)
		locked := map[string]Record{}
		for start := 0; start < len(sourceIDs); start += 500 {
			part, e := locker.LockForRead(ctx, in.Scope, "sources", sourceIDs[start:min(start+500, len(sourceIDs))])
			if e != nil {
				return e
			}
			maps.Copy(locked, part)
		}
		for _, id := range sourceIDs {
			r, ok := locked[id]
			if !ok || r.PrivacyQuarantined || r.Status != "active" || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now())) {
				return ErrConflict
			}
		}
	}
	return audit(ctx, tx, in.Scope, actor, "read:memory_list", "")
}
