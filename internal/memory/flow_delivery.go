package memory

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"
)

const flowDeliveryKind = "flow_result_delivery"
const flowDeliveryLease = 90 * time.Second

// Jobs contain references and receipts only. Result bodies stay in crew_runs.
type FlowDelivery struct {
	ResultHash     string         `json:"result_hash,omitempty" bson:"result_hash,omitempty"`
	RunID          string         `json:"run_id" bson:"run_id"`
	ResultRevision int64          `json:"result_revision" bson:"result_revision"`
	Origin         *CrewRunOrigin `json:"origin" bson:"origin"`
	Status         string         `json:"status" bson:"status"`
	Part           int            `json:"part" bson:"part"`
	MessageIDs     []string       `json:"message_ids" bson:"message_ids"`
	ErrorCode      string         `json:"error_code,omitempty" bson:"error_code,omitempty"`
	LeaseUntil     time.Time      `json:"lease_until,omitempty" bson:"lease_until,omitempty"`
}

type FlowDeliveryView struct {
	ID       string       `json:"id"`
	Revision int64        `json:"revision"`
	Delivery FlowDelivery `json:"delivery"`
}
type FlowDeliveryPage struct {
	Items      []FlowDeliveryView `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}
type FlowDeliveryChange struct {
	ResultHash       string `json:"result_hash,omitempty"`
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	LeaseToken       string `json:"lease_token"`
	Part             int    `json:"part,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
}

func flowDeliveryID(runID string) string { return "delivery-" + runID }
func flowDeliveryView(r Record) FlowDeliveryView {
	return FlowDeliveryView{r.ID, r.Revision, *r.FlowDelivery}
}

func enqueueFlowDelivery(ctx context.Context, tx Tx, r Record) error {
	if r.CrewRun.InvocationMode != "flow" || r.CrewRun.Origin == nil || r.CrewRun.Origin.Connector != "discord" {
		return nil
	}
	d := Record{ID: flowDeliveryID(r.ID), Scope: r.Scope, Kind: flowDeliveryKind, Status: "waiting_run", Revision: 1, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CreatedBy: r.CreatedBy, CreatedByType: r.CreatedByType,
		FlowDelivery: &FlowDelivery{RunID: r.ID, Origin: r.CrewRun.Origin, Status: "waiting_run", MessageIDs: []string{}}}
	return tx.Insert(ctx, colJobs, d)
}

func readyFlowDelivery(ctx context.Context, tx Tx, r Record) error {
	if r.CrewRun.InvocationMode != "flow" || r.CrewRun.Origin == nil || r.CrewRun.Origin.Connector != "discord" || !terminalCrewRun(r.CrewRun.Status) {
		return nil
	}
	d, err := tx.Get(ctx, colJobs, r.Scope, flowDeliveryID(r.ID))
	if err != nil {
		return err
	}
	if d.FlowDelivery == nil || d.FlowDelivery.Status != "waiting_run" {
		return ErrConflict
	}
	prev := d.Revision
	delivery := *d.FlowDelivery
	delivery.ResultRevision, delivery.Status = r.Revision, "pending"
	d.FlowDelivery = &delivery
	d.Status = "pending"
	d.Revision++
	d.UpdatedAt = r.UpdatedAt
	return tx.Replace(ctx, colJobs, d, prev)
}

func (s *Service) ListFlowDeliveries(ctx context.Context, token string, scope Scope, connector, botID, status, after string, limit int) (FlowDeliveryPage, error) {
	if !validScope(scope) || connector != "discord" || !validRunLabel(botID, 128) || limit < 0 || limit > 200 || after != "" && !validRunLabel(after, 256) || !slices.Contains([]string{"", "pending", "sending", "retry_wait", "unknown", "failed", "delivered", "waiting_run"}, status) {
		return FlowDeliveryPage{}, ErrInvalid
	}
	if limit == 0 {
		limit = 200
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, "", ""); err != nil {
		return FlowDeliveryPage{}, err
	}
	rows, err := s.Repo.ListFlowDeliveries(ctx, scope, connector, botID, status, after, limit+1)
	if err != nil {
		return FlowDeliveryPage{}, err
	}
	out := FlowDeliveryPage{Items: []FlowDeliveryView{}}
	if len(rows) > limit {
		out.NextCursor = rows[limit-1].ID
		rows = rows[:limit]
	}
	for _, r := range rows {
		out.Items = append(out.Items, flowDeliveryView(r))
	}
	return out, nil
}

