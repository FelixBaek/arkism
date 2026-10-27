package memory

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/FelixBaek/arkism/internal/prices"
)

const flowAttemptKind = "flow_model_attempt"
const flowAttemptMaxTokens int64 = 1_000_000_000

// Attempts contain identity and accounting only, never prompts or outputs.
type FlowAttempt struct {
	RootID          string            `json:"root_id" bson:"root_id"`
	RunID           string            `json:"run_id" bson:"run_id"`
	NodeID          string            `json:"node_id" bson:"node_id"`
	Attempt         int               `json:"attempt" bson:"attempt"`
	CallKey         string            `json:"call_key" bson:"call_key"`
	Model           string            `json:"model" bson:"model"`
	Status          string            `json:"status" bson:"status"`
	MaxInputTokens  int64             `json:"max_input_tokens" bson:"max_input_tokens"`
	MaxOutputTokens int64             `json:"max_output_tokens" bson:"max_output_tokens"`
	ReservedTokens  int64             `json:"reserved_tokens" bson:"reserved_tokens"`
	ReservedCostUSD float64           `json:"reserved_cost_usd" bson:"reserved_cost_usd"`
	CostKnown       bool              `json:"cost_known" bson:"cost_known"`
	PricingRevision string            `json:"pricing_revision,omitempty" bson:"pricing_revision,omitempty"`
	Settlement      *FlowAttemptUsage `json:"settlement,omitempty" bson:"settlement,omitempty"`
	SettlementHash  string            `json:"-" bson:"settlement_hash,omitempty"`
}

type FlowAttemptUsage struct {
	Status       string   `json:"status" bson:"status"`
	InputTokens  int64    `json:"input_tokens" bson:"input_tokens"`
	OutputTokens int64    `json:"output_tokens" bson:"output_tokens"`
	UsageKnown   bool     `json:"usage_known" bson:"usage_known"`
	Estimated    bool     `json:"estimated" bson:"estimated"`
	CostUSD      *float64 `json:"cost_usd,omitempty" bson:"cost_usd,omitempty"`
}

// Root-only counters are changed atomically with each attempt. Estimates are
// separate from reported actual usage. Uncertain usage retains its reservation.
type FlowBudget struct {
	Attempts         int                           `json:"attempts" bson:"attempts"`
	ReservedTokens   int64                         `json:"reserved_tokens" bson:"reserved_tokens"`
	CommittedTokens  int64                         `json:"committed_tokens" bson:"committed_tokens"`
	EstimatedTokens  int64                         `json:"estimated_tokens" bson:"estimated_tokens"`
	ReservedCostUSD  float64                       `json:"reserved_cost_usd" bson:"reserved_cost_usd"`
	CommittedCostUSD float64                       `json:"committed_cost_usd" bson:"committed_cost_usd"`
	EstimatedCostUSD float64                       `json:"estimated_cost_usd" bson:"estimated_cost_usd"`
	UnknownUsage     int                           `json:"unknown_usage" bson:"unknown_usage"`
	UnknownCost      int                           `json:"unknown_cost" bson:"unknown_cost"`
	Pending          map[string]FlowAttemptPending `json:"pending,omitempty" bson:"pending,omitempty"`
}

type FlowAttemptPending struct {
	RunID   string `json:"run_id" bson:"run_id"`
	NodeID  string `json:"node_id" bson:"node_id"`
	Attempt int    `json:"attempt" bson:"attempt"`
	Unknown bool   `json:"unknown" bson:"unknown"`
}

