package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// MemoryState records observed lifecycle facts without retaining another body.
type MemoryState struct {
	Withheld    bool       `json:"withheld,omitempty" bson:"withheld,omitempty"`
	ObservedAt  time.Time  `json:"observed_at" bson:"observed_at"`
	Revision    int64      `json:"revision" bson:"revision"`
	Status      string     `json:"status" bson:"status"`
	Approved    bool       `json:"approved" bson:"approved"`
	ContentHash string     `json:"content_hash" bson:"content_hash"`
	EffectiveAt *time.Time `json:"effective_at,omitempty" bson:"effective_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty" bson:"expires_at,omitempty"`
	Creation    bool       `json:"creation,omitempty" bson:"creation,omitempty"`
}

func memoryState(r Record, now time.Time) MemoryState {
	return MemoryState{Withheld: r.PrivacyQuarantined || r.ContentErased, ObservedAt: now.UTC().Truncate(time.Millisecond), Revision: r.Revision, Status: r.Status, Approved: r.ApprovedBy != "", ContentHash: ContentHash(recordContent(r)), EffectiveAt: r.EffectiveAt, ExpiresAt: r.ExpiresAt}
}
func recordMemoryState(col string, old *Record, r *Record, now time.Time) error {
	if col != colMemories {
		return nil
	}
	state := memoryState(*r, now)
	if old == nil {
		state.Creation = true
		r.MemoryTimeline = []MemoryState{state}
		return nil
	}
	r.MemoryTimeline = append([]MemoryState(nil), old.MemoryTimeline...)
	if len(r.MemoryTimeline) > 0 {
		if state.ObservedAt.Before(r.MemoryTimeline[len(r.MemoryTimeline)-1].ObservedAt) {
			return ErrUnavailable
		}
		a, b := memoryState(*old, now), state
		a.Revision = b.Revision
		if structuredText(a) == structuredText(b) {
			return nil
		}
	}
	r.MemoryTimeline = append(r.MemoryTimeline, state)
	return nil
}
func memoryAt(r Record, at time.Time) (*MemoryState, string) {
	if len(r.MemoryTimeline) == 0 {
		return nil, "history_not_recorded"
	}
	var found *MemoryState
	for i := range r.MemoryTimeline {
		v := &r.MemoryTimeline[i]
		if v.ObservedAt.After(at) {
			break
		}
		found = v
	}
	if found == nil {
		if r.MemoryTimeline[0].Creation {
			return nil, ""
		}
		return nil, "history_not_recorded"
	}
	if found.Withheld || found.Status != "active" || !found.Approved || found.EffectiveAt != nil && found.EffectiveAt.After(at) || found.ExpiresAt != nil && !found.ExpiresAt.After(at) {
		return nil, ""
	}
	if found.ContentHash != ContentHash(recordContent(r)) {
		return nil, "content_unavailable"
	}
	return found, ""
}

type MemoryAtInput struct {
	Scope  Scope     `json:"scope"`
	AsOf   time.Time `json:"as_of"`
	Query  string    `json:"q,omitempty"`
	Kind   string    `json:"kind,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	Cursor string    `json:"cursor,omitempty"`
}
type MemoryAtHit struct {
	ID                 string     `json:"id"`
	Text               string     `json:"text"`
	Summary            string     `json:"summary,omitempty"`
	Kind               string     `json:"kind"`
	ObservedAt         time.Time  `json:"observed_at"`
	HistoricalRevision int64      `json:"historical_revision"`
	CurrentRevision    int64      `json:"current_revision"`
	EffectiveAt        *time.Time `json:"effective_at,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}
type MemoryAtUnknown struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}
type MemoryAtPage struct {
	AsOf       time.Time         `json:"as_of"`
	Items      []MemoryAtHit     `json:"items"`
	Unknown    []MemoryAtUnknown `json:"unknown"`
	NextCursor string            `json:"next_cursor"`
	Authority  string            `json:"authority"`
}

func memoryAtBinding(in MemoryAtInput) string {
	in.Limit = 0
	in.Cursor = ""
	return ContentHash(structuredText(in))
}
func normalizeMemoryAt(in MemoryAtInput) (MemoryAtInput, MemoryListInput, error) {
	list, _, e := normalizeMemoryList(MemoryListInput{Scope: in.Scope, Query: in.Query, Kind: in.Kind, Limit: in.Limit, IncludeInactive: true})
	if e != nil || in.AsOf.IsZero() || in.AsOf.After(time.Now()) {
		return in, list, ErrInvalid
	}
	in.AsOf = in.AsOf.UTC()
	in.Query = list.Query
	in.Limit = list.Limit
	if in.Cursor != "" {
		if len(in.Cursor) > 2048 {
			return in, list, ErrInvalid
		}
		raw, e := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
		var c memoryCursor
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Binding != memoryAtBinding(in) || structuredText(c) != string(raw) || !validRunLabel(c.ID, 256) || containsSecret(c.ID) {
			return in, list, ErrInvalid
		}
		list.Cursor = memoryListCursor(list, c.ID)
	}
	return in, list, nil
}

