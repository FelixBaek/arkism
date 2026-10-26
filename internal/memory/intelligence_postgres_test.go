package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Consecutive skill.usage traces in one thread build a Tacit Map edge whose
// count, confidence and last_seen come only from live evidence traces.
func TestPostgresTacitMapFromSkillUsage(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{WorkspaceID: "org-tacit", ProjectID: "project-tacit", UserID: "user-tacit", AgentID: "codex"}
	skillScope := scope
	skillScope.AgentID = ""
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err := p.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "owner", Role: "human", Token: testPrincipalToken("h"), OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	// Run as the RLS-restricted application role, like production.
	app := restrictedPostgres(t, p)
	var appRole string
	if err := app.pool.QueryRow(ctx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := app.WithToken(testPrincipalToken("h")) // memoryd binds each request's token the same way
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: bound, Auth: auth}
	for _, id := range []string{"illustration", "mockup", "listing"} {
		hash := ContentHash(id)
		if err := p.Insert(ctx, colSkills, Record{ID: id, Scope: skillScope, Kind: "skill", Status: "active", Revision: 1, Text: id, VersionHistory: []string{id + "-v1"}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := p.Insert(ctx, colSkillVersions, Record{ID: id + "-v1", Scope: skillScope, Kind: "skill_version", Status: "active", Revision: 1, Text: id, SkillID: id, Analysis: &SkillAnalysis{SkillID: id, ContentHash: hash}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	use := func(thread, skill string) string {
		t.Helper()
		n++
		start := now.Add(time.Duration(n) * time.Minute)
		payload, _ := json.Marshal(SkillUsage{SkillID: skill, Version: 1, VersionID: skill + "-v1", ContentHash: ContentHash(skill), Runtime: "codex", Status: "success", StartedAt: start, CompletedAt: start.Add(time.Second)})
		id := fmt.Sprintf("trace-%d", n)
		in := TraceIngestInput{Scope: scope, TraceID: id, ThreadID: thread,
			AgentRun: CompletedAgentRunInput{ID: "run-" + id, TaskType: "skill_run", Outcome: "success", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "skill usage"},
			Events:   []TraceEventInput{{ID: "event-" + id, Kind: skillUsageEventKind, OccurredAt: start, Payload: payload}}}
		if _, err := svc.IngestCompletedTrace(ctx, testPrincipalToken("h"), in); err != nil {
			t.Fatal("ingest", id, err)
		}
		// Replays are idempotent and must not add evidence.
		if _, err := svc.IngestCompletedTrace(ctx, testPrincipalToken("h"), in); err != nil {
			t.Fatal("replay", id, err)
		}
		return id
	}
	var laterTraces []string
	for _, thread := range []string{"s1", "s2", "s3"} {
		use(thread, "illustration")
		laterTraces = append(laterTraces, use(thread, "mockup"))
		use(thread, "listing")
	}
	use("s4", "mockup") // a new thread starts with no predecessor

	page, err := svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "skill:illustration", "")
	if err != nil || len(page.Items) != 1 || page.Authority != "data_only_untrusted" {
		t.Fatalf("edges: %+v %v", page, err)
	}
	e := page.Items[0]
	if e.Target != "skill:mockup" || e.RelationType != tacitFollowedBy || e.OccurrenceCount != 3 || e.Confidence != 0.875 || e.Status != "inferred" || len(e.EvidenceRefs) != 3 {
		t.Fatalf("edge: %+v", e)
	}
	all, err := svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "", "")
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("all edges (illustration->mockup, mockup->listing): %+v %v", all, err)
	}
	other := scope
	other.AgentID = "claude_code"
	if leaked, err := svc.ListTacitEdges(ctx, testPrincipalToken("h"), other, "", ""); err != nil || len(leaked.Items) != 0 {
		t.Fatalf("cross-scope leak: %+v %v", leaked, err)
	}

	// Erasing one evidence trace removes that observation.
	if _, err = p.q().Exec(ctx, `UPDATE execution_traces SET content_erased=true WHERE id=$1`, laterTraces[0]); err != nil {
		t.Fatal(err)
	}
	page, _ = svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "skill:illustration", tacitFollowedBy)
	if len(page.Items) != 1 || page.Items[0].OccurrenceCount != 2 || page.Items[0].Status != "candidate" {
		t.Fatalf("erased evidence still counted: %+v", page.Items)
	}
	dry, err := svc.Improve(ctx, testPrincipalToken("h"), ImprovementInput{Scope: scope, Target: improvementTacitEdge, ID: "skill:illustration|often_followed_by|skill:mockup", DryRun: true})
	if err != nil || dry.Promotable || dry.Reason != "insufficient_distinct_runs" {
		t.Fatalf("2 runs must not promote: %+v %v", dry, err)
	}
	use("s5", "illustration")
	use("s5", "mockup")
	got, err := svc.Improve(ctx, testPrincipalToken("h"), ImprovementInput{Scope: scope, Target: improvementTacitEdge, ID: "skill:illustration|often_followed_by|skill:mockup"})
	if err != nil || !got.Promoted || got.DistinctRuns != 3 {
		t.Fatalf("promote: %+v %v", got, err)
	}
	page, _ = svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "skill:illustration", "")
	if page.Items[0].Status != "confirmed" || page.Items[0].ConfirmedAt == nil || len(page.Items[0].EvidenceRefs) != 3 {
		t.Fatalf("confirmed edge keeps its evidence: %+v", page.Items[0])
	}
	// The UI and promotion path must still reach edges beyond the old 100-row cap.
	liveRefs := page.Items[0].EvidenceRefs
	ids := []string{}
	for _, ref := range liveRefs {
		ids = append(ids, ref.ID)
	}
	metadata, _ := json.Marshal(map[string]any{"evidence": ids})
	if err := p.insertGraphEntity(ctx, scope, "skill", "bulk"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 103; i++ {
		target := fmt.Sprintf("bulk-%03d", i)
		if err := p.insertGraphEntity(ctx, scope, "skill", target); err != nil {
			t.Fatal(err)
		}
		_, err := p.q().Exec(ctx, `INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind,metadata)
VALUES($1,$2,$3,$4,'skill','bulk','skill',$5,$6,$7)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, target, tacitFollowedBy, metadata)
		if err != nil {
			t.Fatal(err)
		}
	}
	bulk, err := svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "skill:bulk", "")
	if err != nil || len(bulk.Items) != 103 || bulk.Items[102].Target != "skill:bulk-102" {
		t.Fatalf("all bulk edges: count=%d err=%v", len(bulk.Items), err)
	}
	got, err = svc.Improve(ctx, testPrincipalToken("h"), ImprovementInput{Scope: scope, Target: improvementTacitEdge, ID: "skill:bulk|often_followed_by|skill:bulk-102"})
	if err != nil || !got.Promoted {
		t.Fatalf("last edge promotion: %+v %v", got, err)
	}
	// Storage must reject a stale promotion after evidence is removed.
	if _, err = p.q().Exec(ctx, `UPDATE execution_traces SET privacy_quarantined=true WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = p.ConfirmTacitEdge(ctx, scope, "skill:bulk", "skill:bulk-101", tacitFollowedBy, "owner", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("lost evidence confirmation: %v", err)
	}
	if _, err = svc.ListTacitEdges(ctx, testPrincipalToken("h"), scope, "no-colon", ""); err == nil {
		t.Fatal("malformed node accepted")
	}
}

// A human-written failed skill.usage trace becomes an automated_check
// evaluation and a lesson candidate under the hub's service principal.
func TestPostgresFailedUsageBecomesReflection(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{WorkspaceID: "org-refl", ProjectID: "project-refl", UserID: "user-refl", AgentID: "designer"}
	skillScope := scope
	skillScope.AgentID = ""
	for token, principal := range map[string][2]string{testPrincipalToken("h"): {"demo-user", "human"}, testPrincipalToken("w"): {"arkism-worker", "service"}} {
		if err := p.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: principal[0], Role: principal[1], Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
	}
	app := restrictedPostgres(t, p)
	var appRole string
	if err := app.pool.QueryRow(ctx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: app, Auth: auth, ReflectionToken: testPrincipalToken("w"), CandidateDailyLimit: 50}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err = p.Insert(ctx, colSkills, Record{ID: "etsy-listing", Scope: skillScope, Kind: "skill", Status: "active", Revision: 1, Text: "etsy", VersionHistory: []string{"etsy-v1"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = p.Insert(ctx, colSkillVersions, Record{ID: "etsy-v1", Scope: skillScope, Kind: "skill_version", Status: "active", Revision: 1, Text: "etsy", SkillID: "etsy-listing", Analysis: &SkillAnalysis{SkillID: "etsy-listing", ContentHash: ContentHash("etsy")}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ingest := func(i int, status string) TraceIngestResult {
		t.Helper()
		start := now.Add(time.Duration(i) * time.Minute)
		u := SkillUsage{SkillID: "etsy-listing", Version: 1, VersionID: "etsy-v1", ContentHash: ContentHash("etsy"), Runtime: "demo-app", Status: status, StartedAt: start, CompletedAt: start.Add(time.Second)}
		if status != "success" {
			u.ErrorCode = "title_too_long"
		}
		payload, _ := json.Marshal(u)
		id := fmt.Sprintf("usage-%d", i)
		in := TraceIngestInput{Scope: scope, TraceID: id, ThreadID: "demo-session-1",
			AgentRun: CompletedAgentRunInput{ID: "run-" + id, TaskType: "skill:etsy-listing", Outcome: "failed", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "skill usage"},
			Events:   []TraceEventInput{{ID: "event-" + id, Kind: skillUsageEventKind, OccurredAt: start, Payload: payload}}}
		out, err := svc.IngestCompletedTrace(ContextWithPostgresToken(ctx, testPrincipalToken("h")), testPrincipalToken("h"), in)
		if err != nil {
			t.Fatal("ingest", err)
		}
		return out
	}
	first := ingest(1, "failure")
	if first.LearningError != "" || first.Learning == nil || first.Learning.Learning == nil || first.Learning.Learning.ReviewSuggested {
		t.Fatalf("first failure: %+v %q", first.Learning, first.LearningError)
	}
	ev, err := p.Get(ctx, "evaluations", scope, first.Learning.Evaluation.ID)
	if err != nil || ev.CreatedBy != "arkism-worker" || ev.Evaluation.EvalType != "automated_check" || *ev.Evaluation.Passed {
		t.Fatalf("evaluator must be the hub service principal: %+v %v", ev, err)
	}
	// A replay reuses the evaluation and adds no lesson.
	again := ingest(1, "failure")
	if again.Learning == nil || again.Learning.Evaluation != first.Learning.Evaluation || again.Learning.Learning != nil {
		t.Fatalf("replay: %+v %q", again.Learning, again.LearningError)
	}
	if ok := ingest(2, "success"); ok.Learning != nil {
		t.Fatal("success produced a lesson")
	}
	ingest(3, "failure")
	third := ingest(4, "validation_failed")
	if third.Learning == nil || third.Learning.Learning == nil {
		t.Fatalf("validation_failed: %+v %q", third.Learning, third.LearningError)
	}
	human, _ := app.WithToken(testPrincipalToken("h"))
	view := &Service{Repo: human, Auth: auth}
	page, err := view.ListReflections(ctx, testPrincipalToken("h"), scope, "skill:etsy-listing")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("failure and validation_failed lessons: %+v %v", page, err)
	}
	for _, r := range page.Items {
		if r.Status != "candidate" {
			t.Fatalf("lessons stay candidates: %+v", r)
		}
	}
	// The skills table counts failure and validation_failed runs apart.
	uses, err := p.SkillUsageSince(ctx, scope, now.Add(-time.Minute))
	if err != nil || len(uses) != 1 || uses[0].Count != 4 || uses[0].Failures != 3 {
		t.Fatalf("usage failures: %+v %v", uses, err)
	}
}

// A runtime that reads skills from the operator's canonical scope records
// usage in its own scope; validation finds the skill there, read-only.
func TestPostgresUsageResolvesCanonicalSkillScope(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	run := Scope{WorkspaceID: "demo-app", ProjectID: "default", UserID: "human-local", AgentID: "bot_designer"}
	canonical := Scope{WorkspaceID: "ws-canon", ProjectID: "skills", UserID: "owner"}
	grants := []PrincipalGrant{
		{PrincipalID: "demo-operator", Role: "human", Token: testPrincipalToken("b"), OrganizationID: run.WorkspaceID, ProjectID: run.ProjectID, Actions: []string{"*"}},
		{PrincipalID: "demo-operator", Role: "human", Token: testPrincipalToken("b"), OrganizationID: canonical.WorkspaceID, ProjectID: canonical.ProjectID, Actions: []string{"db:read", "read:skills", "read:skill_versions", "read:sources"}},
	}
	for _, g := range grants {
		if err := p.ProvisionPrincipal(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	app := restrictedPostgres(t, p)
	var appRole string
	if err := app.pool.QueryRow(ctx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err = p.Insert(ctx, colSkills, Record{ID: "pdf", Scope: canonical, Kind: "skill", Status: "active", Revision: 1, Text: "pdf", VersionHistory: []string{"pdf-v1"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = p.Insert(ctx, colSkillVersions, Record{ID: "pdf-v1", Scope: canonical, Kind: "skill_version", Status: "active", Revision: 1, Text: "pdf", SkillID: "pdf", Analysis: &SkillAnalysis{SkillID: "pdf", ContentHash: ContentHash("pdf")}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(SkillUsage{SkillID: "pdf", Version: 1, VersionID: "pdf-v1", ContentHash: ContentHash("pdf"), Runtime: "demo-app", Status: "success", StartedAt: now, CompletedAt: now.Add(time.Second)})
	in := TraceIngestInput{Scope: run, TraceID: "usage-canon", ThreadID: "demo-session-1",
		AgentRun: CompletedAgentRunInput{ID: "run-canon", TaskType: "skill:pdf", Outcome: "success", StartedAt: now, FinishedAt: now.Add(time.Second), Text: "skill usage"},
		Events:   []TraceEventInput{{ID: "event-canon", Kind: skillUsageEventKind, OccurredAt: now, Payload: payload}}}
	bctx := ContextWithPostgresToken(ctx, testPrincipalToken("b"))
	without := &Service{Repo: app, Auth: auth}
	if _, err = without.IngestCompletedTrace(bctx, testPrincipalToken("b"), in); !errors.Is(err, ErrConflict) {
		t.Fatal("usage of a skill outside the run scope must fail without a canonical scope", err)
	}
	with := &Service{Repo: app, Auth: auth, SkillScope: canonical}
	if _, err = with.IngestCompletedTrace(bctx, testPrincipalToken("b"), in); err != nil {
		t.Fatal("canonical skill usage", err)
	}
	bad := in
	bad.TraceID, bad.AgentRun.ID = "usage-bad", "run-bad"
	badPayload, _ := json.Marshal(SkillUsage{SkillID: "pdf", Version: 1, VersionID: "pdf-v1", ContentHash: ContentHash("other"), Runtime: "demo-app", Status: "success", StartedAt: now, CompletedAt: now.Add(time.Second)})
	bad.Events = []TraceEventInput{{ID: "event-bad", Kind: skillUsageEventKind, OccurredAt: now, Payload: badPayload}}
	if _, err = with.IngestCompletedTrace(bctx, testPrincipalToken("b"), bad); !errors.Is(err, ErrConflict) {
		t.Fatal("hash mismatch against the canonical version accepted", err)
	}
}
