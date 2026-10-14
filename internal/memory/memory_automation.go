package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
)

// Deployment policy, never accepted from a model result or review request.
// Exact scope matching deliberately includes user and agent; no wildcard or
// inferred cross-agent grant is introduced here.
type MemoryReviewPolicy struct {
	PrincipalID string    `json:"principal_id"`
	Scope       Scope     `json:"scope"`
	Kinds       []string  `json:"kinds"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func ParseMemoryReviewPolicies(raw string) ([]MemoryReviewPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var policies []MemoryReviewPolicy
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policies); err != nil {
		return nil, ErrInvalid
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	if len(policies) == 0 || len(policies) > 32 {
		return nil, ErrInvalid
	}
	for _, p := range policies {
		if !validRunLabel(p.PrincipalID, 256) || !validScope(p.Scope) || p.ExpiresAt.IsZero() || len(p.Kinds) == 0 || len(p.Kinds) > 7 || containsSecret(structuredText(p)) {
			return nil, ErrInvalid
		}
		for _, kind := range p.Kinds {
			if !validCreateKind("memory_candidates", kind) {
				return nil, ErrInvalid
			}
		}
	}
	return policies, nil
}

func (s *Service) memoryReviewerAllowed(d Decision, scope Scope, kind string) bool {
	if d.Role == roleHuman {
		return true
	}
	if d.Role != roleService {
		return false
	}
	for _, p := range s.MemoryReviewPolicies {
		if p.PrincipalID == d.PrincipalID && p.Scope == scope && p.ExpiresAt.After(time.Now()) && (kind == "" || slices.Contains(p.Kinds, kind)) {
			return true
		}
	}
	return false
}

func checkAutomaticMemoryReview(ctx context.Context, tx Tx, r Record, review CandidateReviewInput) error {
	if review.Action == "reject" {
		return nil
	}
	if slices.Contains(r.Tags, pendingCaptureCorrectionTag) && review.Conflict == nil {
		return ErrDenied
	}
	if r.Kind == "episodic" {
		return ErrDenied
	}
	if slices.Contains(r.Tags, "review:instruction_like") || instructionLike.MatchString(r.Text+" "+review.Content) {
		if !slices.Contains(r.Tags, "basis:explicit_user") {
			return ErrDenied
		}
		sources, err := sourceMetadata(ctx, tx, r.Scope, r.Sources)
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return ErrDenied
		}
		for _, source := range sources {
			if source.TrustLevel != "user_authored" {
				return ErrDenied
			}
		}
	}
	return nil
}

func reviewOperationID(in CandidateReviewInput, principal string) string {
	return "mr_" + ContentHash(structuredText(in.Scope)+"\n"+principal+"\n"+in.OperationID)
}

func replayMemoryReview(ctx context.Context, tx Tx, id, principal string, in CandidateReviewInput) (Record, bool, error) {
	if in.OperationID == "" {
		return Record{}, false, nil
	}
	receipt, err := tx.Get(ctx, "jobs", in.Scope, reviewOperationID(in, principal))
	if errors.Is(err, ErrNotFound) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if receipt.Kind != "memory_review_receipt" || receipt.Status != JobStatusDone || receipt.RequestHash != ContentHash(id+"\n"+structuredText(in)) || receipt.Target == nil || receipt.Target.ID != id {
		return Record{}, false, ErrConflict
	}
	out, err := tx.Get(ctx, receipt.Target.Collection, in.Scope, receipt.Target.ID)
	if errors.Is(err, ErrNotFound) {
		return Record{}, false, ErrConflict
	}
	if err != nil {
		return Record{}, false, err
	}
	if out.Revision != receipt.Target.Revision || out.ContentErased || out.PrivacyQuarantined {
		return Record{}, false, ErrConflict
	}
	if in.Action != "reject" {
		if !live(out) {
			return Record{}, false, ErrConflict
		}
		if err = checkSources(ctx, tx, out); err != nil {
			return Record{}, false, err
		}
		if err = checkRelations(ctx, tx, out); err != nil {
			return Record{}, false, err
		}
	} else if out.Status != "rejected" {
		return Record{}, false, ErrConflict
	}
	out.Duplicate = true
	return out, true, nil
}

func recordMemoryReview(ctx context.Context, tx Tx, id, principal string, in CandidateReviewInput, result Record) error {
	if in.OperationID == "" {
		return nil
	}
	col := colMemories
	if in.Action == "reject" {
		col = "memory_candidates"
	}
	now := time.Now().UTC()
	return tx.Insert(ctx, "jobs", Record{ID: reviewOperationID(in, principal), Scope: in.Scope, Kind: "memory_review_receipt", Status: JobStatusDone, Revision: 1,
		RequestHash: ContentHash(id + "\n" + structuredText(in)), Target: &Ref{Collection: col, ID: result.ID, Revision: result.Revision}, CreatedAt: now, UpdatedAt: now})
}
