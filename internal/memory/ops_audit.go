package memory

import (
	"context"
	"time"
)

// AuditEventView is the redacted operator form of one audit_events row. The
// free-form decision reason and any stored body are never returned.
type AuditEventView struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Actor      string    `json:"actor"`
	ActorType  string    `json:"actor_type,omitempty"`
	Target     *Ref      `json:"target,omitempty"`
	Decision   string    `json:"decision,omitempty"`
	ReasonCode string    `json:"reason_code,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type AuditEventPage struct {
	Items      []AuditEventView `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type AuditStats struct {
	From     time.Time  `json:"from"`
	To       time.Time  `json:"to"`
	Total    int64      `json:"total"`
	ByAction []OpsCount `json:"by_action"`
	ByDay    []OpsCount `json:"by_day"`
}

func viewAuditEvent(r Record) AuditEventView {
	v := AuditEventView{ID: r.ID, Action: opsText(r.Kind), Actor: opsText(r.CreatedBy), ActorType: opsText(r.CreatedByType), Target: opsRef(r.Target), CreatedAt: r.CreatedAt}
	if r.Audit != nil {
		v.Decision, v.ReasonCode, v.RequestID = opsText(r.Audit.Decision), opsText(r.Audit.ReasonCode), opsText(r.Audit.RequestID)
	}
	return v
}

// ListAuditEvents pages a scope's audit trail. Only a human reads it.
func (s *Service) ListAuditEvents(ctx context.Context, token string, in OpsListInput) (AuditEventPage, error) {
	out := AuditEventPage{Items: []AuditEventView{}}
	if in.Kind != "" {
		return out, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "read:audit_events", in.Scope, "", roleHuman); err != nil {
		return out, err
	}
	rows, next, err := s.opsPage(ctx, in, "audit_events")
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, viewAuditEvent(r))
	}
	out.NextCursor = next
	return out, nil
}

// AuditEventStats counts a scope's audit events by action and by UTC day.
func (s *Service) AuditEventStats(ctx context.Context, token string, scope Scope, from, to time.Time) (AuditStats, error) {
	out := AuditStats{From: from, To: to, ByAction: []OpsCount{}, ByDay: []OpsCount{}}
	if _, err := s.opsAuthorize(ctx, token, "read:audit_events", scope, "", roleHuman); err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	q := opsCountQuery{Scope: scope, Collection: "audit_events", By: "kind", From: from, To: to}
	if out.ByAction, err = st.OpsCounts(ctx, q); err != nil {
		return out, err
	}
	q.By, q.Bucket = "", "day"
	if out.ByDay, err = st.OpsCounts(ctx, q); err != nil {
		return out, err
	}
	for i := range out.ByAction {
		out.ByAction[i].Key = opsText(out.ByAction[i].Key)
		out.Total += out.ByAction[i].Count
	}
	return out, nil
}