type FlowAttemptInput struct {
	Scope           Scope  `json:"scope"`
	LeaseToken      string `json:"lease_token"`
	NodeID          string `json:"node_id"`
	Attempt         int    `json:"attempt"`
	CallKey         string `json:"call_key"`
	Model           string `json:"model"`
	MaxInputTokens  int64  `json:"max_input_tokens"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
}

type FlowAttemptSettleInput struct {
	Scope      Scope  `json:"scope"`
	LeaseToken string `json:"lease_token"`
	FlowAttemptUsage
}

type FlowAttemptReservation struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	DispatchAllowed bool    `json:"dispatch_allowed"`
	ReservedTokens  int64   `json:"reserved_tokens"`
	ReservedCostUSD float64 `json:"reserved_cost_usd"`
	CostKnown       bool    `json:"cost_known"`
}

func flowAttemptView(r Record, dispatch bool) FlowAttemptReservation {
	a := r.FlowAttempt
	return FlowAttemptReservation{r.ID, a.Status, dispatch, a.ReservedTokens, a.ReservedCostUSD, a.CostKnown}
}

func validFlowAttemptInput(id string, in FlowAttemptInput) bool {
	return validScope(in.Scope) && validRunLabel(id, 256) && validRunLabel(in.NodeID, 40) &&
		in.Attempt > 0 && in.Attempt <= 10000 && validRunLabel(in.CallKey, 256) && validRunLabel(in.Model, 128) &&
		in.MaxInputTokens >= 0 && in.MaxInputTokens <= flowAttemptMaxTokens && in.MaxOutputTokens >= 0 && in.MaxOutputTokens <= flowAttemptMaxTokens
}

func validFlowAttemptUsage(u FlowAttemptUsage) bool {
	if !slices.Contains([]string{"succeeded", "failed", "unknown"}, u.Status) || u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > flowAttemptMaxTokens || u.OutputTokens > flowAttemptMaxTokens {
		return false
	}
	if !u.UsageKnown && (u.InputTokens != 0 || u.OutputTokens != 0 || u.Estimated) {
		return false
	}
	return u.CostUSD == nil || (*u.CostUSD >= 0 && *u.CostUSD <= 1e9 && !math.IsNaN(*u.CostUSD) && !math.IsInf(*u.CostUSD, 0))
}

func flowAttemptIdentity(run string, in FlowAttemptInput) (string, string) {
	id := "flow-attempt-" + ContentHash(structuredText([]any{in.Scope, run, in.NodeID, in.Attempt, in.CallKey}))
	in.LeaseToken = ""
	return id, ContentHash(structuredText(in))
}

func newFlowAttempt(root, run string, in FlowAttemptInput) FlowAttempt {
	cost, known := prices.Cost(in.Model, in.MaxInputTokens, in.MaxOutputTokens)
	a := FlowAttempt{RootID: root, RunID: run, NodeID: in.NodeID, Attempt: in.Attempt, CallKey: in.CallKey, Model: in.Model, Status: "reserved", MaxInputTokens: in.MaxInputTokens, MaxOutputTokens: in.MaxOutputTokens, ReservedTokens: in.MaxInputTokens + in.MaxOutputTokens, ReservedCostUSD: cost, CostKnown: known}
	if known {
		a.PricingRevision = prices.Revision
	}
	if in.MaxInputTokens == 0 || in.MaxOutputTokens == 0 {
		a.CostKnown = false
	}
	return a
}

func (s *Service) flowAttemptActor(ctx context.Context, token, id string, scope Scope) (Decision, error) {
	d, e := s.authorize(ctx, token, "write:"+colCrewRuns, scope, id, "")
	if e == nil && !crewRunWriter(d.Role) {
		e = ErrDenied
	}
	return d, e
}

// CAS conflicts are retried only before dispatch, never by repeating a model call.
func flowAttemptTransaction(ctx context.Context, repo Repository, fn func(context.Context, Tx) error) error {
	var e error
	for i := 0; i < 5; i++ {
		e = repo.Transaction(ctx, fn)
		if !errors.Is(e, ErrConflict) || ctx.Err() != nil {
			return e
		}
	}
	return e
}

func flowAttemptRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/runs/{id}/attempts", func(w http.ResponseWriter, r *http.Request) {
		var in FlowAttemptInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReserveFlowAttempt(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/attempts/{attempt_id}/settle", func(w http.ResponseWriter, r *http.Request) {
		var in FlowAttemptSettleInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SettleFlowAttempt(r.Context(), bearer(r), r.PathValue("id"), r.PathValue("attempt_id"), in)
		respond(w, out, e)
	})
}

func replaceFlowAccounting(ctx context.Context, tx Tx, col string, r *Record) error {
	prev := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	return tx.Replace(ctx, col, *r, prev)
}
