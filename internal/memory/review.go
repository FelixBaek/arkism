package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const reviewAction = "review:memory_candidates"

type ReviewInput struct {
	omitAdvice  bool       // Internal catalog scans must not trigger optional paid scoring for every row.
	Scope       Scope      `json:"scope"`
	Status      string     `json:"status"`
	Kind        string     `json:"kind,omitempty"`
	Proposer    string     `json:"proposer,omitempty"`
	CreatedFrom *time.Time `json:"created_from,omitempty"`
	CreatedTo   *time.Time `json:"created_to,omitempty"`
	Limit       int        `json:"limit"`
	Cursor      string     `json:"cursor,omitempty"`
}
type ReviewPage struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor"`
	Authority  string   `json:"authority"`
}
type reviewCursor struct {
	Trust     float64   `json:"trust"`
	Evidence  int       `json:"evidence"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
	Binding   string    `json:"binding"`
}

func reviewPosition(r Record, binding string) reviewCursor {
	ids := map[string]bool{}
	for _, ref := range r.Sources {
		if ref.SourceID != "" {
			ids[ref.SourceID] = true
		}
	}
	return reviewCursor{r.Trust, len(ids), r.CreatedAt, r.ID, binding}
}
func reviewBinding(in ReviewInput) string {
	in.Cursor = ""
	in.Limit = 0
	return ContentHash(structuredText(in))
}
func normalizeReview(in ReviewInput) (ReviewInput, *reviewCursor, error) {
	if in.Status == "" {
		in.Status = "candidate"
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || !slices.Contains([]string{"candidate", "approved", "conflicted", "rejected", "expired"}, in.Status) || in.Limit < 1 || in.Limit > 100 || in.Kind != "" && !validCreateKind("memory_candidates", in.Kind) || len(in.Proposer) > 256 || strings.IndexFunc(in.Proposer, unicode.IsControl) >= 0 || containsSecret(in.Proposer) {
		return in, nil, ErrInvalid
	}
	for _, field := range []**time.Time{&in.CreatedFrom, &in.CreatedTo} {
		if *field != nil {
			normalized := (**field).UTC().Truncate(time.Millisecond)
			if normalized.IsZero() {
				return in, nil, ErrInvalid
			}
			*field = &normalized
		}
	}
	if in.CreatedFrom != nil && in.CreatedTo != nil && !in.CreatedFrom.Before(*in.CreatedTo) {
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
	var after reviewCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&after) != nil {
		return in, nil, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return in, nil, ErrInvalid
	}
	if after.Binding != reviewBinding(in) || math.IsNaN(after.Trust) || math.IsInf(after.Trust, 0) || after.Trust < 0 || after.Trust > 1 || after.Evidence < 0 || after.Evidence > 64 || after.CreatedAt.IsZero() || after.ID == "" || len(after.ID) > 256 || strings.IndexFunc(after.ID, unicode.IsControl) >= 0 || containsSecret(after.ID) {
		return in, nil, ErrInvalid
	}
	return in, &after, nil
}
func (s *Service) ListCandidates(ctx context.Context, token string, in ReviewInput) (ReviewPage, error) {
	out := ReviewPage{Items: []Record{}, Authority: "data_only_untrusted"}
	actor, e := s.authorize(ctx, token, reviewAction, in.Scope, "", "")
	if e != nil {
		return out, e
	}
	// A service reads the queue only with an explicit review grant (the memory reviewer, which classifies it);
	// approving stays human-only in ReviewCandidate (ADR-0022).
	if actor.Role != "human" && actor.Role != "service" {
		return out, ErrDenied
	}
	in, _, e = normalizeReview(in)
	if e != nil {
		return out, e
	}
	rows, e := s.Repo.ListCandidates(ctx, in)
	if e != nil {
		return out, e
	}
	if len(rows) > in.Limit+1 {
		return out, ErrUnavailable
	}
	adviceAllowed, e := s.authorizeReviewRows(ctx, token, in.Scope, actor, rows)
	if e != nil {
		return out, e
	}

	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		position := reviewPosition(rows[len(rows)-1], reviewBinding(in))
		encoded, e := json.Marshal(position)
		if e != nil {
			return out, ErrUnavailable
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	if in.Status == "candidate" && !in.omitAdvice && adviceAllowed {
		// After the cursor is fixed, so reordering the page cannot skip or repeat rows.
		s.scoreCandidates(ctx, token, in.Scope, actor, rows)
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "review:list", "")
	})
	if e != nil {
		return ReviewPage{}, e
	}
	for i := range rows {
		normalizeMemoryProvenance(&rows[i])
	}
	out.Items = append(out.Items, rows...)
	return out, nil
}

// shortcut: computed evidence scans matching candidates; persist the count and index the ordering if measured queue latency requires it.

func parseReviewQuery(raw string) (ReviewInput, error) {
	var in ReviewInput
	q, e := url.ParseQuery(raw)
	if e != nil {
		return in, ErrInvalid
	}
	allowed := []string{"workspace_id", "project_id", "user_id", "agent_id", "status", "kind", "proposer", "created_from", "created_to", "limit", "cursor", "group"}
	for key, values := range q {
		if !slices.Contains(allowed, key) || len(values) != 1 {
			return in, ErrInvalid
		}
	}
	if group, ok := q["group"]; ok && group[0] != "false" {
		return in, ErrInvalid
	}
	in = ReviewInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Status: q.Get("status"), Kind: q.Get("kind"), Proposer: q.Get("proposer"), Cursor: q.Get("cursor"), Limit: 20}
	if limit, ok := q["limit"]; ok {
		in.Limit, e = strconv.Atoi(limit[0])
		if e != nil || in.Limit < 1 {
			return in, ErrInvalid
		}
	}
	for _, field := range []struct {
		key    string
		target **time.Time
	}{{"created_from", &in.CreatedFrom}, {"created_to", &in.CreatedTo}} {
		if value, ok := q[field.key]; ok {
			parsed, e := time.Parse(time.RFC3339Nano, value[0])
			if e != nil {
				return in, ErrInvalid
			}
			*field.target = &parsed
		}
	}
	return in, nil
}

// A collection-level grant never replaces each record's transfer decision.
func (s *Service) authorizeReviewRows(ctx context.Context, token string, scope Scope, actor Decision, rows []Record) (bool, error) {
	advice := actor.ExternalTransfer && actor.ContentSafe
	for _, r := range rows {
		if r.Scope != scope || r.PrivacyQuarantined {
			return false, ErrDenied
		}
		decision, err := s.authorize(ctx, token, reviewAction, scope, r.ID, structuredText(r))
		if err != nil {
			return false, err
		}
		if decision.Role != actor.Role || decision.PrincipalID != actor.PrincipalID {
			return false, ErrDenied
		}
		advice = advice && decision.ExternalTransfer && decision.ContentSafe
	}
	return advice, nil
}
