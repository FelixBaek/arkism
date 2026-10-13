package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTracePayloadLineageFailsClosed(t *testing.T) {
	ids, err := tracePayloadSourceIDs(json.RawMessage(`{"memory_context":{"items":[{"memory_provenance":{"source_event_id":"source-1","trace_id":"trace-old"},"sources":[{"source_id":"source-2","locator":"original"}]}]}}`))
	if err != nil || len(ids) != 2 || ids[0] != "source-1" || ids[1] != "source-2" {
		t.Fatalf("lineage: %v %v", ids, err)
	}
	for _, raw := range []string{
		`{"memory_context":{"items":[{"memory_provenance":{"trace_id":"trace-only"}}]}}`,
		`{"memory_context":{"items":[{"id":"memory-without-provenance"}]}}`,
	} {
		if _, err = tracePayloadSourceIDs(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unresolvable lineage accepted: %s: %v", raw, err)
		}
	}
}

func TestPostgresCompletedTraceIsCanonicalIdempotentAndScoped(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{actors: map[string]fakeActor{
		"trace-token": actor("replay-operator", "human", true, allVerbs...),
		"erase-token": actor("erasure-worker", "service", false, "erase", "purge", "read"),
	}}
	svc := &Service{Repo: p, Auth: auth}
	scope := Scope{WorkspaceID: "org-trace", ProjectID: "project-trace", UserID: "user-trace", AgentID: "agent-trace"}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	finish := start.Add(30 * time.Second)
	contextSource := Record{ID: "source-context", Scope: scope, Kind: "document", Status: "active", Revision: 1, Text: "private historical decision", CreatedAt: start, UpdatedAt: start}
	if err := p.Insert(ctx, "sources", contextSource); err != nil {
		t.Fatal("insert context source:", err)
	}
	payload, err := json.Marshal(map[string]any{
		"message": "contact person@example.invalid", "step": 1,
		"memory_context": map[string]any{"items": []any{map[string]any{
			"memory_provenance": map[string]any{"source_event_id": contextSource.ID, "trace_id": "trace-historical"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := TraceIngestInput{Scope: scope, TraceID: "trace-1", ThreadID: "thread-1", LangSmithRunID: "ls-1",
		AgentRun: CompletedAgentRunInput{ID: "run-1", TaskType: "design-review", Outcome: "success", StartedAt: start, FinishedAt: finish, Text: "completed design review"},
		Events: []TraceEventInput{
			{ID: "event-1", Kind: "message", OccurredAt: start.Add(time.Second), Payload: payload},
			{ID: "event-2", Kind: "tool_result", OccurredAt: start.Add(2 * time.Second), Payload: json.RawMessage(`{"result":"unrelated survivor"}`)},
		}}
	out, err := svc.IngestCompletedTrace(ctx, "trace-token", in)
	if err != nil || out.Trace.ID != "trace-1" || out.Run.ID != "run-1" || len(out.SourceEvents) != 2 || out.SourceEvents[0] != (Ref{Collection: "sources", ID: "event-1", Revision: 1}) {
		t.Fatalf("ingest: %+v %v", out, err)
	}
	if again, replayErr := svc.IngestCompletedTrace(ctx, "trace-token", in); replayErr != nil || again.Trace != out.Trace {
		t.Fatalf("idempotent replay: %+v %v", again, replayErr)
	}
	source, err := p.Get(ctx, "sources", scope, "event-1")
	if err != nil || source.Text != string(payload) || len(source.Sources) != 2 || source.Sources[0].SourceID != source.ID || source.Sources[1].SourceID != contextSource.ID || source.Sensitivity != "private" || source.Inspection == nil || !source.Inspection.PersonalData {
		t.Fatalf("source event projection: %+v %v", source, err)
	}
	run, err := p.Get(ctx, "agent_runs", scope, "run-1")
	if err != nil || run.Run == nil || run.Run.Outcome != "success" || len(run.Sources) != 3 || run.Sources[2].SourceID != contextSource.ID || len(run.Relations) != 1 || run.Relations[0].Collection != "execution_traces" {
		t.Fatalf("run projection: %+v %v", run, err)
	}
	var raw []byte
	if err = p.q().QueryRow(ctx, `SELECT payload_raw FROM source_events WHERE id='event-1'`).Scan(&raw); err != nil || string(raw) != string(payload) {
		t.Fatalf("raw payload changed: %q %v", raw, err)
	}
	if err = p.CanonicalEvidence(ctx, scope, "event-1", "trace-1"); err != nil {
		t.Fatal("canonical evidence:", err)
	}
	if err = p.CanonicalEvidence(ctx, scope, contextSource.ID, ""); err != nil {
		t.Fatal("ordinary source evidence:", err)
	}
	var traceCreator string
	if err = p.q().QueryRow(ctx, `SELECT created_by FROM execution_traces WHERE id='trace-1'`).Scan(&traceCreator); err != nil || traceCreator != "replay-operator" {
		t.Fatalf("native trace actor: %q %v", traceCreator, err)
	}
	other := scope
	other.ProjectID = "other-project"
	if err = p.CanonicalEvidence(ctx, other, "event-1", "trace-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-scope evidence leaked: %v", err)
	}
	if traceID, traceErr := p.TraceForAgentRun(ctx, scope, "run-1"); traceErr != nil || traceID != "trace-1" {
		t.Fatalf("run mapping: %q %v", traceID, traceErr)
	}
	activeAt := start
	vector := make([]float64, VectorDimension)
	vector[0] = 1
	lesson := Record{ID: "lesson-1", Scope: scope, Kind: "lesson", Status: "active", Revision: 1, Text: "lifecycle-keyword", Keywords: []string{"lifecycle-keyword"},
		Sources: source.Sources, SourceEventID: source.ID, TraceID: in.TraceID, VerificationStatus: VerificationVerified,
		Relations: []Ref{out.Trace}, CreatedAt: start, UpdatedAt: start, EffectiveAt: &activeAt, CreatedBy: "replay-operator", CreatedByType: "human",
		ApprovedBy: "replay-operator", TrustLevel: "user_verified", Trust: 1,
		Embedding: &Embedding{Provider: "test", Model: "test", Revision: "1", Dimension: VectorDimension, Space: "test:1:1024d", EmbeddedAt: start, ContentHash: ContentHash("lifecycle-keyword\n"), Vector: vector}}
	if err = p.Insert(ctx, "memories", lesson); err != nil {
		t.Fatal("insert lesson:", err)
	}
	beforeDelete, err := svc.Search(ctx, "trace-token", SearchInput{Scope: scope, Query: "lifecycle-keyword", Modes: []string{"exact", "fts"}})
	if err != nil || len(beforeDelete.Items) != 1 || beforeDelete.Items[0].ID != lesson.ID {
		t.Fatalf("lesson not retrievable before deletion: %+v %v", beforeDelete.Items, err)
	}
	var links int
	if err = p.q().QueryRow(ctx, `SELECT count(*) FROM relations WHERE organization_id=$1 AND project_id=$2`, scope.WorkspaceID, scope.ProjectID).Scan(&links); err != nil || links < 5 {
		t.Fatalf("identity links missing: %d %v", links, err)
	}
	changed := in
	changed.AgentRun.Text = "changed replay"
	if _, err = svc.IngestCompletedTrace(ctx, "trace-token", changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	if err = svc.Delete(ctx, "trace-token", "sources", contextSource.ID, ChangeInput{Scope: scope, ExpectedRevision: 1}); err != nil {
		t.Fatal("delete historical context source:", err)
	}
	afterDelete, err := svc.Search(ctx, "trace-token", SearchInput{Scope: scope, Query: "lifecycle-keyword", Modes: []string{"exact", "fts"}})
	if err != nil || len(afterDelete.Items) != 0 {
		t.Fatalf("erased evidence remained retrievable: %+v %v", afterDelete.Items, err)
	}
	quarantined, err := p.Get(ctx, "sources", scope, contextSource.ID)
	if err != nil || !quarantined.PrivacyQuarantined || quarantined.Text == "" {
		t.Fatalf("source was not quarantined before purge: %+v %v", quarantined, err)
	}
	var erasedEvent, erasedTrace bool
	var survivor []byte
	if err = p.q().QueryRow(ctx, `SELECT payload_raw,privacy_quarantined,content_erased FROM source_events WHERE id='event-1'`).Scan(&raw, &erasedEvent, &erasedTrace); err != nil || len(raw) == 0 || !erasedEvent || erasedTrace {
		t.Fatalf("native event quarantine boundary: %q quarantined=%t erased=%t %v", raw, erasedEvent, erasedTrace, err)
	}
	action, err := svc.ClaimPrivacyDeletion(ctx, "erase-token", PrivacyScopeInput{Scope: scope})
	if err != nil {
		t.Fatal("claim privacy deletion:", err)
	}
	receipt := PrivacyRuntimeReceipt{RequestHash: action.RequestHash, Quarantined: true, Verified: true, ExpectedRunIDs: action.ExpectedRunIDs, MatchedRunIDs: action.ExpectedRunIDs, AffectedRunIDs: action.ExpectedRunIDs, Counts: PrivacyRuntimeCounts{Runs: len(action.ExpectedRunIDs)}}
	status, err := svc.RecordPrivacyRuntimeReceipt(ctx, "erase-token", action.DeletionRequestID, PrivacyRuntimeReceiptInput{Scope: scope, ExpectedRevision: action.Revision, LeaseToken: action.LeaseToken, Receipt: receipt})
	if err != nil {
		t.Fatal("record runtime receipt:", err)
	}
	finished := false
	for i := 0; i < 100; i++ {
		if status.PrivacyDeletion.State == privacyWaiting || status.PrivacyDeletion.State == privacyCompleted {
			finished = true
			break
		}
		if status.PrivacyDeletion.State == privacyPurgingDerived || status.PrivacyDeletion.State == privacyVerifying {
			status, err = svc.AdvancePrivacyDeletion(ctx, "erase-token", action.DeletionRequestID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
			if err != nil {
				t.Fatal("advance privacy deletion:", err)
			}
		}
		if err = svc.WorkOnce(ctx, "erase-token"); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			t.Fatal("erasure worker:", err)
		}
		status, err = svc.GetPrivacyDeletion(ctx, "trace-token", action.DeletionRequestID, scope)
		if err != nil {
			t.Fatal("privacy status:", err)
		}
	}
	if !finished {
		t.Fatalf("privacy erasure did not reach retention boundary: %+v", status)
	}
	if err = p.q().QueryRow(ctx, `SELECT payload_raw,content_erased FROM source_events WHERE id='event-1'`).Scan(&raw, &erasedEvent); err != nil || len(raw) != 0 || !erasedEvent {
		t.Fatalf("native event retained erased content: %q %t %v", raw, erasedEvent, err)
	}
	if err = p.q().QueryRow(ctx, `SELECT payload_raw,content_erased FROM execution_traces WHERE id='trace-1'`).Scan(&raw, &erasedTrace); err != nil || len(raw) != 0 || !erasedTrace {
		t.Fatalf("aggregate trace retained erased content: %q %t %v", raw, erasedTrace, err)
	}
	if err = p.q().QueryRow(ctx, `SELECT payload_raw,content_erased FROM source_events WHERE id='event-2'`).Scan(&survivor, &erasedEvent); err != nil || len(survivor) != 0 || !erasedEvent {
		t.Fatalf("same-trace sibling survived aggregate erasure: %q %t %v", survivor, erasedEvent, err)
	}
	if err = p.CanonicalEvidence(ctx, scope, "event-1", "trace-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("erased evidence accepted: %v", err)
	}
	if err = p.CanonicalEvidence(ctx, scope, contextSource.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted ordinary evidence accepted: %v", err)
	}
	if err = p.q().QueryRow(ctx, `SELECT count(*) FROM relations WHERE organization_id=$1 AND project_id=$2 AND ((source_kind IN ('trace','source_event')) OR (target_kind IN ('trace','source_event')))`, scope.WorkspaceID, scope.ProjectID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("erased native graph links remained: %d %v", links, err)
	}
	if _, err = p.Get(ctx, "memories", scope, lesson.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("derived lesson/embedding survived completed erasure: %v", err)
	}
	if _, err = p.Get(ctx, "execution_traces", scope, in.TraceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("trace projection survived completed erasure: %v", err)
	}
	if _, err = p.Get(ctx, "sources", scope, "event-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("copied context source survived completed erasure: %v", err)
	}
	if _, eventErr := p.Get(ctx, "sources", scope, "event-2"); !errors.Is(eventErr, ErrNotFound) {
		t.Fatalf("same-trace sibling projection survived: %v", eventErr)
	}
	var embedded int
	if err = p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE id=$1 AND embedding IS NOT NULL`, lesson.ID).Scan(&embedded); err != nil || embedded != 0 {
		t.Fatalf("derived embedding survived completed erasure: %d %v", embedded, err)
	}
	var audits int
	var bodyFree bool
	if err = p.q().QueryRow(ctx, `SELECT count(*),bool_and(COALESCE(doc->>'text','')='') FROM memory_records WHERE collection='audit_events' AND kind='request:privacy_deletion' AND doc->'target'->>'id'=$1`, action.DeletionRequestID).Scan(&audits, &bodyFree); err != nil || audits != 1 || !bodyFree {
		t.Fatalf("source deletion audit: count=%d body_free=%t %v", audits, bodyFree, err)
	}
}

func TestPostgresTraceRejectsSecretsAndRollsBackCollision(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{actors: map[string]fakeActor{"agent": actor("agent-trace", "agent", true, allVerbs...)}}
	svc := &Service{Repo: p, Auth: auth}
	scope := Scope{WorkspaceID: "org-trace", ProjectID: "project-trace", AgentID: "agent-trace"}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	base := TraceIngestInput{Scope: scope, TraceID: "trace-a", ThreadID: "thread-a",
		AgentRun: CompletedAgentRunInput{ID: "run-a", TaskType: "test", Outcome: "failed", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "failed run"},
		Events:   []TraceEventInput{{ID: "event-a", Kind: "tool_result", OccurredAt: start, Payload: json.RawMessage(`{"error":"safe failure"}`)}}}
	if _, err := svc.IngestCompletedTrace(ctx, "agent", base); err != nil {
		t.Fatal(err)
	}
	collision := base
	collision.TraceID, collision.ThreadID, collision.AgentRun.ID = "trace-b", "thread-b", "run-b"
	if _, err := svc.IngestCompletedTrace(ctx, "agent", collision); !errors.Is(err, ErrConflict) {
		t.Fatalf("event collision accepted: %v", err)
	}
	var count int
	if err := p.q().QueryRow(ctx, `SELECT count(*) FROM execution_traces WHERE id='trace-b'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial trace survived rollback: %d %v", count, err)
	}
	secret := base
	secret.TraceID, secret.ThreadID, secret.AgentRun.ID, secret.Events[0].ID = "trace-secret", "thread-secret", "run-secret", "event-secret"
	secret.Events[0].Payload = json.RawMessage(`{"password":"password=synthetic-secret"}`)
	if _, err := svc.IngestCompletedTrace(ctx, "agent", secret); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("secret accepted: %v", err)
	}
}

func TestPostgresGraphTracksLessonReferencesAndSupersession(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.initTraceSchema(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{WorkspaceID: "org-graph", ProjectID: "project-graph", AgentID: "agent-graph"}
	now := time.Now().UTC()
	old := Record{ID: "memory-old", Scope: scope, Kind: "semantic", Status: "superseded", Revision: 2, Text: "old", CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(ctx, "memories", old); err != nil {
		t.Fatal(err)
	}
	lesson := Record{ID: "lesson-new", Scope: scope, Kind: "lesson", Status: "active", Revision: 1, Text: "lesson", CreatedAt: now, UpdatedAt: now,
		Relations: []Ref{{Collection: "memories", ID: old.ID, Revision: old.Revision}}, Supersedes: []Ref{{Collection: "memories", ID: old.ID, Revision: old.Revision}}}
	if err := p.Insert(ctx, "memories", lesson); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	rows, err := p.q().Query(ctx, `SELECT relation_kind FROM relations WHERE organization_id=$1 AND project_id=$2 AND source_kind='lesson' AND source_id='lesson-new' ORDER BY relation_kind`, scope.WorkspaceID, scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		if err = rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	if err = rows.Err(); err != nil || len(kinds) != 2 || kinds[0] != "references" || kinds[1] != "supersedes" {
		t.Fatalf("normalized graph: %v %v", kinds, err)
	}
}
