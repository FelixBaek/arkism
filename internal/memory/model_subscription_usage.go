package memory

import (
	"context"
	"errors"
	"time"

	"github.com/FelixBaek/arkism/internal/prices"
)

// Subscription usage (ST-620, FR-012): `claude -p` calls run on the user's Claude
// subscription, not the API account, so they never reserve or settle the monthly
// API budget. The host still reports what each call used; memoryd files it in
// the provider_usage ledger at list price (an estimate of the API-equivalent
// value, not a charge) and keeps a separate monthly counter for the 사용량 screen.

type SubscriptionUsageInput struct {
	Scope       Scope          `json:"scope"`
	OperationID string         `json:"operation_id"`
	Operation   string         `json:"operation"`
	Usage       ModelCallUsage `json:"usage"`
}

type SubscriptionUsageReceipt struct {
	ID                   string `json:"id"`
	Recorded             bool   `json:"recorded"`
	EstimatedCostPicoUSD int64  `json:"estimated_cost_pico_usd"`
	PricingAvailable     bool   `json:"pricing_available"`
}

const subscriptionCounterKind = "subscription_usage_counter"

func subscriptionCounterID(scope Scope, month string) string {
	return budgetCounterID(scope, "subscription:"+month)
}

// RecordSubscriptionUsage is record-only: it never touches the API budget
// counters and never refuses a call because of spend.
func (s *Service) RecordSubscriptionUsage(ctx context.Context, token string, in SubscriptionUsageInput) (SubscriptionUsageReceipt, error) {
	u := in.Usage
	if !idempotencyPattern.MatchString(in.OperationID) || !validRunLabel(in.Operation, 64) || !idempotencyPattern.MatchString(in.Operation) || !validModelCallUsage(&u) || u.InputTokens+u.OutputTokens == 0 {
		return SubscriptionUsageReceipt{}, ErrInvalid
	}
	u.Requests = max(u.Requests, 1)
	// The same principals that settle API calls report subscription calls; no new grant.
	d, e := s.budgetActor(ctx, token, in.Scope, "settle")
	if e != nil {
		return SubscriptionUsageReceipt{}, e
	}
	ctx = bindRequestActor(ctx, d)
	id := "mbs_" + ContentHash(structuredText(in.Scope)+"\n"+d.PrincipalID+"\n"+in.OperationID)
	hash := ContentHash(structuredText(in))
	cost, priced := listPrice(ProviderUsage{Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens})
	out := SubscriptionUsageReceipt{ID: id, Recorded: true, EstimatedCostPicoUSD: cost, PricingAvailable: priced}
	e = s.modelBudgetTransaction(ctx, func(ctx context.Context, tx Tx) error {
		prior, err := tx.Get(ctx, colProviderUsage, in.Scope, id)
		if err == nil {
			if prior.RequestHash != hash || prior.Cost == nil {
				return ErrConflict
			}
			out.EstimatedCostPicoUSD, out.PricingAvailable = prior.Cost.EstimatedCostPicoUSD, prior.Cost.PricingAvailable
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		row := Record{ID: id, Scope: in.Scope, Kind: providerUsageKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role, RequestHash: hash,
			Cost: &ProviderCost{Operation: in.Operation, Provider: modelCallProvider, Model: u.Model, Requests: u.Requests, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.InputTokens + u.OutputTokens, EstimatedCostPicoUSD: cost, PricingAvailable: priced}}
		if priced {
			row.Cost.PricingRevision = prices.Revision
		}
		if err = tx.Insert(ctx, colProviderUsage, row); err != nil {
			return err
		}
		counter, err := tx.Get(ctx, "jobs", in.Scope, subscriptionCounterID(in.Scope, now.Format("2006-01")))
		if errors.Is(err, ErrNotFound) {
			counter = Record{ID: subscriptionCounterID(in.Scope, now.Format("2006-01")), Scope: in.Scope, Kind: subscriptionCounterKind, Status: JobStatusDone, CreatedAt: now, Cost: &ProviderCost{Operation: "subscription", Provider: modelCallProvider, Model: "all"}}
		} else if err != nil {
			return err
		} else if counter.Kind != subscriptionCounterKind || counter.Cost == nil {
			return ErrUnavailable
		}
		c := *counter.Cost
		c.Requests += u.Requests
		c.InputTokens += u.InputTokens
		c.OutputTokens += u.OutputTokens
		c.TotalTokens += u.InputTokens + u.OutputTokens
		c.EstimatedCostPicoUSD += cost
		counter.Cost, counter.UpdatedAt = &c, now
		if counter.Revision == 0 {
			counter.Revision = 1
			err = tx.Insert(ctx, "jobs", counter)
		} else {
			rev := counter.Revision
			counter.Revision++
			err = tx.Replace(ctx, "jobs", counter, rev)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "record:subscription_usage", id)
	})
	if e != nil {
		return SubscriptionUsageReceipt{}, e
	}
	return out, nil
}

// subscriptionMonth is the month's subscription counter, zero when none was recorded.
func subscriptionMonth(ctx context.Context, tx Tx, scope Scope, month string) (ProviderCost, error) {
	row, err := tx.Get(ctx, "jobs", scope, subscriptionCounterID(scope, month))
	if errors.Is(err, ErrNotFound) {
		return ProviderCost{}, nil
	}
	if err != nil {
		return ProviderCost{}, err
	}
	if row.Kind != subscriptionCounterKind || row.Cost == nil {
		return ProviderCost{}, ErrUnavailable
	}
	return *row.Cost, nil
}
