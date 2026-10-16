package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Deleting evidence that a trace read must contaminate that trace: the trace,
// its sibling events and its run follow the context_uses edge.
func TestPostgresPrivacySourceDeletionFollowsContextUse(t *testing.T) {
	p, svc, scope, other := privacyTraceFixture(t)
	impact, err := p.DiscoverPrivacyImpact(testCtx, scope, "source_event", []string{"ctx-src"}, privacyImpactLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(impact.SourceEventIDs, []string{"ctx-src", "event-1", "event-2"}) || !sameStrings(impact.TraceIDs, []string{"trace-1"}) || !sameStrings(impact.ExpectedRunIDs, []string{"run-1"}) {
		t.Fatalf("context-use impact = %+v", impact)
	}
	for _, ref := range impact.Records {
		if strings.HasSuffix(ref.ID, "-other") {
			t.Fatal("impact crossed projects", ref)
		}
	}
	// A bounded discovery fails closed instead of returning a truncated impact.
	for limit := 1; limit < 4; limit++ {
		if _, err := p.DiscoverPrivacyImpact(testCtx, scope, "source_event", []string{"ctx-src"}, limit); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("limit %d: truncated impact accepted: %v", limit, err)
		}
	}
	status, err := svc.RequestPrivacyDeletion(testCtx, "owner", PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "ctx", TargetType: "source_event", TargetIDs: []string{"ctx-src"}, Reason: "owner_request", RetentionPolicyRef: retentionSynthetic})
	if err != nil {
		t.Fatal(err)
	}
	if fence, err := p.PrivacyFence(testCtx, PrivacyFenceInput{Scope: scope, TraceIDs: []string{"trace-1"}}); err != nil || fence.Allowed {
		t.Fatalf("contaminated trace still allowed: %+v %v", fence, err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 1 {
		t.Fatalf("runtime step = %d %v", n, err)
	}
	for i := 0; i < 100 && status.PrivacyDeletion.State != privacyCompleted; i++ {
		if status.PrivacyDeletion.State == privacyPurgingDerived || status.PrivacyDeletion.State == privacyVerifying {
			if status, err = svc.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision}); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.WorkOnce(testCtx, "worker"); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if status, err = svc.GetPrivacyDeletion(testCtx, "owner", status.ID, scope); err != nil {
			t.Fatal(err)
		}
	}
	if status.PrivacyDeletion.State != privacyCompleted || status.JobStatus != JobStatusDone || status.PrivacyDeletion.CompletedAt == nil || status.PrivacyDeletion.Report.BackupExpiryPending {
		t.Fatalf("synthetic deletion did not complete: %+v", status)
	}
	for _, id := range []string{"ctx-src", "event-1", "event-2"} {
		if _, err := p.Get(testCtx, "sources", scope, id); !errors.Is(err, ErrNotFound) {
			t.Fatal("contaminated source still readable", id, err)
		}
		if _, err := p.Get(testCtx, "sources", other, id+"-other"); err != nil {
			t.Fatal("other project source removed", id, err)
		}
	}
	if size, erased := nativeTraceState(t, p, scope, "execution_traces", "trace-1"); size != 0 || !erased {
		t.Fatal("contaminated trace bytes survived")
	}
}

