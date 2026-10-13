package memory

import (
	"context"
	"testing"
)

type skillSourceReads struct {
	Tx
	calls int
}

func (s *skillSourceReads) SourceMetadata(ctx context.Context, scope Scope, ids []string, text bool) ([]Record, error) {
	s.calls++
	return s.Tx.SourceMetadata(ctx, scope, ids, text)
}

func TestActiveSkillChecksIdenticalSourcesOnce(t *testing.T) {
	for _, mode := range []string{"same", "different", "bad_span", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "design-review", Path: "/skills/design-review/SKILL.md", Content: "# Design review\nInspect the interface.", Analysis: detailedSkillAnalysis()})
			if err != nil {
				t.Fatal(err)
			}
			version := imported.Record
			if mode == "different" {
				version.Sources = refs(f.source())
			}
			if mode == "bad_span" {
				version.Sources = append([]SourceRef(nil), version.Sources...)
				span := *version.Sources[0].Span
				span.ContentHash = ContentHash("other content")
				version.Sources[0].Span = &span
			}
			if mode == "revoked" {
				src, err := f.repo.Get(testCtx, "sources", scopeA, version.Sources[0].SourceID)
				if err != nil {
					t.Fatal(err)
				}
				src.Status = "deleted"
				if err = f.repo.Replace(testCtx, "sources", src, src.Revision); err != nil {
					t.Fatal(err)
				}
			}
			spy := &skillSourceReads{Tx: f.repo}
			err = checkActiveSkill(testCtx, spy, version)
			rejected := mode == "bad_span" || mode == "revoked"
			if (err != nil) != rejected {
				t.Fatalf("unexpected eligibility: %v", err)
			}
			want := 1
			if mode == "different" || mode == "bad_span" {
				want = 2
			}
			if spy.calls != want {
				t.Fatalf("source reads=%d, want %d", spy.calls, want)
			}
		})
	}
}
