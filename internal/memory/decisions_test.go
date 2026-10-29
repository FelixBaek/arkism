package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// DecisionDailyCounts stands in for the decision_daily_counts trigger: one count per decision row by day, kind and code.
func (f *fakeRepo) DecisionDailyCounts(_ context.Context, scope Scope, from time.Time) ([]DecisionCount, error) {
	at := map[[3]string]int{}
	out := []DecisionCount{}
	for _, r := range f.rows("audit_events") {
		if !strings.HasPrefix(r.Kind, decisionKindPrefix) || r.Scope.WorkspaceID != scope.WorkspaceID || r.Scope.ProjectID != scope.ProjectID {
			continue
		}
		day := time.Date(r.UpdatedAt.Year(), r.UpdatedAt.Month(), r.UpdatedAt.Day(), 0, 0, 0, 0, time.UTC)
		if day.Before(from) {
			continue
		}
		key := [3]string{day.Format(time.DateOnly), r.Kind, r.Audit.ReasonCode}
		if i, ok := at[key]; ok {
			out[i].Count++
			continue
		}
		at[key] = len(out)
		out = append(out, DecisionCount{Day: day, Kind: r.Kind, ReasonCode: r.Audit.ReasonCode, Count: 1})
	}
	return out, nil
}

// decisionsOf returns the audit metadata of every counted decision of one kind.
func decisionsOf(f *fakeRepo, kind string) []Record {
	var out []Record
	for _, r := range f.rows("audit_events") {
		if r.Kind == decisionKindPrefix+kind {
			out = append(out, r)
		}
	}
	return out
}

// requireDecision fails unless exactly one decision of kind was counted, with that reason code.
func requireDecision(t *testing.T, f *fakeRepo, kind, code string) Record {
	t.Helper()
	got := decisionsOf(f, kind)
	if len(got) != 1 {
		var all []string
		for _, r := range f.rows("audit_events") {
			if strings.HasPrefix(r.Kind, decisionKindPrefix) {
				all = append(all, r.Kind+"/"+r.Audit.ReasonCode)
			}
		}
		t.Fatalf("decision %s counted %d times, want 1; decisions: %v", kind, len(got), all)
	}
	if got[0].Audit == nil || got[0].Audit.ReasonCode != code {
		t.Fatalf("decision %s reason code = %+v, want %q", kind, got[0].Audit, code)
	}
	return got[0]
}

func TestRecordDecisionBoundsTheReasonAndCode(t *testing.T) {
	repo := newFakeRepo()
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	long := strings.Repeat("가", decisionReasonMax+20)
	err := repo.Transaction(context.Background(), func(ctx context.Context, tx Tx) error {
		if err := recordDecision(ctx, tx, scope, "u", "Bad Kind", "x", "", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad kind: %v", err)
		}
		if err := recordDecision(ctx, tx, scope, "u", "skill_trashed", "a", "Not A Code!", "  "+long+"  "); err != nil {
			return err
		}
		return recordDecision(ctx, tx, scope, "u", "skill_restored", "b", "kept", "비밀번호: hunter2")
	})
	if err != nil {
		t.Fatal(err)
	}
	r := requireDecision(t, repo, "skill_trashed", "other")
	if n := len([]rune(r.Audit.Reason)); n != decisionReasonMax || r.Target == nil || r.Target.ID != "a" {
		t.Fatalf("reason runes = %d, target %+v", n, r.Target)
	}
	if r := requireDecision(t, repo, "skill_restored", "kept"); r.Audit.Reason != "" {
		t.Fatalf("a reason carrying a secret was kept: %q", r.Audit.Reason)
	}
}

func TestPeriodStart(t *testing.T) {
	d := time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC) // a Thursday
	for period, want := range map[string]string{"day": "2026-10-29", "week": "2026-10-26", "month": "2026-10-01"} {
		if got := periodStart(d, period).Format(time.DateOnly); got != want {
			t.Fatalf("%s: %s, want %s", period, got, want)
		}
	}
}

func TestDecisionSummaryCountsByKindCodeAndPeriod(t *testing.T) {
	f := newFixture(t)
	repo, svc := f.repo, f.svc
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	now := time.Now().UTC()
	add := func(kind, code string, at time.Time) {
		t.Helper()
		if err := repo.Insert(context.Background(), "audit_events", Record{ID: ID(), Scope: scope, Kind: decisionKindPrefix + kind, Status: "active", Revision: 1,
			CreatedAt: at, UpdatedAt: at, Audit: &AuditMetadata{Decision: "allow", ReasonCode: code}}); err != nil {
			t.Fatal(err)
		}
	}
	add("merge_dismiss", "same_meaning", now)
	add("merge_dismiss", "same_meaning", now)
	add("merge_dismiss", "", now)
	add("skill_trashed", "", now.AddDate(0, 0, -40)) // outside 30 days
	got, err := svc.DecisionSummary(context.Background(), f.human, scope, "month", 30)
	if err != nil {
		t.Fatal(err)
	}
	month := periodStart(now, "month").Format(time.DateOnly)
	want := map[string]int64{"merge_dismiss/same_meaning": 2, "merge_dismiss/": 1}
	if got.Authority != AuthorityDataOnly || len(got.Rows) != len(want) {
		t.Fatalf("summary = %+v", got)
	}
	for _, r := range got.Rows {
		if r.Period != month || want[r.Kind+"/"+r.ReasonCode] != r.Count {
			t.Fatalf("row %+v, want %v in %s", r, want, month)
		}
	}
	if got, _ := svc.DecisionSummary(context.Background(), f.human, scope, "", 0); got.Period != "week" || len(got.Rows) != 2 {
		t.Fatalf("defaults: %+v", got)
	}
	for _, bad := range []struct {
		period string
		days   int
	}{{"year", 30}, {"day", -1}, {"day", decisionSummaryMax + 1}} {
		if _, err := svc.DecisionSummary(context.Background(), f.human, scope, bad.period, bad.days); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if _, err := svc.DecisionSummary(context.Background(), f.agent, scope, "day", 7); err == nil {
		t.Fatal("an agent read what people decided")
	}
}

func TestReportDecisionAcceptsOnlyAIHelpFromAPerson(t *testing.T) {
	f := newFixture(t)
	repo, svc := f.repo, f.svc
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	ctx := context.Background()
	if err := svc.ReportDecision(ctx, f.human, DecisionReport{Scope: scope, Kind: "ai_help_accepted", TargetID: "c1"}); err != nil {
		t.Fatal(err)
	}
	requireDecision(t, repo, "ai_help_accepted", "")
	if err := svc.ReportDecision(ctx, f.human, DecisionReport{Scope: scope, Kind: "skill_trashed", TargetID: "c1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a server-side kind was accepted from the client: %v", err)
	}
	if err := svc.ReportDecision(ctx, f.agent, DecisionReport{Scope: scope, Kind: "ai_help_discarded", TargetID: "c1"}); err == nil {
		t.Fatal("an agent reported a person's decision")
	}
	if len(decisionsOf(repo, "ai_help_discarded")) != 0 {
		t.Fatal("a refused report was counted")
	}
}
