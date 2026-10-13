package memory

import (
	"context"
	"errors"
)

type RecoveryReceiptCause struct {
	Target     DeletionTarget  `json:"target"`
	OriginType string          `json:"origin_type"`
	Basis      string          `json:"basis"`
	RootTarget *DeletionTarget `json:"root_target,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
}

type RecoveryReceiptScope struct {
	Scope map[string]string      `json:"scope"`
	Items []RecoveryReceiptCause `json:"items"`
}

// Resolve only durable links within the already validated, single-snapshot
// inventory. A missing or ambiguous historical link is data for review, not a
// guessed authorization. Erased bodies and actor details are never exported.
func recoveryReceiptCauses(ctx context.Context, p *Postgres, scope Scope, ledger DeletionLedger, requests []Record) (RecoveryReceiptScope, error) {
	out := RecoveryReceiptScope{Scope: ledger.Scope, Items: []RecoveryReceiptCause{}}
	records, err := p.DeletionRecords(ctx, scope)
	if err != nil {
		return out, err
	}
	receipts := map[DeletionTarget]Record{}
	for _, r := range records {
		if r.Target == nil {
			return out, ErrUnavailable
		}
		receipts[DeletionTarget{r.Target.Collection, r.Target.ID}] = r
	}
	requestByID := map[string]Record{}
	// Index only roots represented by receipts. Historical matching must not
	// repeatedly scan every request's full Impact for every receipt.
	coverage := map[DeletionTarget]map[string]bool{}
	addCoverage := func(target DeletionTarget, id string) {
		if _, ok := receipts[target]; !ok {
			return
		}
		if coverage[target] == nil {
			coverage[target] = map[string]bool{}
		}
		coverage[target][id] = true
	}
	for _, r := range requests {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		if r.Scope != scope || r.PrivacyDeletion == nil {
			return out, ErrUnavailable
		}
		requestByID[r.ID] = r
		for _, id := range r.PrivacyDeletion.Impact.SourceEventIDs {
			addCoverage(DeletionTarget{"sources", id}, r.ID)
		}
		for _, ref := range r.PrivacyDeletion.Impact.Records {
			if ref.Collection == colWorkCheckpoints && ref.Revision > 0 {
				addCoverage(DeletionTarget{ref.Collection, ref.ID}, r.ID)
			}
		}
	}
	unresolved := func(target DeletionTarget, basis string) RecoveryReceiptCause {
		return RecoveryReceiptCause{Target: target, OriginType: "unresolved", Basis: basis}
	}
	fromRequest := func(target, root DeletionTarget, id, basis string) RecoveryReceiptCause {
		if !coverage[root][id] {
			return unresolved(target, "invalid_request_impact")
		}
		return RecoveryReceiptCause{Target: target, OriginType: "privacy_request", Basis: basis, RootTarget: &root, RequestID: id}
	}
	fromSourceJob := func(target DeletionTarget, job Record) RecoveryReceiptCause {
		if job.Scope != scope || !validSourceErasureJob(job) || job.Target.Collection != "sources" {
			return unresolved(target, "invalid_parent")
		}
		root := DeletionTarget{job.Target.Collection, job.Target.ID}
		receipt, ok := receipts[root]
		if !ok || (receipt.ErasureParentID != "" && receipt.ErasureParentID != job.ErasureParentID) {
			return unresolved(target, "invalid_parent")
		}
		if job.ErasureParentID != "" {
			return fromRequest(target, root, job.ErasureParentID, "parent_chain")
		}
		if job.CreatedByType != "human" {
			return unresolved(target, "invalid_parent")
		}
		return RecoveryReceiptCause{Target: target, OriginType: "legacy_delete", Basis: "human_delete", RootTarget: &root}
	}
	for _, target := range ledger.Targets {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		row, ok := receipts[target]
		if !ok || row.Scope != scope {
			return out, ErrUnavailable
		}
		cause := unresolved(target, "missing_parent")
		switch row.Kind {
		case workDeletionKind:
			if row.ErasureParentID != "" {
				cause = fromRequest(target, target, row.ErasureParentID, "parent_chain")
			} else {
				matches := []string{}
				for id := range coverage[target] {
					r := requestByID[id]
					if !row.CreatedAt.IsZero() && !r.PrivacyDeletion.RequestedAt.After(row.CreatedAt) {
						matches = append(matches, r.ID)
					}
				}
				if len(matches) == 1 {
					cause = fromRequest(target, target, matches[0], "request_impact")
				}
				if len(matches) > 1 {
					cause = unresolved(target, "ambiguous_request")
				}
			}
		case "source_deletion":
			parent, e := p.Get(ctx, colJobs, scope, sourceMemoryErasureID(scope, target.ID))
			if e != nil && !errors.Is(e, ErrNotFound) {
				return out, e
			}
			if row.ErasureParentID != "" {
				cause = fromRequest(target, target, row.ErasureParentID, "parent_chain")
				if e == nil && (!validSourceErasureJob(parent) || parent.ErasureParentID != row.ErasureParentID) {
					cause = unresolved(target, "invalid_parent")
				}
			} else if e == nil {
				cause = fromSourceJob(target, parent)
			}
		default:
			if !validSourceErasureJob(row) {
				return out, ErrUnavailable
			}
			if row.ErasureParentID == "" {
				if row.CreatedByType == "human" {
					root := target
					cause = RecoveryReceiptCause{Target: target, OriginType: "legacy_delete", Basis: "human_delete", RootTarget: &root}
				}
			} else {
				parent, e := p.Get(ctx, colJobs, scope, row.ErasureParentID)
				if e != nil && !errors.Is(e, ErrNotFound) {
					return out, e
				}
				if e == nil {
					cause = fromSourceJob(target, parent)
				}
			}
		}
		out.Items = append(out.Items, cause)
	}
	return out, nil
}
