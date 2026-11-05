package memory

import (
	"context"
	"strings"
	"time"
)

type CandidateReviewInput struct {
	Automated        bool                     `json:"automated,omitempty"`
	Conflict         *CandidateReviewConflict `json:"conflict,omitempty"`
	OperationID      string                   `json:"operation_id,omitempty"`
	Scope            Scope                    `json:"scope"`
	ExpectedRevision int64                    `json:"expected_revision"`
	Action           string                   `json:"action"`
	Content          string                   `json:"content,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
	ReasonCode       string                   `json:"reason_code,omitempty"`
	// Reach is the reviewer's pick on approve/edit_approve; empty keeps the kind default (memoryReach).
	Reach        string `json:"reach,omitempty"`
	ReachProject string `json:"reach_project,omitempty"`
	// Occurred, on approve/edit_approve, is the reviewer's event span for the memory; {} clears the extracted one.
	Occurred *RecallTimeWindow `json:"occurred,omitempty"`
}
type ReviewDecision struct {
	ReceiptID string    `json:"receipt_id,omitempty" bson:"receipt_id,omitempty"`
	ActorRole string    `json:"actor_role,omitempty" bson:"actor_role,omitempty"`
	Channel   string    `json:"channel,omitempty" bson:"channel,omitempty"`
	Action    string    `json:"action" bson:"action"`
	By        string    `json:"by" bson:"by"`
	At        time.Time `json:"at" bson:"at"`
	Reason    string    `json:"reason,omitempty" bson:"reason,omitempty"`
	// ReasonCode is the quick reason picked for a rejection (see removalReasons).
	ReasonCode string `json:"reason_code,omitempty" bson:"reason_code,omitempty"`
	// Person is a legacy classifier-measurement flag, not proof of physical human presence. AIRisk is the candidate's risk call
	// when the person decided; Overturned marks a decision against it.
	Person     bool   `json:"person,omitempty" bson:"person,omitempty"`
	AIRisk     string `json:"ai_risk,omitempty" bson:"ai_risk,omitempty"`
	Overturned bool   `json:"overturned,omitempty" bson:"overturned,omitempty"`
	// Reach is what the reviewer chose; ReachOverridden marks a choice other than the AI suggestion shown.
	Reach           string `json:"reach,omitempty" bson:"reach,omitempty"`
	ReachOverridden bool   `json:"reach_overridden,omitempty" bson:"reach_overridden,omitempty"`
}

func (s *Service) ReviewCandidate(ctx context.Context, token, id string, in CandidateReviewInput) (Record, error) {
	if in.ExpectedRevision < 1 || id == "" || len(id) > 256 || in.OperationID != "" && !idempotencyPattern.MatchString(in.OperationID) {
		return Record{}, ErrInvalid
	}
	in.ReachProject = strings.ToLower(strings.TrimSpace(in.ReachProject)) // same form SetMemoryReach stores
	status, e := candidateReviewStatus(in)
	if e != nil {
		return Record{}, e
	}
	if in.Conflict != nil && !validCandidateReviewConflict(id, in) {
		return Record{}, ErrInvalid
	}
	if in.Content != "" || in.Reason != "" {
		if e = s.defendSpace(ctx, in.Scope, "review:memory_candidates", &in.Content, &in.Reason); e != nil {
			return Record{}, e
		}
	}
	if containsSecret(structuredText(in)) {
		return Record{}, ErrSecretDetected
	}
	r, e := s.transition(ctx, token, "memory_candidates", id, ChangeInput{Scope: in.Scope, ExpectedRevision: in.ExpectedRevision, Status: status}, &in)
	if e != nil {
		return Record{}, e
	}
	if status == "approved" {
		s.extractEntitiesAfterApproval(ctx, r.ApprovedBy, r)
	}
	return r, nil
}

func candidateReviewStatus(in CandidateReviewInput) (string, error) {
	if in.ReasonCode != "" && (in.Action != "reject" || !removalReasons[in.ReasonCode]) {
		return "", ErrInvalid
	}
	if !validReviewReach(in) || !validReviewOccurred(in) {
		return "", ErrInvalid
	}
	switch in.Action {
	case "approve":
		if in.Content != "" || in.Reason != "" {
			return "", ErrInvalid
		}
	case "edit_approve":
		if strings.TrimSpace(in.Content) == "" || len(in.Content) > 64000 || in.Reason != "" {
			return "", ErrInvalid
		}
	case "reject":
		if strings.TrimSpace(in.Reason) == "" && in.ReasonCode == "" || len(in.Reason) > 2000 || in.Content != "" {
			return "", ErrInvalid
		}
		return "rejected", nil
	default:
		return "", ErrInvalid
	}
	return "approved", nil
}

// validReviewOccurred: a reviewer's event span goes only with an approval; {} clears it.
func validReviewOccurred(in CandidateReviewInput) bool {
	return in.Occurred == nil || in.Action != "reject" && (*in.Occurred == RecallTimeWindow{} || validOccurred(in.Occurred, time.Now()))
}

// validReviewReach: reach goes only with an approval, and project needs its project key.
func validReviewReach(in CandidateReviewInput) bool {
	switch in.Reach {
	case "":
		return in.ReachProject == ""
	case ReachPersonal, ReachGeneral:
		return in.ReachProject == "" && in.Action != "reject"
	case ReachProject:
		return in.ReachProject != "" && validProjectKey(in.ReachProject) && in.Action != "reject"
	}
	return false
}

type CandidateReviewBatchItem struct {
	Automated        bool                     `json:"automated,omitempty"`
	Conflict         *CandidateReviewConflict `json:"conflict,omitempty"`
	OperationID      string                   `json:"operation_id,omitempty"`
	ID               string                   `json:"id"`
	ExpectedRevision int64                    `json:"expected_revision"`
	Action           string                   `json:"action"`
	Content          string                   `json:"content,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
	ReasonCode       string                   `json:"reason_code,omitempty"`
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
	if d.Role != roleHuman {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	// shortcut: sequential independent transactions, bounded to 20; policy batching requires equivalent per-item authorization.
	for i, item := range in.Items {
		result := CandidateReviewBatchResult{Index: i, Status: 200}
		var r Record
		e = ctx.Err()
		if e == nil {
			r, e = s.ReviewCandidate(ctx, token, item.ID, CandidateReviewInput{Automated: item.Automated, Conflict: item.Conflict, OperationID: item.OperationID, Scope: in.Scope, ExpectedRevision: item.ExpectedRevision, Action: item.Action, Content: item.Content, Reason: item.Reason, ReasonCode: item.ReasonCode})
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
