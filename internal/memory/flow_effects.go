package memory

import (
	"context"
	"net/http"
	"strings"
)

const flowEffectKind = "flow_notify_effect"

// FlowEffect is a body-free receipt. The human preview exists only on the
// canonical run waiting for approval; targets, credentials and payloads do not.
type FlowEffect struct {
	RootID         string             `json:"root_id" bson:"root_id"`
	RunID          string             `json:"run_id" bson:"run_id"`
	NodeID         string             `json:"node_id" bson:"node_id"`
	Attempt        int                `json:"attempt" bson:"attempt"`
	Adapter        string             `json:"adapter" bson:"adapter"`
	TargetHash     string             `json:"target_hash" bson:"target_hash"`
	ArgsHash       string             `json:"args_hash" bson:"args_hash"`
	PreviewHash    string             `json:"preview_hash" bson:"preview_hash"`
	Status         string             `json:"status" bson:"status"`
	ApprovedBy     string             `json:"approved_by,omitempty" bson:"approved_by,omitempty"`
	ClaimLeaseHash string             `json:"-" bson:"claim_lease_hash,omitempty"`
	Receipt        *FlowEffectReceipt `json:"receipt,omitempty" bson:"receipt,omitempty"`
}

type FlowEffectReceipt struct {
	Status       string `json:"status" bson:"status"`
	HTTPStatus   int    `json:"http_status" bson:"http_status"`
	ResponseHash string `json:"response_hash,omitempty" bson:"response_hash,omitempty"`
}

type FlowEffectInput struct {
	Scope      Scope  `json:"scope"`
	LeaseToken string `json:"lease_token"`
	NodeID     string `json:"node_id"`
	Attempt    int    `json:"attempt"`
	Adapter    string `json:"adapter"`
	TargetHash string `json:"target_hash"`
	ArgsHash   string `json:"args_hash"`
	Preview    string `json:"preview"`
}

type FlowEffectClaimInput struct {
	Scope      Scope  `json:"scope"`
	LeaseToken string `json:"lease_token"`
	EffectHash string `json:"effect_hash"`
	Preview    string `json:"preview,omitempty"`
}

type FlowEffectSettleInput struct {
	FlowEffectClaimInput
	FlowEffectReceipt
}

type FlowEffectView struct {
	ID              string `json:"id"`
	EffectHash      string `json:"effect_hash"`
	Status          string `json:"status"`
	DispatchAllowed bool   `json:"dispatch_allowed"`
}

// FLOW supports these existing outbound notifications, not a generic URL or
// arbitrary tool executor. The private host configuration resolves the target.
func FlowNotifyNode(n CrewNode) bool {
	return n.Type == "notify" && (n.Config["channel"] == "telegram" || n.Config["channel"] == "slack") &&
		len(n.Config["to"]) <= 128 && !strings.ContainsAny(n.Config["to"], "/{}\r\n")
}

func flowEffectView(r Record, dispatch bool) FlowEffectView {
	return FlowEffectView{r.ID, r.RequestHash, r.FlowEffect.Status, dispatch}
}

func effectHashValid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func effectAdapter(channel string) string { return "notify." + channel + ".v1" }

func (s *Service) effectActor(ctx context.Context, token, id string, scope Scope, adapter, preview string) (Decision, error) {
	destination := "external:" + strings.TrimSuffix(strings.TrimPrefix(adapter, "notify."), ".v1")
	d, e := s.authorizeAt(ctx, token, "write:"+colCrewRuns, scope, id, preview, destination)
	if e == nil && (!crewRunWriter(d.Role) || !d.ExternalTransfer || !d.ContentSafe) {
		e = ErrDenied
	}
	return d, e
}

func flowEffectRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/runs/{id}/effects", func(w http.ResponseWriter, r *http.Request) {
		var in FlowEffectInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.PrepareFlowEffect(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/effects/{effect_id}/claim", func(w http.ResponseWriter, r *http.Request) {
		var in FlowEffectClaimInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ClaimFlowEffect(r.Context(), bearer(r), r.PathValue("id"), r.PathValue("effect_id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/effects/{effect_id}/settle", func(w http.ResponseWriter, r *http.Request) {
		var in FlowEffectSettleInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SettleFlowEffect(r.Context(), bearer(r), r.PathValue("id"), r.PathValue("effect_id"), in)
		respond(w, out, e)
	})
}