func TestPostgresPrivacyStoreRejectsBadArguments(t *testing.T) {
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	many := make([]string, 65)
	for i := range many {
		many[i] = fmt.Sprintf("id-%d", i)
	}
	for name, call := range map[string]struct {
		scope  Scope
		target string
		ids    []string
		limit  int
	}{
		"bad scope":      {Scope{}, "source_event", []string{"s"}, 10},
		"bad target":     {scope, "memory", []string{"s"}, 10},
		"no ids":         {scope, "trace", nil, 10},
		"too many ids":   {scope, "trace", many, 10},
		"zero limit":     {scope, "trace", []string{"t"}, 0},
		"huge limit":     {scope, "trace", []string{"t"}, privacyImpactLimit + 1},
		"other subject":  {scope, "subject", []string{"someone"}, 10},
		"two subjects":   {scope, "subject", []string{"u", "v"}, 10},
		"bad work id":    {scope, "work_checkpoint", []string{"not a work id!"}, 10},
		"subject no use": {Scope{WorkspaceID: "w", ProjectID: "p"}, "subject", []string{""}, 10},
	} {
		if _, err := p.DiscoverPrivacyImpact(testCtx, call.scope, call.target, call.ids, call.limit); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scope, "trace", []string{"missing"}, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing trace", err)
	}
	// Mutating and verifying steps are only valid inside the request transaction.
	impact := PrivacyImpact{SourceEventIDs: []string{"s"}}
	if err := p.QuarantinePrivacyImpact(testCtx, scope, impact); !errors.Is(err, ErrInvalid) {
		t.Fatal("quarantine outside transaction", err)
	}
	if _, err := p.VerifyPrivacyPurge(testCtx, scope, impact); !errors.Is(err, ErrInvalid) {
		t.Fatal("verify outside transaction", err)
	}
	if err := p.CheckPrivacyOverlap(testCtx, scope, "id", "source_event", impact, false); !errors.Is(err, ErrUnavailable) {
		t.Fatal("overlap outside transaction", err)
	}
	if err := p.checkArtifactHoldBoundary(testCtx, scope, "id"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("artifact boundary outside transaction", err)
	}
	if err := p.CheckArtifactPrivacyHold(testCtx, strings.Repeat("a", 64)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("artifact hold outside transaction", err)
	}
	if err := p.FenceWorkSubject(testCtx, scope); !errors.Is(err, ErrInvalid) {
		t.Fatal("work subject fence outside transaction", err)
	}
	if _, err := p.ClaimPrivacyDeletion(testCtx, scope, time.Now(), 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("claim without lease", err)
	}
	if _, err := p.ClaimPrivacyDeletion(testCtx, scope, time.Now(), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal("claim on empty queue", err)
	}
	if _, err := p.NextPrivacyAdvance(testCtx, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatal("advance on empty queue", err)
	}
	if scopes, err := p.PrivacyRuntimeScopes(testCtx); err != nil || len(scopes) != 0 {
		t.Fatal("runtime scopes on empty queue", scopes, err)
	}
	err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		pp := tx.(*Postgres)
		if err := pp.CheckArtifactPrivacyHold(ctx, "not-a-digest"); !errors.Is(err, ErrUnavailable) {
			t.Error("malformed digest accepted", err)
		}
		if err := pp.CheckPrivacyOverlap(ctx, Scope{}, "id", "source_event", impact, false); !errors.Is(err, ErrUnavailable) {
			t.Error("overlap with bad scope", err)
		}
		if err := pp.QuarantinePrivacyImpact(ctx, Scope{}, impact); !errors.Is(err, ErrInvalid) {
			t.Error("quarantine with bad scope", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 129)
	for i := range ids {
		ids[i] = fmt.Sprintf("s-%d", i)
	}
	for name, in := range map[string]PrivacyFenceInput{
		"bad scope":   {SourceEventIDs: []string{"s"}},
		"empty":       {Scope: scope},
		"too many":    {Scope: scope, SourceEventIDs: ids},
		"bad label":   {Scope: scope, SourceEventIDs: []string{"bad\nid"}},
		"bad run":     {Scope: scope, SourceEventIDs: []string{"s"}, ExpectedRunIDs: []string{""}},
		"bad trace":   {Scope: scope, TraceIDs: []string{strings.Repeat("t", 300)}},
		"control run": {Scope: scope, TraceIDs: []string{"t"}, ExpectedRunIDs: []string{"run\x00"}},
	} {
		if _, err := p.PrivacyFence(testCtx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("fence %s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// The fence must never say "allowed" for evidence it cannot see, including
// rows that exist only in another scope.
func TestPostgresPrivacyFenceFailsClosedOnUnknownEvidence(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	other := scope
	other.UserID = "v"
	if err := p.Insert(testCtx, "sources", Record{ID: "s-1", Scope: other, Kind: "document", Status: statusActive, Revision: 1, Text: "other user", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []PrivacyFenceInput{
		{Scope: scope, SourceEventIDs: []string{"s-1"}},
		{Scope: scope, TraceIDs: []string{"trace-none"}},
	} {
		if res, err := p.PrivacyFence(testCtx, in); !errors.Is(err, ErrConflict) || res.Allowed {
			t.Fatalf("fence on invisible evidence = %+v %v", res, err)
		}
	}
	if res, err := p.PrivacyFence(testCtx, PrivacyFenceInput{Scope: other, SourceEventIDs: []string{"s-1"}}); err != nil || !res.Allowed || len(res.DeletionRequestIDs) != 0 {
		t.Fatalf("clean source fenced = %+v %v", res, err)
	}
}

func TestPostgresPrivacySubjectDiscoveryIsBoundedAndScoped(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	other := scope
	other.UserID = "v"
	for i := 0; i < 3; i++ {
		for _, s := range []Scope{scope, other} {
			if err := p.Insert(testCtx, "sources", Record{ID: fmt.Sprintf("%s-src-%d", s.UserID, i), Scope: s, Kind: "document", Status: statusActive, Revision: 1, Text: "subject data", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
	}
	impact, err := p.DiscoverPrivacyImpact(testCtx, scope, "subject", []string{"u"}, 10)
	if err != nil || !sameStrings(impact.SourceEventIDs, []string{"u-src-0", "u-src-1", "u-src-2"}) || len(impact.Records) != 3 {
		t.Fatalf("subject impact = %+v %v", impact, err)
	}
	for _, ref := range impact.Records {
		if !strings.HasPrefix(ref.ID, "u-") {
			t.Fatal("subject impact includes another user", ref)
		}
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scope, "subject", []string{"u"}, 2); !errors.Is(err, ErrUnavailable) {
		t.Fatal("subject impact over limit was truncated", err)
	}
}

// A consultation is one evidence package: deleting its source pulls in the
// sources it shares a trace with and every run that read it.
func TestPostgresPrivacyDiscoveryFollowsConsultations(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	for _, r := range []Record{
		{ID: "src-1", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "asked", TraceID: "trace-x", CreatedAt: now, UpdatedAt: now},
		{ID: "src-2", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "answered", CreatedAt: now, UpdatedAt: now},
		{ID: "src-3", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "unrelated", CreatedAt: now, UpdatedAt: now},
	} {
		if err := p.Insert(testCtx, "sources", r); err != nil {
			t.Fatal(err)
		}
	}
	consultations := []Record{
		{ID: "c-1", Scope: scope, Kind: "consultation", Status: statusActive, Revision: 1, SourceEventID: "src-1", CreatedAt: now, UpdatedAt: now,
			Consultation: &Consultation{Parent: ConsultationAgent{AgentRunID: "run-parent", ThreadID: "thread-parent"}, Child: &ConsultationAgent{AgentRunID: "run-child", ThreadID: "thread-child", TraceID: "trace-x"}}},
		{ID: "c-2", Scope: scope, Kind: "consultation", Status: statusActive, Revision: 1, SourceEventID: "src-2", TraceID: "trace-x", CreatedAt: now, UpdatedAt: now,
			Consultation: &Consultation{Parent: ConsultationAgent{AgentRunID: "run-two"}}},
	}
	for _, r := range consultations {
		if err := p.Insert(testCtx, "consultations", r); err != nil {
			t.Fatal(err)
		}
	}
	impact, err := p.DiscoverPrivacyImpact(testCtx, scope, "source_event", []string{"src-1"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(impact.SourceEventIDs, []string{"src-1", "src-2"}) || !sameStrings(impact.ExpectedRunIDs, []string{"run-child", "run-parent", "run-two"}) || !sameStrings(impact.ThreadIDs, []string{"thread-child", "thread-parent"}) {
		t.Fatalf("consultation impact = %+v", impact)
	}
	got := map[string]bool{}
	for _, ref := range impact.Records {
		got[ref.Collection+"/"+ref.ID] = true
	}
	if !got["consultations/c-1"] || !got["consultations/c-2"] || !got["sources/src-2"] || got["sources/src-3"] {
		t.Fatalf("consultation records = %v", got)
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scope, "source_event", []string{"src-1"}, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("consultation impact over limit was truncated", err)
	}
}

// Verification is the last gate before "completed": every way the data could
// still exist must keep the deletion open.
func TestPostgresPrivacyVerifyRefusesIncompletePurge(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	if err := p.Insert(testCtx, "sources", Record{ID: "live", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "still here", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"live"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("verify passed with the source still present", err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("verify passed without the dependent erasure job", err)
	}
	job := Record{ID: sourceMemoryErasureID(scope, "gone"), Scope: scope, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusFailed, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: "gone", Revision: 1}, SourceMemoryErasure: &SourceMemoryErasure{}}
	if err := p.Insert(testCtx, colJobs, job); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("verify passed with a failed erasure job", err)
	}
	job.Status, job.Revision = JobStatusPending, 2
	if err := p.Replace(testCtx, colJobs, job, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("verify passed with a pending erasure job", err)
	}
	job.Status, job.Revision = JobStatusDone, 3
	job.SourceMemoryErasure = &SourceMemoryErasure{PendingArtifacts: []artifactCleanup{{ID: "artifact-1", Digest: strings.Repeat("a", 64)}}}
	if err := p.Replace(testCtx, colJobs, job, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("verify passed with artifact bytes still pending", err)
	}
	job.SourceMemoryErasure, job.Revision = &SourceMemoryErasure{}, 4
	if err := p.Replace(testCtx, colJobs, job, 3); err != nil {
		t.Fatal(err)
	}
	// A record that still cites the deleted source keeps verification open.
	cite := Record{ID: "cite", Scope: scope, Kind: "semantic", Status: statusActive, Revision: 1, Text: "derived", Sources: []SourceRef{{SourceID: "gone", Locator: "original"}}, CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(testCtx, "memories", cite); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("verify passed while a memory still cites the source", err)
	}
	if err := p.Delete(testCtx, "memories", scope, "cite", 1); err != nil {
		t.Fatal(err)
	}
	if report, err := verifyInTx(p, scope, PrivacyImpact{SourceEventIDs: []string{"gone"}}); err != nil || !report.DerivedPurged || !report.LiveAccessRemoved {
		t.Fatalf("clean verify = %+v %v", report, err)
	}
}

// Advancing a verifying deletion: an unfinished purge retries, a broken one
// becomes partial (then failed), and only a runtime-purged one may finish.
func TestPostgresPrivacyAdvanceVerifyingOutcomes(t *testing.T) {
	p := isolatedPostgres(t)
	auth := &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}
	svc := &Service{Repo: p, Auth: auth}
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	if err := p.Insert(testCtx, "sources", Record{ID: "live", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "still here", PrivacyQuarantined: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	failed := Record{ID: sourceMemoryErasureID(scope, "gone"), Scope: scope, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusFailed, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: "sources", ID: "gone", Revision: 1}, SourceMemoryErasure: &SourceMemoryErasure{}}
	if err := p.Insert(testCtx, colJobs, failed); err != nil {
		t.Fatal(err)
	}
	n := 0
	job := func(impact PrivacyImpact, attempts int, runtimePurged bool, retention string) Record {
		t.Helper()
		n++
		pd := &PrivacyDeletion{State: privacyVerifying, TargetType: "source_event", RequestHash: ContentHash(fmt.Sprint(n)), LegalHoldStatus: privacyHoldNone, RetentionPolicyRef: retention, Impact: impact, Report: PrivacyPurgeReport{RuntimePurged: runtimePurged}}
		r := Record{ID: fmt.Sprintf("privacy-deletion-%d", n), Scope: scope, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, Attempts: attempts, CreatedAt: now, UpdatedAt: now, PrivacyDeletion: pd}
		if err := p.Insert(testCtx, colJobs, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	advance := func(r Record) PrivacyDeletionStatus {
		t.Helper()
		st, err := svc.AdvancePrivacyDeletion(testCtx, "worker", r.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: r.Revision})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := advance(job(PrivacyImpact{SourceEventIDs: []string{"live"}}, 1, true, retentionSynthetic)); st.PrivacyDeletion.State != privacyVerifying || st.JobStatus != JobStatusRetry {
		t.Fatalf("unfinished purge = %+v", st)
	}
	if st := advance(job(PrivacyImpact{SourceEventIDs: []string{"gone"}}, 1, true, retentionSynthetic)); st.PrivacyDeletion.State != privacyPartial || st.JobStatus != JobStatusRetry || st.PrivacyDeletion.ErrorSummary != "canonical_verification_failed" {
		t.Fatalf("broken purge = %+v", st)
	}
	if st := advance(job(PrivacyImpact{SourceEventIDs: []string{"gone"}}, maxJobAttempts, true, retentionSynthetic)); st.PrivacyDeletion.State != privacyFailed || st.JobStatus != JobStatusFailed {
		t.Fatalf("broken purge at last attempt = %+v", st)
	}
	if st := advance(job(PrivacyImpact{}, 1, false, retentionSynthetic)); st.PrivacyDeletion.State != privacyPartial || st.PrivacyDeletion.CompletedAt != nil {
		t.Fatalf("completed without runtime purge = %+v", st)
	}
	if st := advance(job(PrivacyImpact{}, 1, true, retentionExternal)); st.PrivacyDeletion.State != privacyWaiting || !st.PrivacyDeletion.Report.BackupExpiryPending {
		t.Fatalf("external retention = %+v", st)
	}
	if st := advance(job(PrivacyImpact{}, 1, true, retentionSynthetic)); st.PrivacyDeletion.State != privacyCompleted || st.JobStatus != JobStatusDone || st.PrivacyDeletion.CompletedAt == nil {
		t.Fatalf("synthetic retention = %+v", st)
	}
	if _, err := p.Get(testCtx, "sources", scope, "live"); err != nil {
		t.Fatal("verification removed data", err)
	}
}

// A waiting deletion completes only once every retained backup is newer than
// its purge; missing or older backup evidence leaves it waiting.
func TestPostgresPrivacyBackupExpiryCompletesWaiting(t *testing.T) {
	p := isolatedPostgres(t)
	auth := &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}
	svc := &Service{Repo: p, Auth: auth}
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	insert := func(id, state, status string, at time.Time) {
		t.Helper()
		pd := &PrivacyDeletion{State: state, TargetType: "source_event", RequestHash: ContentHash(id), LegalHoldStatus: privacyHoldNone, RetentionPolicyRef: retentionExternal, Report: PrivacyPurgeReport{RuntimePurged: true}}
		if err := p.Insert(testCtx, colJobs, Record{ID: id, Scope: scope, Kind: privacyDeletionKind, Status: status, Revision: 1, Attempts: 1, CreatedAt: at, UpdatedAt: at, PrivacyDeletion: pd}); err != nil {
			t.Fatal(err)
		}
	}
	insert("privacy-deletion-new", privacyVerifying, JobStatusPending, now)
	// Parked before JobStatusWaiting existed, like the production rows.
	insert("privacy-deletion-legacy", privacyWaiting, JobStatusPending, now.Add(-time.Hour))
	st, err := svc.AdvancePrivacyDeletion(testCtx, "worker", "privacy-deletion-new", PrivacyRevisionInput{Scope: scope, ExpectedRevision: 1})
	if err != nil || st.PrivacyDeletion.State != privacyWaiting || st.JobStatus != JobStatusWaiting || !st.PrivacyDeletion.Report.BackupExpiryPending {
		t.Fatalf("parked = %+v %v", st, err)
	}
	parked, err := p.Get(testCtx, colJobs, scope, "privacy-deletion-new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdvancePrivacyDeletion(testCtx, "worker", parked.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: parked.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("advanced without backup evidence: %v", err)
	}
	if _, err := p.NextPrivacyAdvance(testCtx, time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("waiting job offered to the worker: %v", err)
	}
	if n, err := svc.ExpirePrivacyBackups(testCtx, "worker", now.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("backup older than the new purge: n=%d %v", n, err)
	}
	if again, _ := p.Get(testCtx, colJobs, scope, parked.ID); again.PrivacyDeletion.State != privacyWaiting || again.Revision != parked.Revision {
		t.Fatalf("still-covered deletion moved: %+v", again.PrivacyDeletion)
	}
	oldest := parked.UpdatedAt.Add(time.Hour)
	if n, err := svc.ExpirePrivacyBackups(testCtx, "worker", oldest); err != nil || n != 1 {
		t.Fatalf("expired backups: n=%d %v", n, err)
	}
	for range 2 {
		next, err := p.NextPrivacyAdvance(testCtx, time.Now().UTC())
		if err != nil || next.PrivacyDeletion.State != privacyVerifying {
			t.Fatalf("next = %+v %v", next, err)
		}
		if st, err = svc.AdvancePrivacyDeletion(testCtx, "worker", next.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: next.Revision}); err != nil {
			t.Fatal(err)
		}
		r := st.PrivacyDeletion.Report
		if st.PrivacyDeletion.State != privacyCompleted || st.JobStatus != JobStatusDone || r.BackupExpiryPending || r.BackupOldestRetainedAt == nil || st.PrivacyDeletion.CompletedAt == nil {
			t.Fatalf("completed = %+v", st)
		}
	}
	if _, err := p.NextPrivacyAdvance(testCtx, time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("work left: %v", err)
	}
}

// A verification that finds an incomplete purge drops old backup evidence, so the re-purge waits again.
func TestPostgresPrivacyPartialDropsBackupEvidence(t *testing.T) {
	p := isolatedPostgres(t)
	auth := &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}
	svc := &Service{Repo: p, Auth: auth}
	now := time.Now().UTC().Truncate(time.Millisecond)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	pd := &PrivacyDeletion{State: privacyVerifying, TargetType: "source_event", RequestHash: ContentHash("x"), LegalHoldStatus: privacyHoldNone,
		RetentionPolicyRef: retentionExternal, Report: PrivacyPurgeReport{RuntimePurged: false, BackupOldestRetainedAt: &now}}
	if err := p.Insert(testCtx, colJobs, Record{ID: "privacy-deletion-partial", Scope: scope, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, Attempts: 1, CreatedAt: now, UpdatedAt: now, PrivacyDeletion: pd}); err != nil {
		t.Fatal(err)
	}
	st, err := svc.AdvancePrivacyDeletion(testCtx, "worker", "privacy-deletion-partial", PrivacyRevisionInput{Scope: scope, ExpectedRevision: 1})
	if r := st.PrivacyDeletion.Report; err != nil || st.PrivacyDeletion.State != privacyPartial || r.BackupOldestRetainedAt != nil || !r.BackupExpiryPending {
		t.Fatalf("partial = %+v %v", st, err)
	}
}
