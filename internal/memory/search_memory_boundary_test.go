package memory

import (
	"fmt"
	"testing"
	"time"
)

// TC-SEC-004 / FR-020: ineligible records must not consume the finite search
// candidate window, even if an old/imported row still says status=active.
func TestPostgresSearchMemoryEligibilityBeforeLimit(t *testing.T) {
	p := isolatedPostgres(t)
	p.SetVectorMaxDistance(0.7)
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, invalid := range []string{"unapproved", "erased", "expired", "future", "quarantined", "workspace", "project", "user", "agent", "reach", "kind", "tag"} {
		t.Run(invalid, func(t *testing.T) {
			scope := scopeA
			scope.ProjectID = invalid
			for i := range 5 {
				r := Record{ID: fmt.Sprintf("%s-memory-%02d", invalid, i), Scope: scope, Status: "active", Kind: "decision", Revision: 1,
					ApprovedBy: "reviewer", Text: "eligibilityneedle", Keywords: []string{"eligibilityneedle"}, Tags: []string{"allowed"}, CreatedAt: now, UpdatedAt: now.Add(time.Duration(i) * time.Second)}
				if i == 0 {
					r.ID = invalid + "-zz-eligible"
				}
				if i > 0 {
					switch invalid {
					case "unapproved":
						r.ApprovedBy = ""
					case "erased":
						r.ContentErased = true
					case "expired":
						r.ExpiresAt = &past
					case "future":
						r.EffectiveAt = &future
					case "quarantined":
						r.PrivacyQuarantined = true
					case "workspace":
						r.Scope.WorkspaceID = "other-workspace"
					case "project":
						r.Scope.ProjectID = "other-project"
					case "user":
						r.Scope.UserID = "other-user"
					case "agent":
						r.Scope.AgentID = "other-agent"
					case "reach":
						r.Reach, r.ReachProject = ReachProject, "owner/elsewhere"
					case "kind":
						r.Kind = "semantic"
					case "tag":
						r.Tags = []string{"excluded"}
					}
				}
				vector := make([]float64, VectorDimension)
				vector[0] = 1
				if i == 0 {
					// Invalid rows are closer than the legitimate match. They must
					// neither consume LIMIT nor tighten the relative vector cutoff.
					vector[0], vector[1] = 0.8, 0.6
				}
				r.Embedding = &Embedding{Space: "boundary-test", Dimension: VectorDimension, Vector: vector, ContentHash: ContentHash(recordContent(r)), EmbeddedAt: now}
				if err := p.Insert(testCtx, colMemories, r); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"exact", "fts", "vector"} {
				t.Run(mode, func(t *testing.T) {
					vector := make([]float64, VectorDimension)
					vector[0] = 1
					criteria := SearchCriteria{ProjectKey: "owner/here", Kinds: []string{"decision"}, Filters: &SearchFilters{Tags: []string{"allowed"}}}
					lanes, err := p.SearchCollection(testCtx, colMemories, SearchInput{SearchCriteria: criteria, Scope: scope, Query: "eligibilityneedle", Limit: 1, Modes: []string{mode}}, vector, "boundary-test")
					if err != nil {
						t.Fatal(err)
					}
					hits := Fuse(lanes, 1)
					if len(hits) != 1 || hits[0].ID != invalid+"-zz-eligible" {
						t.Fatalf("eligible memory displaced by %s rows in %s lane: %v", invalid, mode, hits)
					}
				})
			}
		})
	}
}
