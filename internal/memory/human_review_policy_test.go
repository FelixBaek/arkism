package memory

import (
	"errors"
	"testing"
)

// Authorship and event names are model-supplied evidence labels, not an
// authenticated human decision to store the extracted wording.
func TestCaptureAlwaysWaitsForHumanDecision(t *testing.T) {
	for _, basis := range []string{"explicit_user", "inferred", "assistant_result"} {
		for _, event := range []string{"turn_completed", "explicit_remember"} {
			t.Run(basis+"/"+event, func(t *testing.T) {
				f := newFixture(t)
				claim := explicit("앞으로 답변은 한국어로 작성한다")
				claim.Basis, claim.Risk = basis, "low"
				in := captureInput(scopeA, "owner-extraction", claim)
				in.Event.Type = event
				items := f.capture(f.human, in)
				if len(items) != 1 || items[0].Status != "candidate" || items[0].MemoryID != "" || len(activeMemories(f, scopeA)) != 0 {
					t.Fatal("capture self-approved", items)
				}
				c := f.get(f.human, "memory_candidates", scopeA, items[0].CandidateID)
				if c.Review != nil {
					t.Fatal("capture recorded a human decision", c.Review)
				}
				hits := f.search(f.human, SearchInput{Scope: scopeA, Query: claim.Text, Modes: []string{"exact", "fts", "vector"}, Limit: 5, MaxChars: 4000})
				for _, hit := range hits.Items {
					if hit.ID == c.ID {
						t.Fatal("unapproved candidate recalled")
					}
				}
				m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
				if err != nil || m.Status != "active" || m.Review == nil || !m.Review.Person {
					t.Fatal("explicit human review failed", m, err)
				}
			})
		}
	}
}

func TestServicePolicyCannotDecideCandidates(t *testing.T) {
	for _, action := range []string{"approve", "edit_approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			f, c, in := autoReviewFixture(t)
			in.Action = action
			if action == "edit_approve" {
				in.Content = "rewritten proposal"
			}
			if action == "reject" {
				in.Reason = "model decision"
			}
			if _, err := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); !errors.Is(err, ErrDenied) {
				t.Fatal("service decision accepted", err)
			}
			if _, err := f.svc.ReviewCandidates(testCtx, "reviewer", CandidateReviewBatchInput{Scope: scopeA, Items: []CandidateReviewBatchItem{{ID: c.ID, ExpectedRevision: c.Revision, Action: action, Content: in.Content, Reason: in.Reason, OperationID: in.OperationID}}}); !errors.Is(err, ErrDenied) {
				t.Fatal("service batch decision accepted", err)
			}
			if _, err := f.svc.Transition(testCtx, "reviewer", "memory_candidates", c.ID, ChangeInput{Scope: scopeA, ExpectedRevision: c.Revision, Status: "approved"}); !errors.Is(err, ErrDenied) {
				t.Fatal("legacy transition bypassed review", err)
			}
			if got := f.get(f.human, "memory_candidates", scopeA, c.ID); got.Status != c.Status || got.Revision != c.Revision || got.Text != c.Text {
				t.Fatal("denied review mutated candidate", got)
			}
		})
	}
}
