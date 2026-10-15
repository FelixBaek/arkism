package memory

import (
	"context"
	"strings"
	"time"
)

// AI classifies, people approve (ADR-0022). A classification only orders the review list: it never changes a
// candidate's status or text. A principal with the classifier role can do nothing else: every approval path
// requires a human or service role, and the Postgres guard trigger refuses its other writes.

const roleClassifier = "classifier"

var candidateRisks = map[string]int{"high": 0, "medium": 1, "low": 2}

type CandidateClassification struct {
	Risk         string     `json:"risk" bson:"risk"`
	Reasons      []string   `json:"risk_reasons,omitempty" bson:"risk_reasons,omitempty"`
	EvidenceSpan string     `json:"evidence_span,omitempty" bson:"evidence_span,omitempty"`
	DupHint      string     `json:"dup_hint,omitempty" bson:"dup_hint,omitempty"`
	By           string     `json:"by,omitempty" bson:"by,omitempty"`
	At           *time.Time `json:"at,omitempty" bson:"at,omitempty"`
}

func validClassification(c CandidateClassification) bool {
	if _, ok := candidateRisks[c.Risk]; !ok || len(c.Reasons) > 8 || len(c.EvidenceSpan) > 8000 || len(c.DupHint) > 2000 {
		return false
	}
	for _, r := range c.Reasons {
		if strings.TrimSpace(r) == "" || len(r) > 1000 {
			return false
		}
	}
	return !containsSecret(structuredText(c))
}

type CandidateClassifyInput struct {
	Scope            Scope    `json:"scope"`
	ExpectedRevision int64    `json:"expected_revision"`
	Risk             string   `json:"risk"`
	RiskReasons      []string `json:"risk_reasons,omitempty"`
	DupHint          string   `json:"dup_hint,omitempty"`
}

// ClassifyCandidate records a reviewer's risk call on a waiting candidate. The extractor's evidence span is kept.
func (s *Service) ClassifyCandidate(ctx context.Context, token, id string, in CandidateClassifyInput) (Record, error) {
	c := CandidateClassification{Risk: in.Risk, Reasons: in.RiskReasons, DupHint: in.DupHint}
	if in.ExpectedRevision < 1 || id == "" || len(id) > 256 || !validClassification(c) {
		return Record{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "classify:memory_candidates", in.Scope, id, structuredText(in))
	if e != nil {
		return Record{}, e
	}
	if !d.ContentSafe {
		return Record{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	var out Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, "memory_candidates", in.Scope, id)
		if e != nil {
			return e
		}
		if r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}
		if r.Revision != in.ExpectedRevision || r.Status != "candidate" && r.Status != "conflicted" {
			return ErrConflict
		}
		now := time.Now().UTC()
		if r.Classification != nil {
			c.EvidenceSpan = r.Classification.EvidenceSpan
			if c.DupHint == "" {
				c.DupHint = r.Classification.DupHint
			}
		}
		c.By, c.At = d.PrincipalID, &now
		r.Classification = &c
		r.Revision++
		r.UpdatedAt = now
		if e = tx.Replace(ctx, "memory_candidates", r, in.ExpectedRevision); e != nil {
			return e
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "classify:memory_candidates", id)
	})
	return out, e
}

// overturned: a person approved what was called high risk, or rejected what was called low risk.
func overturned(c *CandidateClassification, action string) bool {
	if c == nil {
		return false
	}
	return c.Risk == "high" && action != "reject" || c.Risk == "low" && action == "reject"
}

type ClassificationReport struct {
	// Decided counts candidates a person reviewed, per AI risk ("none" when unclassified); Overturned counts the
	// ones a person decided against the risk call. Error rate = Overturned[risk] / Decided[risk].
	Decided    map[string]int `json:"decided"`
	Overturned map[string]int `json:"overturned"`
}

// shortcut: tallies the newest 1000 approved and 1000 rejected candidates; page through them if the queue grows past that.
func (s *Service) ClassificationReport(ctx context.Context, token string, scope Scope) (ClassificationReport, error) {
	out := ClassificationReport{Decided: map[string]int{}, Overturned: map[string]int{}}
	if _, e := s.authorize(ctx, token, "review:memory_candidates", scope, "", ""); e != nil {
		return out, e
	}
	for _, status := range []string{"approved", "rejected"} {
		rows, e := s.Repo.ListRecordsByStatus(ctx, scope, "memory_candidates", status, 1000)
		if e != nil {
			return out, e
		}
		for _, r := range rows {
			if r.Review == nil || !r.Review.Person {
				continue
			}
			risk := r.Review.AIRisk
			if risk == "" {
				risk = "none"
			}
			out.Decided[risk]++
			if r.Review.Overturned {
				out.Overturned[risk]++
			}
		}
	}
	return out, nil
}
