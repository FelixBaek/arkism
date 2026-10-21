package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ST-812: read back memory_outcome evaluations per memory. Responses carry only
// references, booleans, counts and times — never memory, source or trace text.

const (
	memoryOutcomeKind       = "memory_outcome"
	memoryOutcomeBatchLimit = 100
)

type MemoryOutcomeQuery struct {
	Scope     Scope      `json:"scope"`
	MemoryIDs []string   `json:"memory_ids"`
	Dimension string     `json:"dimension,omitempty"`
	From      *time.Time `json:"from,omitempty"`
	To        *time.Time `json:"to,omitempty"`
	Limit     int        `json:"limit,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
}

func (v *MemoryOutcomeQuery) UnmarshalJSON(data []byte) error {
	type plain MemoryOutcomeQuery
	return decodeComparison(data, (*plain)(v), []string{"scope", "memory_ids"})
}

// MemoryOutcomeCount is the repository aggregate for one memory id.
type MemoryOutcomeCount struct {
	MemoryID string
	Total    int
	Passed   int
	Last     time.Time
}

type MemoryOutcomeEntry struct {
	Evaluation Ref       `json:"evaluation"`
	Memory     Ref       `json:"memory"`
	Trace      *Ref      `json:"trace"`
	Dimension  string    `json:"dimension"`
	Passed     bool      `json:"passed"`
	JudgedAt   time.Time `json:"judged_at"`
}
type MemoryOutcomeSummary struct {
	MemoryID     string     `json:"memory_id"`
	Available    bool       `json:"available"`
	Total        int        `json:"total"`
	Passed       int        `json:"passed"`
	PassRate     *float64   `json:"pass_rate"`
	LastJudgedAt *time.Time `json:"last_judged_at"`
}
type MemoryOutcomePage struct {
	Memory     Ref                  `json:"memory"`
	Summary    MemoryOutcomeSummary `json:"summary"`
	Items      []MemoryOutcomeEntry `json:"items"`
	NextCursor string               `json:"next_cursor"`
	Authority  string               `json:"authority"`
}
type MemoryOutcomeSummaries struct {
	Items     []MemoryOutcomeSummary `json:"items"`
	Authority string                 `json:"authority"`
}

type memoryOutcomeCursor struct {
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
	Binding string    `json:"binding"`
}

func memoryOutcomeBinding(in MemoryOutcomeQuery) string {
	in.Cursor, in.Limit = "", 0
	return ContentHash(structuredText(in))
}
func memoryOutcomeCursorFor(in MemoryOutcomeQuery, r Record) string {
	return base64.RawURLEncoding.EncodeToString([]byte(structuredText(memoryOutcomeCursor{r.CreatedAt, r.ID, memoryOutcomeBinding(in)})))
}

// normalizeMemoryOutcomeQuery is shared by the service and the repository, so
// both enforce the same filters; the returned cursor is nil on the first page.
func normalizeMemoryOutcomeQuery(in MemoryOutcomeQuery, page bool) (MemoryOutcomeQuery, *memoryOutcomeCursor, error) {
	if page && in.Limit == 0 {
		in.Limit = 20
	}
	ids := slices.Clone(in.MemoryIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || len(ids) == 0 || len(ids) != len(in.MemoryIDs) || len(ids) > memoryOutcomeBatchLimit || page && (len(ids) != 1 || in.Limit < 1 || in.Limit > 100) || !page && (in.Limit != 0 || in.Cursor != "") ||
		in.Dimension != "" && in.Dimension != "personal_compliance" && in.Dimension != "work_task" || in.From != nil && in.To != nil && !in.From.Before(*in.To) {
		return in, nil, ErrInvalid
	}
	for _, id := range ids {
		if !validRunLabel(id, 256) || containsSecret(id) {
			return in, nil, ErrInvalid
		}
	}
	in.MemoryIDs = ids
	if in.Cursor == "" {
		return in, nil, nil
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	var c memoryOutcomeCursor
	if len(in.Cursor) > 2048 || e != nil || json.Unmarshal(raw, &c) != nil || c.Binding != memoryOutcomeBinding(in) || !validRunLabel(c.ID, 256) || c.At.IsZero() || structuredText(c) != string(raw) {
		return in, nil, ErrInvalid
	}
	return in, &c, nil
}

func outcomeDimension(r Record) string {
	for _, t := range r.Tags {
		if d, ok := strings.CutPrefix(t, "dimension:"); ok {
			return d
		}
	}
	return ""
}
func memoryOutcomeMatches(r Record, in MemoryOutcomeQuery) bool {
	d := outcomeDimension(r)
	return r.Scope == in.Scope && r.Kind == memoryOutcomeKind && r.Status == "active" && live(r) && !r.ContentErased && !r.PrivacyQuarantined && r.Target != nil && r.Target.Collection == colMemories && slices.Contains(in.MemoryIDs, r.Target.ID) &&
		(d == "personal_compliance" || d == "work_task") && (in.Dimension == "" || d == in.Dimension) && (in.From == nil || !r.CreatedAt.Before(*in.From)) && (in.To == nil || r.CreatedAt.Before(*in.To))
}

func parseMemoryOutcomeQuery(raw, id string) (MemoryOutcomeQuery, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return MemoryOutcomeQuery{}, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 || v[0] == "" && k != "user_id" && k != "agent_id" || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "dimension", "from", "to", "limit", "cursor"}, k) {
			return MemoryOutcomeQuery{}, ErrInvalid
		}
	}
	in := MemoryOutcomeQuery{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, MemoryIDs: []string{id}, Dimension: q.Get("dimension"), Cursor: q.Get("cursor")}
	for key, dst := range map[string]**time.Time{"from": &in.From, "to": &in.To} {
		if v, ok := q[key]; ok {
			t, e := time.Parse(time.RFC3339, v[0])
			if e != nil {
				return in, ErrInvalid
			}
			t = t.UTC()
			*dst = &t
		}
	}
	if v, ok := q["limit"]; ok {
		if in.Limit, e = strconv.Atoi(v[0]); e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	return in, nil
}

func memoryOutcomeReadRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/memories/{id}/outcomes", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryOutcomeQuery(r.URL.RawQuery, r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListMemoryOutcomes(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/memory-outcomes/summary", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryOutcomeQuery
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SummarizeMemoryOutcomes(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

// outcomeReader binds read:memories (per id) and read:evaluations to one principal.
func (s *Service) outcomeReader(ctx context.Context, token string, scope Scope) (Decision, error) {
	if s.Repo == nil {
		return Decision{}, ErrUnavailable
	}
	who, e := s.authorize(ctx, token, "read:memories", scope, "", "")
	if e != nil {
		return Decision{}, e
	}
	d, e := s.authorize(ctx, token, "read:evaluations", scope, "", "")
	if e != nil {
		return Decision{}, e
	}
	if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
		return Decision{}, ErrDenied
	}
	return who, nil
}
func (s *Service) readableOutcomeMemory(ctx context.Context, token string, who Decision, scope Scope, id string) (Record, error) {
	d, e := s.authorize(ctx, token, "read:memories", scope, id, "")
	if e != nil {
		return Record{}, e
	}
	if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
		return Record{}, ErrDenied
	}
	m, e := s.Repo.Get(ctx, colMemories, scope, id)
	if e != nil {
		return Record{}, e
	}
	if m.Scope != scope || m.ContentErased || m.PrivacyQuarantined || m.Revision < 1 {
		return Record{}, ErrNotFound
	}
	return m, nil
}

func (s *Service) memoryOutcomeSummaries(ctx context.Context, in MemoryOutcomeQuery, available map[string]bool) ([]MemoryOutcomeSummary, error) {
	in.Limit, in.Cursor = 0, ""
	counts, e := s.Repo.MemoryOutcomeCounts(ctx, in)
	if e != nil {
		return nil, e
	}
	byID := map[string]MemoryOutcomeCount{}
	for _, c := range counts {
		if !slices.Contains(in.MemoryIDs, c.MemoryID) || c.Total < 1 || c.Passed < 0 || c.Passed > c.Total || c.Last.IsZero() {
			return nil, ErrUnavailable
		}
		if _, dup := byID[c.MemoryID]; dup {
			return nil, ErrUnavailable
		}
		byID[c.MemoryID] = c
	}
	out := make([]MemoryOutcomeSummary, 0, len(in.MemoryIDs))
	for _, id := range in.MemoryIDs {
		sum := MemoryOutcomeSummary{MemoryID: id, Available: available[id]}
		if c, ok := byID[id]; ok && sum.Available {
			rate, last := float64(c.Passed)/float64(c.Total), c.Last.UTC()
			sum.Total, sum.Passed, sum.PassRate, sum.LastJudgedAt = c.Total, c.Passed, &rate, &last
		}
		out = append(out, sum)
	}
	return out, nil
}

// ListMemoryOutcomes pages one memory's verdicts, newest first, with a summary
// over the same filters (all revisions of the memory id).
func (s *Service) ListMemoryOutcomes(ctx context.Context, token string, in MemoryOutcomeQuery) (MemoryOutcomePage, error) {
	in, _, e := normalizeMemoryOutcomeQuery(in, true)
	if e != nil {
		return MemoryOutcomePage{}, e
	}
	who, e := s.outcomeReader(ctx, token, in.Scope)
	if e != nil {
		return MemoryOutcomePage{}, e
	}
	id := in.MemoryIDs[0]
	m, e := s.readableOutcomeMemory(ctx, token, who, in.Scope, id)
	if e != nil {
		return MemoryOutcomePage{}, e
	}
	rows, e := s.Repo.MemoryOutcomes(ctx, in)
	if e != nil {
		return MemoryOutcomePage{}, e
	}
	if len(rows) > in.Limit+1 {
		return MemoryOutcomePage{}, ErrUnavailable
	}
	out := MemoryOutcomePage{Memory: Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Items: []MemoryOutcomeEntry{}, Authority: AuthorityDataOnly}
	for i, r := range rows {
		if !memoryOutcomeMatches(r, in) || r.Revision < 1 || i > 0 && !(r.CreatedAt.Before(rows[i-1].CreatedAt) || r.CreatedAt.Equal(rows[i-1].CreatedAt) && r.ID < rows[i-1].ID) {
			return MemoryOutcomePage{}, ErrUnavailable
		}
		if i == in.Limit {
			out.NextCursor = memoryOutcomeCursorFor(in, rows[i-1])
			break
		}
		d, e := s.authorize(ctx, token, "read:evaluations", in.Scope, r.ID, "")
		if e != nil {
			return MemoryOutcomePage{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return MemoryOutcomePage{}, ErrDenied
		}
		entry := MemoryOutcomeEntry{Evaluation: Ref{Collection: "evaluations", ID: r.ID, Revision: r.Revision}, Memory: *r.Target, Dimension: outcomeDimension(r), Passed: r.Passed, JudgedAt: r.CreatedAt.UTC()}
		for _, rel := range r.Relations {
			if rel.Collection == "execution_traces" {
				rel := rel
				entry.Trace = &rel
				break
			}
		}
		out.Items = append(out.Items, entry)
	}
	sums, e := s.memoryOutcomeSummaries(ctx, in, map[string]bool{id: true})
	if e != nil {
		return MemoryOutcomePage{}, e
	}
	out.Summary = sums[0]
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_outcomes", id)
	}); e != nil {
		return MemoryOutcomePage{}, e
	}
	return out, nil
}

// SummarizeMemoryOutcomes returns per-memory totals for up to 100 ids. A memory
// that is missing, erased or not readable comes back available=false with zero
// counts, so one deleted memory does not fail a daily batch.
func (s *Service) SummarizeMemoryOutcomes(ctx context.Context, token string, in MemoryOutcomeQuery) (MemoryOutcomeSummaries, error) {
	in, _, e := normalizeMemoryOutcomeQuery(in, false)
	if e != nil {
		return MemoryOutcomeSummaries{}, e
	}
	who, e := s.outcomeReader(ctx, token, in.Scope)
	if e != nil {
		return MemoryOutcomeSummaries{}, e
	}
	available := map[string]bool{}
	for _, id := range in.MemoryIDs {
		_, e := s.readableOutcomeMemory(ctx, token, who, in.Scope, id)
		if e != nil && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrDenied) {
			return MemoryOutcomeSummaries{}, e
		}
		available[id] = e == nil
	}
	items, e := s.memoryOutcomeSummaries(ctx, in, available)
	if e != nil {
		return MemoryOutcomeSummaries{}, e
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_outcome_summary", "")
	}); e != nil {
		return MemoryOutcomeSummaries{}, e
	}
	return MemoryOutcomeSummaries{Items: items, Authority: AuthorityDataOnly}, nil
}
