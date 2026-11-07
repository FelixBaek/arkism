package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
)

type directivePageCursor struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Binding  string `json:"binding"`
}
type directivePageLister interface {
	DirectiveRecordsAfter(context.Context, Scope, int, string, int) ([]Record, error)
}

func directiveListBinding(scope Scope) string {
	return ContentHash("directive-page-v1:" + structuredText(projectScope(scope)))
}
func decodeDirectiveCursor(raw string, scope Scope) (directivePageCursor, error) {
	c := directivePageCursor{Priority: directiveMaxPriority + 1}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 2048 {
		return c, ErrInvalid
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(raw)
	if e != nil || json.Unmarshal(b, &c) != nil || c.ID == "" || c.Priority < 0 || c.Priority > directiveMaxPriority || c.Binding != directiveListBinding(scope) {
		return c, ErrInvalid
	}
	return c, nil
}
func encodeDirectiveCursor(scope Scope, r Record) string {
	return base64.RawURLEncoding.EncodeToString([]byte(structuredText(directivePageCursor{r.ID, r.Directive.Priority, directiveListBinding(scope)})))
}
func directiveLess(a, b Record) int {
	if a.Directive.Priority != b.Directive.Priority {
		return b.Directive.Priority - a.Directive.Priority
	}
	return strings.Compare(a.ID, b.ID)
}
func (p *Postgres) DirectiveRecordsAfter(ctx context.Context, scope Scope, priority int, id string, limit int) ([]Record, error) {
	if limit < 1 || limit > directiveMaxPerScope+1 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colDirectives, projectScope(scope), `doc->>'kind'='directive' AND doc->'directive' IS NOT NULL AND ((doc->'directive'->>'priority')::int<$6 OR ((doc->'directive'->>'priority')::int=$6 AND id>$7))`, []any{priority, id}, `(doc->'directive'->>'priority')::int DESC,id ASC`, limit)
}
func (s *Service) directivePage(ctx context.Context, scope Scope, c directivePageCursor, limit int) ([]Record, error) {
	if pager, ok := s.Repo.(directivePageLister); ok {
		return pager.DirectiveRecordsAfter(ctx, scope, c.Priority, c.ID, limit)
	}
	rows, e := s.Repo.ListRecords(ctx, projectScope(scope), colDirectives, 1000)
	if e != nil {
		return nil, e
	}
	if len(rows) >= 1000 {
		return nil, ErrUnavailable
	}
	rows = slices.DeleteFunc(rows, func(r Record) bool {
		return r.Directive == nil || r.Kind != directiveKind || r.Directive.Priority > c.Priority || r.Directive.Priority == c.Priority && r.ID <= c.ID
	})
	slices.SortFunc(rows, directiveLess)
	return rows[:min(limit, len(rows))], nil
}
