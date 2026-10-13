package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type SourceListInput struct {
	Query         string     `json:"q,omitempty"`
	Scope         Scope      `json:"scope"`
	Kind          string     `json:"kind,omitempty"`
	OriginProduct string     `json:"origin_product,omitempty"`
	CollectedFrom *time.Time `json:"collected_from,omitempty"`
	CollectedTo   *time.Time `json:"collected_to,omitempty"`
	Limit         int        `json:"limit"`
	Cursor        string     `json:"cursor,omitempty"`
}
type SourceListItem struct {
	ID            string    `json:"id"`
	Scope         Scope     `json:"scope"`
	Kind          string    `json:"kind"`
	FirstLine     string    `json:"first_line"`
	OriginProduct string    `json:"origin_product"`
	CollectedAt   time.Time `json:"collected_at"`
	Sensitivity   string    `json:"sensitivity"`
}
type SourcePage struct {
	Items      []SourceListItem `json:"items"`
	NextCursor string           `json:"next_cursor"`
	Authority  string           `json:"authority"`
}
type sourceCursor struct {
	CollectedAt time.Time `json:"collected_at"`
	ID          string    `json:"id"`
	Binding     string    `json:"binding"`
}

func sourceListBinding(in SourceListInput) string {
	in.Cursor, in.Limit = "", 0
	return ContentHash(structuredText(in))
}
func normalizeSourceList(in SourceListInput) (SourceListInput, *sourceCursor, error) {
	if len(in.Query) > 256 || !utf8.ValidString(in.Query) || strings.IndexFunc(in.Query, unicode.IsControl) >= 0 || containsSecret(in.Query) {
		return in, nil, ErrInvalid
	}
	if in.Query != "" && strings.TrimSpace(in.Query) == "" {
		return in, nil, ErrInvalid
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.Limit < 1 || in.Limit > 100 || in.Kind != "" && !validCreateKind("sources", in.Kind) || in.OriginProduct != "" && (!originProductPattern.MatchString(in.OriginProduct) || containsSecret(in.OriginProduct)) {
		return in, nil, ErrInvalid
	}
	for _, field := range []**time.Time{&in.CollectedFrom, &in.CollectedTo} {
		if *field != nil {
			at := (**field).UTC().Truncate(time.Millisecond)
			if at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
				return in, nil, ErrInvalid
			}
			*field = &at
		}
	}
	if in.CollectedFrom != nil && in.CollectedTo != nil && !in.CollectedFrom.Before(*in.CollectedTo) {
		return in, nil, ErrInvalid
	}
	if in.Cursor == "" {
		return in, nil, nil
	}
	if len(in.Cursor) > 2048 || strings.IndexFunc(in.Cursor, unicode.IsSpace) >= 0 {
		return in, nil, ErrInvalid
	}
	data, e := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	if e != nil {
		return in, nil, ErrInvalid
	}
	var after sourceCursor
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&after) != nil || dec.Decode(&extra) != io.EOF || after.Binding != sourceListBinding(in) || after.ID == "" || len(after.ID) > 256 || strings.IndexFunc(after.ID, unicode.IsControl) >= 0 || containsSecret(after.ID) || after.CollectedAt.IsZero() || !after.CollectedAt.Equal(after.CollectedAt.Truncate(time.Millisecond)) {
		return in, nil, ErrInvalid
	}
	return in, &after, nil
}
func encodeSourceCursor(in SourceListInput, r Record) (string, error) {
	data, e := json.Marshal(sourceCursor{r.CollectedAt.UTC(), r.ID, sourceListBinding(in)})
	return base64.RawURLEncoding.EncodeToString(data), e
}
func sourceListItem(r Record) SourceListItem {
	text := []rune(r.Text)
	if len(text) > 160 {
		text = text[:160]
	}
	line, _, _ := strings.Cut(string(text), "\n")
	return SourceListItem{r.ID, r.Scope, r.Kind, strings.TrimSuffix(line, "\r"), r.OriginProduct, r.CollectedAt.UTC(), r.Sensitivity}
}
func (s *Service) ListSources(ctx context.Context, token string, in SourceListInput) (SourcePage, error) {
	in, _, e := normalizeSourceList(in)
	if e != nil {
		return SourcePage{}, e
	}
	actor, e := s.authorize(ctx, token, "read:sources", in.Scope, "", in.Query)
	if e != nil {
		return SourcePage{}, e
	}
	if s.Repo == nil {
		return SourcePage{}, ErrUnavailable
	}
	var rows []Record
	scan := in
	for {
		if ctx.Err() != nil {
			return SourcePage{}, ErrUnavailable
		}
		batch, err := s.Repo.ListSources(ctx, scan)
		if err != nil {
			return SourcePage{}, err
		}
		if len(batch) > in.Limit+1 {
			return SourcePage{}, ErrUnavailable
		}
		for _, r := range batch {
			if r.Scope != in.Scope || r.CollectedAt == nil || r.CollectedAt.IsZero() || !live(r) {
				return SourcePage{}, ErrConflict
			}
			d, err := s.authorize(ctx, token, "read:sources", in.Scope, r.ID, structuredText(sourceListItem(r)))
			// Search must not expose denied body matches through a 403-vs-200 oracle.
			if in.Query != "" && errors.Is(err, ErrDenied) {
				continue
			}
			if err != nil {
				return SourcePage{}, err
			}
			if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
				return SourcePage{}, ErrDenied
			}
			rows = append(rows, r)
			if len(rows) == in.Limit+1 {
				break
			}
		}
		if in.Query == "" || len(rows) == in.Limit+1 || len(batch) < in.Limit+1 {
			break
		}
		// shortcut: scan matching batches until the authorized page is full; the request deadline bounds policy/scan cost.
		scan.Cursor, err = encodeSourceCursor(in, batch[len(batch)-1])
		if err != nil {
			return SourcePage{}, ErrUnavailable
		}
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, r := range rows {
			current, err := tx.Get(ctx, "sources", in.Scope, r.ID)
			if err != nil {
				return err
			}
			if !live(current) || current.Revision != r.Revision || current.CollectedAt == nil || sourceListItem(current) != sourceListItem(r) || !strings.Contains(current.Text, in.Query) {
				return ErrConflict
			}
			if err = tx.FenceSource(ctx, in.Scope, r.ID); err != nil {
				return err
			}
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:sources:list", "")
	})
	if e != nil {
		return SourcePage{}, e
	}
	out := SourcePage{Items: []SourceListItem{}, Authority: "data_only_untrusted"}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		var err error
		out.NextCursor, err = encodeSourceCursor(in, last)
		if err != nil {
			return SourcePage{}, ErrUnavailable
		}
	}
	for _, r := range rows {
		out.Items = append(out.Items, sourceListItem(r))
	}
	return out, nil
}

// shortcut: literal substring may scan source bodies; add a source index after measuring real-volume latency.

func parseSourceListQuery(raw string) (SourceListInput, error) {
	var in SourceListInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "kind", "origin_product", "collected_from", "collected_to", "limit", "cursor", "q"}, key) {
			return in, ErrInvalid
		}
	}
	if q.Has("q") && strings.TrimSpace(q.Get("q")) == "" {
		return in, ErrInvalid
	}
	in = SourceListInput{Query: q.Get("q"), Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Kind: q.Get("kind"), OriginProduct: q.Get("origin_product"), Cursor: q.Get("cursor"), Limit: 20}
	if values, ok := q["limit"]; ok {
		in.Limit, e = strconv.Atoi(values[0])
		if e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	for _, field := range []struct {
		key    string
		target **time.Time
	}{{"collected_from", &in.CollectedFrom}, {"collected_to", &in.CollectedTo}} {
		if value, ok := q[field.key]; ok {
			at, e := time.Parse(time.RFC3339Nano, value[0])
			if e != nil {
				return in, ErrInvalid
			}
			*field.target = &at
		}
	}
	return in, nil
}
