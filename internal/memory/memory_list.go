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
	FoldQuery           bool
	Limit               int
	Cursor              string
	// ProjectKey (ST-824), when set, lists only what recall would show for that project
	// (empty key: personal and general). Unset lists every reach, for management screens.
	ProjectKey *string
	// UpdatedSince lists only rows changed after this time, so a screen can refresh without reading everything again.
	UpdatedSince *time.Time
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
		if len(v) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "q", "kind", "status", "include_inactive", "limit", "cursor", "project_key", "updated_since", "ignore_case"}, k) {
			return in, ErrInvalid
		}
	}
	in = MemoryListInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Query: q.Get("q"), Kind: q.Get("kind"), Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: 20}
	for _, key := range []string{"q", "kind", "status"} {
		if v, ok := q[key]; ok && v[0] == "" {
			return in, ErrInvalid
		}
	}
	if v, ok := q["ignore_case"]; ok {
		if v[0] != "true" && v[0] != "false" {
			return in, ErrInvalid
		}
		in.FoldQuery = v[0] == "true"
	}
	if v, ok := q["project_key"]; ok {
		in.ProjectKey = &v[0]
	}
	if v, ok := q["updated_since"]; ok {
		t, e := time.Parse(time.RFC3339Nano, v[0])
		if e != nil {
			return in, ErrInvalid
		}
		in.UpdatedSince = &t
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
func memoryListTextMatches(r Record, in MemoryListInput) bool {
	if in.FoldQuery {
		return strings.Contains(strings.ToLower(r.Text+" "+r.Summary), strings.ToLower(in.Query))
	}
	return strings.Contains(r.Text, in.Query)
}
func memoryListMatches(r Record, in MemoryListInput) bool {
	return r.Scope == in.Scope && !r.ContentErased && !r.PrivacyQuarantined && (in.IncludeInactive || live(r)) && (in.Status == "" || r.Status == in.Status) && (in.Kind == "" || r.Kind == in.Kind) && memoryListTextMatches(r, in) &&
		(in.ProjectKey == nil || reachVisible(memoryReach(r), r.ReachProject, *in.ProjectKey)) && (in.UpdatedSince == nil || r.UpdatedAt.After(*in.UpdatedSince))
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
	read := s.itemAuthorizer(ctx, token, "read:memories", in.Scope)
	// shortcut: denied rows may require a full scoped scan.
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
			d, e := read(r.ID, structuredText(r))
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
	e = s.readTransaction(ctx, func(ctx context.Context, tx Tx) error {
		if locker, ok := tx.(readLocker); ok && len(rows) > 0 {
			return listReadLocked(ctx, tx, locker, in, rows, actor.PrincipalID)
		}
		for _, r := range rows {
			current, e := tx.Get(ctx, colMemories, in.Scope, r.ID)
			if e != nil {
				return e
			}
			if !memoryListMatches(current, in) || current.Revision != r.Revision || structuredText(current) != structuredText(r) {
				return ErrConflict
			}
			if e = tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); e != nil {
				return e
			}
		}
		ids, e := listSourceIDs(ctx, tx, in.Scope, rows)
		if e != nil {
			return e
		}
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

// readTransaction uses the repository's lighter read transaction when it has one (#173).
func (s *Service) readTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r, ok := s.Repo.(interface {
		ReadTransaction(context.Context, func(context.Context, Tx) error) error
	}); ok {
		return r.ReadTransaction(ctx, fn)
	}
	return s.Repo.Transaction(ctx, fn)
}

// listSourceIDs checks every listed memory's sources with one metadata read per 64 sources
// instead of one per memory (#174). Rows are unchanged since the caller compared them.
func listSourceIDs(ctx context.Context, tx Tx, scope Scope, rows []Record) ([]string, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	refs := []SourceRef{}
	for _, r := range rows {
		if len(r.Sources) == 0 || len(r.Sources) > 64 {
			return nil, ErrInvalid
		}
		refs = append(refs, r.Sources...)
	}
	metadata, e := sourceMetadataBatch(ctx, tx, scope, refs)
	if e != nil {
		return nil, e
	}
	ids := make([]string, 0, len(metadata))
	for _, ref := range metadata {
		ids = append(ids, ref.ID)
	}
	slices.Sort(ids)
	return ids, nil
}

// listLockSources share-locks every listed memory's sources, then checks their spans with one
// metadata read per 64 sources instead of one per memory (#174). Locking first means the spans
// are checked against the same source versions the lock holds. Rows are unchanged since the
// caller compared them.
func listLockSources(ctx context.Context, tx Tx, locker readLocker, scope Scope, rows []Record) error {
	refs := []SourceRef{}
	seen := map[string]bool{}
	ids := []string{}
	for _, r := range rows {
		if len(r.Sources) == 0 || len(r.Sources) > 64 {
			return ErrInvalid
		}
		refs = append(refs, r.Sources...)
		for _, ref := range r.Sources {
			if ref.SourceID != "" && !seen[ref.SourceID] {
				seen[ref.SourceID] = true
				ids = append(ids, ref.SourceID)
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	slices.Sort(ids)
	locked := map[string]Record{}
	for start := 0; start < len(ids); start += 500 {
		part, e := locker.LockForRead(ctx, scope, "sources", ids[start:min(start+500, len(ids))])
		if e != nil {
			return e
		}
		maps.Copy(locked, part)
	}
	for _, id := range ids {
		r, ok := locked[id]
		if !ok || r.PrivacyQuarantined || r.Status != "active" || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now())) {
			return ErrConflict
		}
	}
	_, e := sourceMetadataBatch(ctx, tx, scope, refs)
	return e
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
	for _, r := range rows {
		c, ok := current[r.ID]
		if !ok || !memoryListMatches(c, in) || c.Revision != r.Revision || structuredText(c) != structuredText(r) {
			return ErrConflict
		}
	}
	if e = listLockSources(ctx, tx, locker, in.Scope, rows); e != nil {
		return e
	}
	return audit(ctx, tx, in.Scope, actor, "read:memory_list", "")
}
