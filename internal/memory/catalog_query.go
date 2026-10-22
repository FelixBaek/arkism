package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CatalogQueryInput describes a management view, never an authorization grant.
// Category rules and kept IDs are presentation preferences supplied by the user.
type CatalogQueryInput struct {
	Scope         Scope             `json:"scope"`
	Query         string            `json:"q,omitempty"`
	State         string            `json:"state,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Repository    string            `json:"repository,omitempty"`
	Model         string            `json:"model,omitempty"`
	Category      string            `json:"category,omitempty"`
	Sort          string            `json:"sort,omitempty"`
	Direction     int               `json:"direction,omitempty"`
	Limit         int               `json:"limit,omitempty"`
	Cursor        string            `json:"cursor,omitempty"`
	Categories    []CatalogCategory `json:"categories,omitempty"`
	Agents        []string          `json:"agents,omitempty"`
	KeptIDs       []string          `json:"kept_ids,omitempty"`
	CollectUnused *bool             `json:"collect_unused,omitempty"`
	Format        string            `json:"format,omitempty"`
}
type CatalogCategory struct {
	ID    string     `json:"id"`
	Rules [][]string `json:"rules"`
}
type CatalogFacets struct {
	Kinds        map[string]int `json:"kinds"`
	Repositories map[string]int `json:"repositories"`
	Categories   map[string]int `json:"categories"`
	States       map[string]int `json:"states"`
}

func newCatalogFacets() CatalogFacets {
	return CatalogFacets{Kinds: map[string]int{}, Repositories: map[string]int{}, Categories: map[string]int{}, States: map[string]int{}}
}

type MemoryCatalogPage struct {
	Items      []Record      `json:"items"`
	Total      int           `json:"total"`
	NextCursor string        `json:"next_cursor"`
	Authority  string        `json:"authority"`
	Facets     CatalogFacets `json:"facets"`
}
type SkillCatalogPage struct {
	Items      []SkillListItem `json:"items"`
	Total      int             `json:"total"`
	NextCursor string          `json:"next_cursor"`
	Authority  string          `json:"authority"`
	Facets     CatalogFacets   `json:"facets"`
}
type catalogCursor struct {
	Binding  string `json:"binding"`
	Snapshot string `json:"snapshot"`
	Offset   int    `json:"offset"`
}

func catalogText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0 && !containsSecret(value)
}
func normalizeCatalog(in CatalogQueryInput, domain string) (CatalogQueryInput, error) {
	in.Query = strings.TrimSpace(in.Query)
	if !validScope(in.Scope) || !catalogText(in.Query, 256) || in.Limit < 0 || in.Limit > 100 || in.Direction < -1 || in.Direction > 1 || len(in.Cursor) > 2048 {
		return in, ErrInvalid
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	sorts := map[string][]string{"memories": {"recent", "old", "used"}, "candidates": {"risk", "recent", "old"}, "skills": {"used", "recent", "name", "updated", "quality", "security", "agents"}, "knowledge": {"recent", "name", "chunks"}}
	states := map[string][]string{"memories": {"active", "idle", "provenance"}, "candidates": {"candidate", "conflicted", "all"}, "skills": {"all", "trash", "unused", "plugin", "update", "missing", "caution"}, "knowledge": {"all", "검토 대기", "사용 중", "실패"}}
	if len(sorts[domain]) == 0 || len(states[domain]) == 0 {
		return in, ErrInvalid
	}
	if in.Sort == "" {
		in.Sort = sorts[domain][0]
	}
	if in.State == "" {
		in.State = states[domain][0]
	}
	if !slices.Contains(sorts[domain], in.Sort) || !slices.Contains(states[domain], in.State) {
		return in, ErrInvalid
	}
	for _, value := range []string{in.Repository, in.Model, in.Category, in.Format} {
		if !catalogText(value, 256) {
			return in, ErrInvalid
		}
	}
	if in.Kind != "" && !validCreateKind("memory_candidates", in.Kind) {
		return in, ErrInvalid
	}
	if err := validateCatalogPreferences(in); err != nil {
		return in, err
	}
	return in, nil
}
func validateCatalogPreferences(in CatalogQueryInput) error {
	if len(in.Categories) > 100 || len(in.Agents) > 32 || len(in.KeptIDs) > 2048 {
		return ErrInvalid
	}
	for _, id := range append(slices.Clone(in.Agents), in.KeptIDs...) {
		if !validRunLabel(id, 256) || containsSecret(id) {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, category := range in.Categories {
		if !validRunLabel(category.ID, 128) || seen[category.ID] || len(category.Rules) > 64 {
			return ErrInvalid
		}
		seen[category.ID] = true
		if err := validateCatalogRules(category.Rules); err != nil {
			return err
		}
	}
	return nil
}
func validateCatalogRules(rules [][]string) error {
	for _, rule := range rules {
		if len(rule) != 2 || !slices.Contains([]string{"name", "path", "desc"}, rule[0]) || !catalogText(rule[1], 256) {
			return ErrInvalid
		}
	}
	return nil
}
func catalogBinding(in CatalogQueryInput, domain, principal string) string {
	in.Cursor = ""
	in.Limit = 0
	return ContentHash(structuredText([]any{domain, principal, in}))
}

// decodeCatalogCursor rejects a malformed or foreign cursor; callers run it before reading rows
// so a bad cursor costs no scan (#176). catalogWindow then checks it against the snapshot.
func decodeCatalogCursor(in CatalogQueryInput, binding string) (catalogCursor, error) {
	var cursor catalogCursor
	if in.Cursor == "" {
		return cursor, nil
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	if err != nil || json.Unmarshal(data, &cursor) != nil || structuredText(cursor) != string(data) || cursor.Binding != binding || cursor.Offset < 0 {
		return cursor, ErrInvalid
	}
	return cursor, nil
}
func catalogWindow[T any](items []T, in CatalogQueryInput, binding string) ([]T, string, error) {
	cursor, err := decodeCatalogCursor(in, binding)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(items); err != nil {
		return nil, "", ErrUnavailable
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	offset := 0
	if in.Cursor != "" {
		if cursor.Snapshot != digest || cursor.Offset > len(items) {
			return nil, "", ErrConflict
		}
		offset = cursor.Offset
	}
	end := min(offset+in.Limit, len(items))
	next := ""
	if end < len(items) {
		next = base64.RawURLEncoding.EncodeToString([]byte(structuredText(catalogCursor{binding, digest, end})))
	}
	return items[offset:end], next, nil
}
func collectCatalogPages[T any](ctx context.Context, read func(string) ([]T, string, error)) ([]T, error) {
	all := []T{}
	cursor := ""
	seen := map[string]bool{}
	for {
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		items, next, err := read(cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		if next == cursor || seen[next] {
			return nil, ErrUnavailable
		}
		seen[next] = true
		cursor = next
	}
}

// Bind direct service calls as strictly as HTTP, including across internal pages.
func (s *Service) catalogActor(ctx context.Context, token, action string, scope Scope) (context.Context, Decision, error) {
	actor, err := s.authorize(ctx, token, action, scope, "", "")
	if err != nil {
		return ctx, actor, err
	}
	if _, ok := ctx.Value(httpActorKey{}).(*httpActor); !ok {
		ctx = context.WithValue(ctx, httpActorKey{}, &httpActor{principal: actor.PrincipalID, role: actor.Role})
	}
	return ctx, actor, nil
}
func (s *Service) catalogTools(ctx context.Context, token string, scope Scope) (*AgentTools, error) {
	record, err := s.AgentTools(ctx, token, scope)
	if catalogActorChanged(ctx) {
		return nil, ErrDenied
	}
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return record.AgentTools, err
}
func catalogActorChanged(ctx context.Context) bool {
	actor, _ := ctx.Value(httpActorKey{}).(*httpActor)
	return actor != nil && actor.changed.Load()
}
func catalogQueryRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/memories/query", func(w http.ResponseWriter, r *http.Request) {
		var in CatalogQueryInput
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, err := s.QueryMemoryCatalog(r.Context(), bearer(r), in, false)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/memory_candidates/query", func(w http.ResponseWriter, r *http.Request) {
		var in CatalogQueryInput
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, err := s.QueryMemoryCatalog(r.Context(), bearer(r), in, true)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/skills/query", func(w http.ResponseWriter, r *http.Request) {
		var in CatalogQueryInput
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, err := s.QuerySkillCatalog(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/knowledge/sources/query", func(w http.ResponseWriter, r *http.Request) {
		var in CatalogQueryInput
		if !decodeLimit(w, r, &in, 1<<20) {
			return
		}
		out, err := s.QueryKnowledgeCatalog(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
