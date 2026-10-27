package memory

import (
	"context"
	"net/http"
	"slices"
	"time"
)

type FlowDeliveryDestination struct {
	BotID     string `json:"bot_id"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	ThreadID  string `json:"thread_id"`
}
type FlowDeliveryRecovery struct {
	Scope            Scope                    `json:"scope"`
	ExpectedRevision int64                    `json:"expected_revision"`
	Action           string                   `json:"action"`
	Confirmed        bool                     `json:"confirmed"`
	MessageID        string                   `json:"message_id,omitempty"`
	Destination      *FlowDeliveryDestination `json:"destination,omitempty"`
}
type FlowDeliveryRecoveryReceipt struct {
	Action             string         `json:"action" bson:"action"`
	By                 string         `json:"by" bson:"by"`
	At                 time.Time      `json:"at" bson:"at"`
	Status             string         `json:"status" bson:"status"`
	Origin             *CrewRunOrigin `json:"origin" bson:"origin"`
	Part               int            `json:"part" bson:"part"`
	MessageIDs         []string       `json:"message_ids" bson:"message_ids"`
	ConfirmedMessageID string         `json:"confirmed_message_id,omitempty" bson:"confirmed_message_id,omitempty"`
}

func validFlowDeliveryRecovery(id string, in FlowDeliveryRecovery) bool {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || !in.Confirmed {
		return false
	}
	switch in.Action {
	case "retry":
		return in.MessageID == "" && in.Destination == nil
	case "ack_part":
		return discordIDRe.MatchString(in.MessageID) && in.Destination == nil
	case "redirect":
		d := in.Destination
		return in.MessageID == "" && d != nil && validRunLabel(d.BotID, 128) && discordIDRe.MatchString(d.GuildID) && discordIDRe.MatchString(d.ChannelID) && discordIDRe.MatchString(d.ThreadID)
	}
	return false
}

// A person reviews the uncertain transport effect, never re-executes the job.
// The delivery worker must still check the current connector permissions before
// sending to either the original or an explicitly selected destination.
func (s *Service) RecoverFlowDelivery(ctx context.Context, token, id string, in FlowDeliveryRecovery) (FlowDeliveryView, error) {
	if !validFlowDeliveryRecovery(id, in) {
		return FlowDeliveryView{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return FlowDeliveryView{}, err
	}
	if d.Role != roleHuman {
		return FlowDeliveryView{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowDeliveryView
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colJobs, in.Scope, id)
		if e != nil {
			return e
		}
		if !readableFlowDelivery(r) {
			return ErrNotFound
		}
		cur := *r.FlowDelivery
		if r.Revision != in.ExpectedRevision || !slices.Contains([]string{"unknown", "failed"}, cur.Status) || len(cur.RecoveryHistory) >= 20 {
			return ErrConflict
		}
		run, e := tx.Get(ctx, colCrewRuns, in.Scope, cur.RunID)
		if e != nil {
			return e
		}
		if !readableCrewRun(run) || !terminalCrewRun(run.CrewRun.Status) || flowResultRevision(run) != cur.ResultRevision {
			return ErrConflict
		}
		if _, e = s.authorize(ctx, token, "read:"+colCrew, in.Scope, run.CrewRun.CrewID, ""); e != nil {
			return e
		}
		if e = s.authorizeFlowSourcesFrom(ctx, token, run, tx); e != nil {
			return e
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if e = applyFlowDeliveryRecovery(&cur, in, d.PrincipalID, now); e != nil {
			return e
		}
		previous := r.Revision
		r.FlowDelivery, r.Status, r.Revision, r.UpdatedAt = &cur, cur.Status, r.Revision+1, now
		r.LeaseToken, r.LeaseUntil = "", time.Time{}
		if e = tx.Replace(ctx, colJobs, r, previous); e != nil {
			return e
		}
		out = flowDeliveryView(r)
		return audit(ctx, tx, in.Scope, d.PrincipalID, "flow_delivery:recover_"+in.Action, id)
	})
	return out, err
}

func applyFlowDeliveryRecovery(cur *FlowDelivery, in FlowDeliveryRecovery, by string, now time.Time) error {
	if cur.Origin == nil || cur.Origin.Connector != "discord" {
		return ErrInvalid
	}
	if in.Action == "ack_part" && (cur.Status != "unknown" || slices.Contains(cur.MessageIDs, in.MessageID)) {
		return ErrConflict
	}
	receipt := FlowDeliveryRecoveryReceipt{Action: in.Action, By: by, At: now, Status: cur.Status, Origin: cur.Origin, Part: cur.Part, MessageIDs: slices.Clone(cur.MessageIDs), ConfirmedMessageID: in.MessageID}
	cur.RecoveryHistory = append(slices.Clone(cur.RecoveryHistory), receipt)
	cur.MessageIDs = slices.Clone(cur.MessageIDs)
	switch in.Action {
	case "ack_part":
		cur.MessageIDs = append(cur.MessageIDs, in.MessageID)
		cur.Part++
	case "redirect":
		origin := *cur.Origin
		origin.BotID, origin.GuildID, origin.ChannelID, origin.ThreadID = in.Destination.BotID, in.Destination.GuildID, in.Destination.ChannelID, in.Destination.ThreadID
		cur.Origin, cur.Part, cur.MessageIDs = &origin, 0, []string{}
	}
	cur.Status, cur.ErrorCode, cur.LeaseUntil = "pending", "", time.Time{}
	return nil
}

func flowDeliveryRecoveryRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/deliveries/{id}/recover", func(w http.ResponseWriter, r *http.Request) {
		var in FlowDeliveryRecovery
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RecoverFlowDelivery(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
