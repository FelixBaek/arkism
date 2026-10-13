package memory

import (
	"context"
	"strings"
	"time"
)

type CandidateReviewInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Content          string `json:"content,omitempty"`
	Reason           string `json:"reason,omitempty"`
}
type ReviewDecision struct {
	Action string    `json:"action" bson:"action"`
	By     string    `json:"by" bson:"by"`
	At     time.Time `json:"at" bson:"at"`
	Reason string    `json:"reason,omitempty" bson:"reason,omitempty"`
}

func (s *Service) ReviewCandidate(ctx context.Context, token, id string, in CandidateReviewInput) (Record, error) {
	if in.ExpectedRevision < 1 || id == "" || len(id) > 256 {
		return Record{}, ErrInvalid
	}
	status := "approved"
	switch in.Action {
	case "approve":
		if in.Content != "" || in.Reason != "" {
			return Record{}, ErrInvalid
		}
	case "edit_approve":
		if strings.TrimSpace(in.Content) == "" || len(in.Content) > 64000 || in.Reason != "" {
			return Record{}, ErrInvalid
		}
	case "reject":
		status = "rejected"
		if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 || in.Content != "" {
			return Record{}, ErrInvalid
		}
	default:
		return Record{}, ErrInvalid
	}
	if containsSecret(structuredText(in)) {
		return Record{}, ErrSecretDetected
	}
	r, e := s.transition(ctx, token, "memory_candidates", id, ChangeInput{Scope: in.Scope, ExpectedRevision: in.ExpectedRevision, Status: status}, &in)
	if e != nil {
		return Record{}, e
	}
	return r, nil
}

type CandidateReviewBatchItem struct {
	ID               string `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Content          string `json:"content,omitempty"`
	Reason           string `json:"reason,omitempty"`
}
type CandidateReviewBatchInput struct {
	Scope Scope                      `json:"scope"`
	Items []CandidateReviewBatchItem `json:"items"`
}
type CandidateReviewBatchResult struct {
	Index  int     `json:"index"`
	Status int     `json:"status"`
	Code   string  `json:"code,omitempty"`
	Record *Record `json:"record,omitempty"`
}
type CandidateReviewBatchOutput struct {
	Items []CandidateReviewBatchResult `json:"items"`
}

func (s *Service) ReviewCandidates(ctx context.Context, token string, in CandidateReviewBatchInput) (CandidateReviewBatchOutput, error) {
	out := CandidateReviewBatchOutput{Items: []CandidateReviewBatchResult{}}
	if len(in.Items) < 1 || len(in.Items) > 20 {
		return out, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "transition:memory_candidates", in.Scope, "", "")
	if e != nil {
		return out, e
	}
	if d.Role != "human" {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	// shortcut: sequential independent transactions, bounded to 20; policy batching requires equivalent per-item authorization.
	for i, item := range in.Items {
		result := CandidateReviewBatchResult{Index: i, Status: 200}
		var r Record
		e = ctx.Err()
		if e == nil {
			r, e = s.ReviewCandidate(ctx, token, item.ID, CandidateReviewInput{Scope: in.Scope, ExpectedRevision: item.ExpectedRevision, Action: item.Action, Content: item.Content, Reason: item.Reason})
		}
		if e != nil {
			result.Code, result.Status = errorResponse(e)
		} else {
			result.Record = &r
		}
		out.Items = append(out.Items, result)
	}
	return out, nil
}