// MemoriesAt returns historical data under current authorization, never an execution context.
func (s *Service) MemoriesAt(ctx context.Context, token string, in MemoryAtInput) (MemoryAtPage, error) {
	in, scan, e := normalizeMemoryAt(in)
	if e != nil {
		return MemoryAtPage{}, e
	}
	actor, e := s.authorize(ctx, token, "read:memories", in.Scope, "", structuredText(in))
	if e != nil {
		return MemoryAtPage{}, e
	}
	if s.Repo == nil {
		return MemoryAtPage{}, ErrUnavailable
	}
	rows := []Record{}
	for len(rows) < in.Limit+1 {
		if ctx.Err() != nil {
			return MemoryAtPage{}, ErrUnavailable
		}
		batch, e := s.Repo.ListMemories(ctx, scan)
		if e != nil {
			return MemoryAtPage{}, e
		}
		if len(batch) > in.Limit+1 {
			return MemoryAtPage{}, ErrUnavailable
		}
		_, after, _ := normalizeMemoryList(scan)
		if rows, e = s.memoryAtBatch(ctx, token, in, scan, actor, batch, after, rows); e != nil {
			return MemoryAtPage{}, e
		}
		if len(batch) < in.Limit+1 || len(rows) == in.Limit+1 {
			break
		}
		scan.Cursor = memoryListCursor(scan, batch[len(batch)-1].ID)
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		checked := searchReadTx{Tx: tx}
		for _, r := range rows {
			current, e := tx.Get(ctx, colMemories, in.Scope, r.ID)
			if e != nil {
				return e
			}
			if structuredText(current) != structuredText(r) || structuredText(current.MemoryTimeline) != structuredText(r.MemoryTimeline) || !historySearchable(current) {
				return ErrConflict
			}
			if e = tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); e != nil {
				return e
			}
			if e = checkSources(ctx, checked, current); e != nil {
				return e
			}
			d, e := s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(current))
			if e != nil {
				return e
			}
			if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
				return ErrDenied
			}
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:memory_at", "")
	})
	if e != nil {
		return MemoryAtPage{}, e
	}
	out := MemoryAtPage{AsOf: in.AsOf, Items: []MemoryAtHit{}, Unknown: []MemoryAtUnknown{}, Authority: AuthorityDataOnly}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(structuredText(memoryCursor{rows[len(rows)-1].ID, memoryAtBinding(in)})))
	}
	for _, r := range rows {
		state, reason := memoryAt(r, in.AsOf)
		if reason != "" {
			out.Unknown = append(out.Unknown, MemoryAtUnknown{r.ID, reason})
			continue
		}
		out.Items = append(out.Items, MemoryAtHit{ID: r.ID, Text: r.Text, Summary: r.Summary, Kind: r.Kind, ObservedAt: state.ObservedAt, HistoricalRevision: state.Revision, CurrentRevision: r.Revision, EffectiveAt: state.EffectiveAt, ExpiresAt: state.ExpiresAt})
	}
	return out, nil
}

// memoryAtBatch appends the batch rows readable as of in.AsOf, up to one past the page limit.
func (s *Service) memoryAtBatch(ctx context.Context, token string, in MemoryAtInput, scan MemoryListInput, actor Decision, batch []Record, after string, rows []Record) ([]Record, error) {
	for _, r := range batch {
		if !memoryListMatches(r, scan) || r.ID <= after || r.Revision < 1 || !validRunLabel(r.ID, 256) || containsSecret(r.ID) {
			return rows, ErrUnavailable
		}
		after = r.ID
		if !historySearchable(r) {
			continue
		}
		d, e := s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r))
		if errors.Is(e, ErrDenied) {
			continue
		}
		if e != nil {
			return rows, e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return rows, ErrDenied
		}
		state, unknown := memoryAt(r, in.AsOf)
		if state == nil && unknown == "" {
			continue
		}
		rows = append(rows, r)
		if len(rows) == in.Limit+1 {
			break
		}
	}
	return rows, nil
}
