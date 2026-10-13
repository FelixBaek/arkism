package memory

import (
	"errors"
	"strings"
	"testing"
)

// These are the generic memory transition endpoint's allowed edges, not the
// separate review/conflict/supersede/delete/skill-apply operations. The oracle
// is the 2026-09-20 implementation-boundary paragraph in SRS 5.0, not the
// broader target table below it.
var memoryTransitionEdges = map[string]bool{
	"memory_candidates/candidate/approved": true,
	"memory_candidates/candidate/rejected": true,
	"memory_candidates/candidate/expired":  true,
	"memories/approved/active":             true,
	"memories/approved/rejected":           true,
	"memories/approved/expired":            true,
	"memories/active/rejected":             true,
	"memories/active/expired":              true,
}

func transitionFixture(t *testing.T, col, status string) (*fixture, Record) {
	t.Helper()
	f := newFixture(t)
	src := f.source()
	// Seed persisted states so terminal states and invalid outgoing edges can
	// be tested independently of the specialized operations that produce them.
	r := Record{ID: ID(), Scope: scopeA, Kind: "semantic", Status: status, Revision: 3,
		Text: "transition evidence", Sources: refs(src), SourceEventID: src.ID, VerificationStatus: VerificationCandidate, CreatedBy: "human-1",
		CreatedByType: "human", CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt}
	if col == "memories" {
		r.ApprovedBy = "human-1"
		r.VerificationStatus = VerificationVerified
	}
	f.repo.store[col] = map[string]Record{recKey(scopeA, r.ID): r}
	return f, r
}

func TestMemoryTransitionMatrix(t *testing.T) {
	states := []string{"candidate", "approved", "active", "superseded", "conflicted", "rejected", "expired", "deleted"}
	for _, col := range []string{"memory_candidates", "memories"} {
		for _, from := range states {
			if col == "memories" && from == "candidate" {
				continue // A memory is first created approved, never candidate.
			}
			for _, to := range append(append([]string{}, states...), "", "unknown") {
				for _, role := range []string{"human", "agent", "pipeline", "service"} {
					t.Run(col+"/"+from+"/"+to+"/"+role, func(t *testing.T) {
						f, r := transitionFixture(t, col, from)
						// Deliberately grant policy permission to every role: the
						// service must still enforce the independent human gate.
						f.auth.actors["caller"] = actor("reviewer", role, true, allVerbs...)
						before := structuredText(f.repo.store)
						out, err := f.svc.Transition(testCtx, "caller", col, r.ID, ChangeInput{Scope: scopeA, ExpectedRevision: r.Revision, Status: to})
						allowed := role == "human" && memoryTransitionEdges[col+"/"+from+"/"+to]
						if !allowed {
							wantErr := ErrInvalid
							// Generic supersede is rejected before authorization;
							// callers must use the separate atomic Supersede API.
							if role != "human" && !(col == "memories" && to == "superseded") {
								wantErr = ErrDenied
							}
							if !errors.Is(err, wantErr) || structuredText(f.repo.store) != before {
								t.Fatal("forbidden transition succeeded or wrote data", err)
							}
							return
						}
						if err != nil || out.Status != to || out.Revision != r.Revision+1 {
							t.Fatal("allowed transition failed", out, err)
						}
						stored, err := f.repo.Get(testCtx, col, scopeA, r.ID)
						if err != nil || structuredText(stored) != structuredText(out) || len(f.repo.rows("audit_events")) != 2 {
							t.Fatal("state or audit missing", err)
						}
						if col == "memory_candidates" && to == "approved" {
							m, err := f.repo.Get(testCtx, "memories", scopeA, r.ID)
							if err != nil || m.Status != "approved" || m.Proposal == nil || m.Proposal.Revision != out.Revision || structuredText(m.Sources) != structuredText(r.Sources) {
								t.Fatal("approved memory/proposal/source link missing", err)
							}
						}
						wantJobs := 0
						if to == "active" {
							wantJobs = 1
						}
						if len(f.embeddingJobs()) != wantJobs {
							t.Fatal("wrong embedding jobs")
						}
					})
				}
			}
		}
	}
}

func TestMemoryTransitionRollbackAndRevision(t *testing.T) {
	for edge := range memoryTransitionEdges {
		// The keys above are a fixed contract table, not derived from the code.
		parts := strings.Split(edge, "/")
		for _, failure := range []string{"audit", "revision"} {
			t.Run(edge+"/"+failure, func(t *testing.T) {
				f, r := transitionFixture(t, parts[0], parts[1])
				before := structuredText(f.repo.store)
				in := ChangeInput{Scope: scopeA, ExpectedRevision: r.Revision, Status: parts[2]}
				want := ErrUnavailable
				if failure == "audit" {
					f.repo.failAudit = true
				} else {
					in.ExpectedRevision--
					want = ErrConflict
				}
				_, err := f.svc.Transition(testCtx, f.human, parts[0], r.ID, in)
				if !errors.Is(err, want) || structuredText(f.repo.store) != before {
					t.Fatal("failed transition changed state, provenance, job or audit", err)
				}
			})
		}
	}
}
