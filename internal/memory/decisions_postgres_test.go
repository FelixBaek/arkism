package memory

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func pgDecision(t *testing.T, p *Postgres, scope Scope, kind, code string, at time.Time) {
	t.Helper()
	if err := p.Insert(context.Background(), "audit_events", Record{ID: ID(), Scope: scope, Kind: decisionKindPrefix + kind, Status: "active", Revision: 1,
		CreatedAt: at, UpdatedAt: at, Audit: &AuditMetadata{Decision: "allow", ReasonCode: code, Reason: "words that expire"}}); err != nil {
		t.Fatal(err)
	}
}

// The counts are the point of #466: retention deletes the rows and their words, never the counts.
func TestPostgresDecisionCountsSurviveAuditRetention(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	old, now := time.Now().UTC().AddDate(0, 0, -40), time.Now().UTC()
	pgDecision(t, p, scope, "merge_dismiss", "same_meaning", old)
	pgDecision(t, p, scope, "merge_dismiss", "same_meaning", old)
	pgDecision(t, p, scope, "skill_trashed", "", now)
	if err := p.Insert(ctx, "audit_events", Record{ID: ID(), Scope: scope, Kind: "allow:read", Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := p.ExpireAuditEvents(ctx, now.AddDate(0, 0, -30), true); err != nil || n != 2 {
		t.Fatal("retention", n, err)
	}
	for _, r := range pgCollection(t, p, "audit_events") {
		if r.Kind == decisionKindPrefix+"merge_dismiss" {
			t.Fatal("retention kept an old decision row")
		}
	}
	counts, err := p.DecisionDailyCounts(ctx, scope, now.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	want := []DecisionCount{
		{Day: time.Date(old.Year(), old.Month(), old.Day(), 0, 0, 0, 0, time.UTC), Kind: decisionKindPrefix + "merge_dismiss", ReasonCode: "same_meaning", Count: 2},
		{Day: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), Kind: decisionKindPrefix + "skill_trashed", Count: 1},
	}
	if len(counts) != len(want) {
		t.Fatalf("counts = %+v", counts)
	}
	for i := range want {
		if !counts[i].Day.Equal(want[i].Day) || counts[i].Kind != want[i].Kind || counts[i].ReasonCode != want[i].ReasonCode || counts[i].Count != want[i].Count {
			t.Fatalf("count %d = %+v, want %+v", i, counts[i], want[i])
		}
	}
	// The summary a person reads goes through the service on the same counts.
	f := newFixture(t)
	f.svc.Repo = p
	sum, err := f.svc.DecisionSummary(ctx, f.human, scope, "day", 60)
	if err != nil || len(sum.Rows) != 2 || sum.Rows[0].Kind != "merge_dismiss" || sum.Rows[0].Count != 2 {
		t.Fatalf("summary = %+v %v", sum, err)
	}
}

// Decision counts are read under the caller's scopes, written only by the trigger, even for the app role.
func TestPostgresDecisionCountsFollowRLS(t *testing.T) {
	admin := isolatedPostgres(t)
	ctx := context.Background()
	mine, other := Scope{WorkspaceID: "org-a", ProjectID: "project-a"}, Scope{WorkspaceID: "org-b", ProjectID: "project-b"}
	token := testPrincipalToken("decision-reader")
	if err := admin.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "human-a", Role: roleHuman, Token: token, OrganizationID: mine.WorkspaceID, ProjectID: mine.ProjectID, Actions: []string{"db:read", "db:write"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, admin)
	role := ""
	if err := app.pool.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.q().Exec(ctx, "GRANT SELECT ON decision_daily_counts TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	pgDecision(t, admin, other, "wiki_reject", "", time.Now().UTC())
	bound := ContextWithPostgresToken(ctx, token)
	// The app role writes its own decision row; the trigger counts it although the role cannot write counts.
	if err := app.Transaction(bound, func(ctx context.Context, tx Tx) error {
		return recordDecision(ctx, tx, mine, "human-a", "wiki_approve", "page-1", "", "")
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().AddDate(0, 0, -1)
	if got, err := app.DecisionDailyCounts(bound, mine, from); err != nil || len(got) != 1 || got[0].Kind != decisionKindPrefix+"wiki_approve" {
		t.Fatalf("own scope: %+v %v", got, err)
	}
	if got, err := app.DecisionDailyCounts(bound, other, from); err != nil || len(got) != 0 {
		t.Fatalf("another scope's counts leaked: %+v %v", got, err)
	}
	if _, err := app.pool.Exec(bound, "INSERT INTO decision_daily_counts VALUES('org-a','project-a',current_date,'decision:x','',5)"); err == nil {
		t.Fatal("the app role wrote a count directly")
	}
}

// Migration 0005 lands on a database at release 4 and counts from then on.
func TestSchemaMigrationDecisionDailyCountsFromRelease4(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	historicalSchema(t, p, 4)
	var exists bool
	if err := p.pool.QueryRow(ctx, "SELECT to_regclass('arkism.decision_daily_counts') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("release 4 already had the table", exists, err)
	}
	if err := p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if schemaCount(t, p) != 5 {
		t.Fatal("migration 5 not recorded")
	}
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	pgDecision(t, p, scope, "candidate_expired", "automatic", time.Now().UTC())
	got, err := p.DecisionDailyCounts(ctx, scope, time.Now().UTC().AddDate(0, 0, -1))
	if err != nil || len(got) != 1 || got[0].ReasonCode != "automatic" || got[0].Count != 1 {
		t.Fatalf("counts after migration: %+v %v", got, err)
	}
	if err := p.Insert(ctx, "audit_events", Record{ID: ID(), Scope: scope, Kind: "allow:read", Status: "active", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if again, _ := p.DecisionDailyCounts(ctx, scope, time.Now().UTC().AddDate(0, 0, -1)); len(again) != 1 || again[0].Count != 1 {
		t.Fatal("a non-decision audit row was counted", again)
	}
}