// Changes are serialized by the job revision. A lost send response is never
// retried blindly; expired sending leases become unknown and are inspectable.
func (s *Service) ChangeFlowDelivery(ctx context.Context, token, id string, in FlowDeliveryChange) (FlowDeliveryView, error) {
	if in.Action == "claim" && !crewHashRe.MatchString(in.ResultHash) {
		return FlowDeliveryView{}, ErrInvalid
	}
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || !validRunLabel(in.LeaseToken, 128) || len(in.LeaseToken) < 16 || in.Part < 0 || in.Part > 1000 || !validFlowCode(in.ErrorCode) || !slices.Contains([]string{"claim", "ack_part", "complete", "retry", "unknown", "failed"}, in.Action) {
		return FlowDeliveryView{}, ErrInvalid
	}
	if in.Action == "ack_part" && !discordIDRe.MatchString(in.MessageID) {
		return FlowDeliveryView{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return FlowDeliveryView{}, err
	}
	if d.Role != roleService && d.Role != roleHuman {
		return FlowDeliveryView{}, ErrDenied
	}
	var out FlowDeliveryView
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colJobs, in.Scope, id)
		if e != nil {
			return e
		}
		if r.Kind != flowDeliveryKind || r.FlowDelivery == nil {
			return ErrNotFound
		}
		cur := *r.FlowDelivery
		if in.Action == "claim" {
			if _, e := s.authorize(ctx, token, "read:"+colCrew, in.Scope, cur.RunID, ""); e != nil {
				return e
			}
		}
		cur.MessageIDs = slices.Clone(cur.MessageIDs)
		now := time.Now().UTC().Truncate(time.Millisecond)
		if in.Action == "ack_part" && r.LeaseToken == in.LeaseToken && in.Part < cur.Part && cur.MessageIDs[in.Part] == in.MessageID {
			out = flowDeliveryView(r)
			return nil
		}
		if in.Action == "complete" && r.LeaseToken == in.LeaseToken && cur.Status == "delivered" {
			out = flowDeliveryView(r)
			return nil
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if cur.Status == "sending" && !r.LeaseUntil.After(now) {
			cur.Status = "unknown"
			cur.ErrorCode = "send_lease_expired"
		} else {
			if e = s.flowDeliveryAction(ctx, tx, &r, &cur, in, now); e != nil {
				return e
			}
		}
		prev := r.Revision
		r.FlowDelivery = &cur
		r.Status = cur.Status
		r.Revision++
		r.UpdatedAt = now
		if e = tx.Replace(ctx, colJobs, r, prev); e != nil {
			return e
		}
		out = flowDeliveryView(r)
		return audit(ctx, tx, in.Scope, d.PrincipalID, "flow_delivery:"+in.Action, id)
	})
	return out, err
}

func (s *Service) flowDeliveryAction(ctx context.Context, tx Tx, r *Record, cur *FlowDelivery, in FlowDeliveryChange, now time.Time) error {
	if in.Action == "claim" {
		return claimFlowDelivery(ctx, tx, r, cur, in, now)
	}
	if in.Action == "complete" && cur.Status == "pending" && cur.Part > 0 && r.LeaseToken == in.LeaseToken {
		cur.Status = "delivered"
		return nil
	}
	if cur.Status != "sending" || r.LeaseToken != in.LeaseToken {
		return ErrConflict
	}
	switch in.Action {
	case "ack_part":
		if in.Part != cur.Part {
			return ErrConflict
		}
		cur.MessageIDs = append(cur.MessageIDs, in.MessageID)
		cur.Part++
		cur.Status = "pending"
		r.LeaseUntil = time.Time{}
		cur.LeaseUntil = r.LeaseUntil
	case "complete":
		if cur.Part == 0 {
			return ErrConflict
		}
		cur.Status = "delivered"
	case "retry":
		cur.Status = "retry_wait"
		cur.ErrorCode = in.ErrorCode
	case "unknown":
		cur.Status = "unknown"
		cur.ErrorCode = in.ErrorCode
	case "failed":
		cur.Status = "failed"
		cur.ErrorCode = in.ErrorCode
	}
	return nil
}

func claimFlowDelivery(ctx context.Context, tx Tx, r *Record, cur *FlowDelivery, in FlowDeliveryChange, now time.Time) error {
	if cur.ResultHash != "" && cur.ResultHash != in.ResultHash {
		return ErrConflict
	}
	if cur.Status != "pending" && cur.Status != "retry_wait" {
		return ErrConflict
	}
	run, err := tx.Get(ctx, colCrewRuns, r.Scope, cur.RunID)
	if err != nil {
		return err
	}
	if !readableCrewRun(run) || !terminalCrewRun(run.CrewRun.Status) || run.Revision != cur.ResultRevision {
		return ErrConflict
	}
	r.LeaseToken = in.LeaseToken
	cur.ResultHash = in.ResultHash
	r.LeaseUntil = now.Add(flowDeliveryLease)
	cur.LeaseUntil = r.LeaseUntil
	cur.Status = "sending"
	cur.ErrorCode = ""
	return nil
}

func flowDeliveryRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/crew/deliveries", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 0
		var err error
		if q.Get("limit") != "" {
			limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.ListFlowDeliveries(r.Context(), bearer(r), scopeQuery(r), q.Get("connector"), q.Get("bot_id"), q.Get("status"), q.Get("after"), limit)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/deliveries/{id}/change", func(w http.ResponseWriter, r *http.Request) {
		var in FlowDeliveryChange
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ChangeFlowDelivery(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
