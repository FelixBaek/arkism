package memory

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Operator views over audit_events and jobs, scope statistics and tag upkeep.
// Every read is metadata or an aggregate: no record text, job payload or
// free-form audit reason leaves these endpoints.

const (
	opsPageDefault      = 50
	opsPageMax          = 200
	opsRangeDefault     = 30 * 24 * time.Hour
	opsRangeMax         = 366 * 24 * time.Hour
	opsCountRows        = 5000
	maxManualJobRetries = 3
	tagRenameBatch      = 500
	opsRedacted         = "[redacted]"
)

// opsRecordQuery selects metadata rows of one collection, newest first.
type opsRecordQuery struct {
	Scope      Scope
	Collection string
	Kind       string // exact
	KindPrefix string
	Statuses   []string
	Actor      string
	TargetID   string
	Tag        string
	Transport  string    // audit.transport
	From, To   time.Time // zero is open
	After      *keysetCursor
	Limit      int
}

// opsCountQuery groups one collection by a key (kind|status|source|tag|"")
// and an optional time bucket (day|week|"").
type opsCountQuery struct {
	Scope      Scope
	Collection string
	By         string
	Bucket     string
	Statuses   []string
	From, To   time.Time
	// TimeField is the axis From, To and Bucket use: "" or created, mentioned, occurred (opsTimeExpr).
	TimeField string
}

type OpsCount struct {
	Bucket string `json:"bucket,omitempty"`
	Key    string `json:"key"`
	Count  int64  `json:"count"`
}

// opsStore is the optional query surface the operator views need. The
// Postgres store implements it; a store without it answers ErrUnavailable.
type opsStore interface {
	OpsRecords(context.Context, opsRecordQuery) ([]Record, error)
	OpsCounts(context.Context, opsCountQuery) ([]OpsCount, error)
}

func (s *Service) opsStore() (opsStore, error) {
	st, ok := s.Repo.(opsStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return st, nil
}

// opsAuthorize checks one primary action and the allowed roles.
func (s *Service) opsAuthorize(ctx context.Context, token, action string, scope Scope, id string, roles ...string) (Decision, error) {
	if !validScope(scope) || containsSecret(structuredText(scope)) || id != "" && (!validRunLabel(id, 256) || containsSecret(id)) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, action, scope, id, "")
	if err != nil {
		return Decision{}, err
	}
	for _, role := range roles {
		if d.Role == role {
			return d, nil
		}
	}
	return Decision{}, ErrDenied
}

// OpsListInput is the shared filter of the audit and operation lists.
type OpsListInput struct {
	Scope    Scope
	Action   string // audit: kind prefix
	Kind     string // operations: exact kind
	Status   string
	Actor    string
	RecordID string
	Tag      string
	// Transport (audit only): http, mcp or system.
	Transport string
	From, To  time.Time
	Cursor    string
	Limit     int
}

// ParseOpsListQuery reads the list filters from a URL query.
func ParseOpsListQuery(q url.Values) (OpsListInput, error) {
	in := OpsListInput{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")},
		Action: q.Get("action"), Kind: q.Get("kind"), Status: q.Get("status"), Actor: q.Get("actor"), RecordID: q.Get("record_id"), Transport: q.Get("transport"), Cursor: q.Get("cursor")}
	var err error
	if in.From, in.To, err = parseOpsRange(q, false); err != nil {
		return in, err
	}
	if v := q.Get("limit"); v != "" {
		if in.Limit, err = strconv.Atoi(v); err != nil {
			return in, ErrInvalid
		}
	}
	return in, nil
}

// parseOpsRange reads from/to (RFC 3339). With defaults, an open range becomes
// the last 30 days; any range is capped so aggregates stay bounded.
func parseOpsRange(q url.Values, defaults bool) (time.Time, time.Time, error) {
	var from, to time.Time
	for name, field := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil || !storableTime(t) {
				return from, to, ErrInvalid
			}
			*field = t.UTC().Truncate(time.Millisecond)
		}
	}
	if defaults {
		if to.IsZero() {
			to = time.Now().UTC().Truncate(time.Millisecond)
		}
		if from.IsZero() {
			from = to.Add(-opsRangeDefault)
		}
		if to.Sub(from) > opsRangeMax {
			return from, to, ErrInvalid
		}
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return from, to, ErrInvalid
	}
	return from, to, nil
}

var auditTransports = []string{"", "http", "mcp", "system"}

func validOpsFilter(v string) bool { return v == "" || validRunLabel(v, 256) && !containsSecret(v) }

// recordQuery validates the shared filters and turns them into a store query.
func (in OpsListInput) recordQuery(collection, binding string) (opsRecordQuery, error) {
	if in.Limit == 0 {
		in.Limit = opsPageDefault
	}
	if in.Limit < 1 || in.Limit > opsPageMax {
		return opsRecordQuery{}, ErrInvalid
	}
	if !slices.Contains(auditTransports, in.Transport) {
		return opsRecordQuery{}, ErrInvalid
	}
	for _, v := range []string{in.Action, in.Kind, in.Status, in.Actor, in.RecordID, in.Tag} {
		if !validOpsFilter(v) {
			return opsRecordQuery{}, ErrInvalid
		}
	}
	q := opsRecordQuery{Scope: in.Scope, Collection: collection, Kind: in.Kind, KindPrefix: in.Action, Actor: in.Actor, TargetID: in.RecordID, Tag: in.Tag, Transport: in.Transport, From: in.From, To: in.To, Limit: in.Limit + 1}
	if in.Status != "" {
		q.Statuses = []string{in.Status}
	}
	if in.Cursor != "" {
		after, err := decodeKeysetCursor(in.Cursor, binding)
		if err != nil {
			return opsRecordQuery{}, err
		}
		q.After = after
	}
	return q, nil
}

// opsBinding ties a cursor to the filters that produced it.
func (in OpsListInput) binding(collection string) string {
	in.Cursor, in.Limit = "", 0
	return ContentHash(collection + "\x00" + structuredText(in))
}

// opsPage runs one list query and returns the page rows plus the next cursor.
func (s *Service) opsPage(ctx context.Context, in OpsListInput, collection string) ([]Record, string, error) {
	binding := in.binding(collection)
	q, err := in.recordQuery(collection, binding)
	if err != nil {
		return nil, "", err
	}
	st, err := s.opsStore()
	if err != nil {
		return nil, "", err
	}
	rows, err := st.OpsRecords(ctx, q)
	if err != nil {
		return nil, "", err
	}
	if len(rows) < q.Limit {
		return rows, "", nil
	}
	rows = rows[:q.Limit-1]
	last := rows[len(rows)-1]
	next, err := encodeKeysetCursor(last.CreatedAt.Truncate(time.Millisecond), last.ID, binding)
	return rows, next, err
}

// opsText keeps a stored label only when it carries nothing secret.
func opsText(v string) string {
	if containsSecret(v) {
		return opsRedacted
	}
	return v
}

func opsRef(r *Ref) *Ref {
	if r == nil || r.ID == "" && r.Collection == "" {
		return nil
	}
	return &Ref{Collection: opsText(r.Collection), ID: opsText(r.ID), Revision: r.Revision}
}

func opsCountMap(rows []OpsCount) (map[string]int64, int64) {
	out := map[string]int64{}
	var total int64
	for _, c := range rows {
		out[c.Key] += c.Count
		total += c.Count
	}
	return out, total
}
