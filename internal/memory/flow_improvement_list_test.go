package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
)

func (f *fakeRepo) ListFlowImprovements(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	rows, e := f.ListRecords(ctx, scope, colJobs, 1<<30)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range rows {
		if r.Kind == flowImprovementKind && r.ID > after {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestFlowImprovementListFiltersBeforePagingAndOmitsGenerationMaterial(t *testing.T) {
	f, _, start, _ := improvementFixture(t, false)
	for i := 0; i < 2; i++ {
		start.RequestID = fmt.Sprint("request-", i)
		if _, e := f.svc.StartFlowImprovement(testCtx, f.agent, start); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 150; i++ {
		if e := f.repo.Insert(testCtx, colJobs, Record{ID: fmt.Sprintf("unrelated-%d", i), Scope: scopeA, Kind: "unrelated", Revision: 1}); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, e := f.svc.ListFlowImprovements(testCtx, f.human, scopeA, cursor, 1)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range page.Items {
			if seen[r.ID] || r.FlowImprovement == nil || r.FlowImprovement.Generation.Path != "" || len(r.FlowImprovement.Generation.Lessons) != 0 || r.Text != "" {
				t.Fatal("unexpected body or duplicate", r)
			}
			seen[r.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("stuck cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 2 {
		t.Fatal("missing records", seen)
	}
	f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:crew"}
	if _, e := f.svc.ListFlowImprovements(testCtx, f.human, scopeA, "", 10); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked read", e)
	}
}
