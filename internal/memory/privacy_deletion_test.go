package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type privacyAdvanceRepo struct {
	*fakeRepo
	nextID string
}

// The small fake only advances a single privacy request. Overlapping requests
// and their concurrency are exercised against PostgreSQL, not approximated here.
func (x fakeTx) CheckPrivacyOverlap(_ context.Context, scope Scope, id, _ string, _ PrivacyImpact, _ bool) error {
	for _, row := range x.store[colJobs] {
		if row.Scope == scope && row.Kind == privacyDeletionKind && row.ID != id {
			return ErrUnavailable
		}
	}
	return nil
}

func (r *privacyAdvanceRepo) DiscoverPrivacyImpact(context.Context, Scope, string, []string, int) (PrivacyImpact, error) {
	return PrivacyImpact{}, ErrUnavailable
}
func (r *privacyAdvanceRepo) QuarantinePrivacyImpact(context.Context, Scope, PrivacyImpact) error {
	return ErrUnavailable
}
func (r *privacyAdvanceRepo) VerifyPrivacyPurge(context.Context, Scope, PrivacyImpact) (PrivacyPurgeReport, error) {
	return PrivacyPurgeReport{}, ErrUnavailable
}
func (r *privacyAdvanceRepo) ClaimPrivacyDeletion(context.Context, Scope, time.Time, time.Duration) (Record, error) {
	return Record{}, ErrNotFound
}
func (r *privacyAdvanceRepo) PrivacyFence(context.Context, PrivacyFenceInput) (PrivacyFenceResult, error) {
	return PrivacyFenceResult{}, ErrUnavailable
}
func (r *privacyAdvanceRepo) NextPrivacyAdvance(ctx context.Context, _ time.Time) (Record, error) {
	return r.Get(ctx, colJobs, scopeA, r.nextID)
}

func TestPrivacyStateAndReceiptFailClosed(t *testing.T) {
	states := []string{privacyRequested, privacyAuthorized, privacyDiscoveringImpact, privacyQuarantined, privacyPurgingRuntime, privacyPurgingDerived, privacyWaiting, privacyHeld, privacyVerifying, privacyCompleted, privacyPartial, privacyFailed, privacyCancelled}
	for _, state := range states {
		if state == "" {
			t.Fatal("empty privacy state")
		}
	}
	if privacyTransitionAllowed(privacyQuarantined, privacyCompleted) || privacyTransitionAllowed(privacyHeld, privacyCompleted) || privacyTransitionAllowed(privacyQuarantined, privacyCancelled) {
		t.Fatal("privacy lifecycle permits an unsafe terminal transition")
	}
	pd := &PrivacyDeletion{RequestHash: ContentHash("request"), LegalHoldStatus: privacyHoldNone, Impact: PrivacyImpact{ExpectedRunIDs: []string{"run-1"}}}
	base := PrivacyRuntimeReceipt{RequestHash: pd.RequestHash, Quarantined: true, Verified: true, ExpectedRunIDs: []string{"run-1"}, MatchedRunIDs: []string{"run-1"}, AffectedRunIDs: []string{"run-1"}, Counts: PrivacyRuntimeCounts{Runs: 1}}
	if !validRuntimeReceipt(pd, base) {
		t.Fatal("valid receipt rejected")
	}
	forged := base
	forged.MatchedRunIDs, forged.AffectedRunIDs, forged.Counts.Runs = nil, nil, 0
	if validRuntimeReceipt(pd, forged) {
		t.Fatal("missing expected runtime evidence accepted")
	}
	absent := forged
	absent.AbsentRunIDs = []string{"run-1"}
	if !validRuntimeReceipt(pd, absent) {
		t.Fatal("explicit absent runtime evidence rejected")
	}
	badCount := base
	badCount.Counts.Runs = 0
	if validRuntimeReceipt(pd, badCount) {
		t.Fatal("unbound runtime count accepted")
	}
	held := base
	pd.LegalHoldStatus = privacyHoldHeld
	if validRuntimeReceipt(pd, held) {
		t.Fatal("non-held receipt accepted for held deletion")
	}
	if live(Record{Status: statusActive, PrivacyQuarantined: true}) {
		t.Fatal("quarantined record is live")
	}
}

func TestPrivacyRuntimeFailureIsDurableAndRetryable(t *testing.T) {
	repo := newFakeRepo()
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	now := time.Now().UTC()
	pd := &PrivacyDeletion{State: privacyPurgingRuntime, RequestHash: ContentHash("request"), LegalHoldStatus: privacyHoldNone, Impact: PrivacyImpact{ExpectedRunIDs: []string{"run-1"}}}
	job := Record{ID: "privacy-1", Scope: scope, Kind: privacyDeletionKind, Status: JobStatusRunning, Revision: 2, Attempts: 1, LeaseToken: "lease-1", LeaseUntil: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now, PrivacyDeletion: pd}
	if err := repo.Insert(context.Background(), colJobs, job); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: repo, Auth: &fakeAuth{actors: map[string]fakeActor{
		"worker": actor("worker", "service", false, "purge"),
		"owner":  actor("owner", "human", false, "hold"),
	}}}
	in := PrivacyRuntimeReceiptInput{Scope: scope, ExpectedRevision: 2, LeaseToken: "lease-1", Receipt: PrivacyRuntimeReceipt{RequestHash: pd.RequestHash, ExpectedRunIDs: []string{"run-1"}, Errors: []string{"database URL secret must never persist"}}}
	status, err := svc.RecordPrivacyRuntimeReceipt(context.Background(), "worker", job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if status.JobStatus != JobStatusRetry || status.PrivacyDeletion.State != privacyPartial || status.PrivacyDeletion.ErrorSummary != "runtime_purge_failed" || len(status.PrivacyDeletion.RuntimeReceipt.Errors) != 1 || status.PrivacyDeletion.RuntimeReceipt.Errors[0] != "runtime_purge_failed" {
		t.Fatalf("failure status = %+v", status)
	}
	if strings.Contains(structuredText(status), "database URL") {
		t.Fatal("runtime error prose persisted")
	}
	replayed, err := svc.RecordPrivacyRuntimeReceipt(context.Background(), "worker", job.ID, in)
	if err != nil || replayed.Revision != status.Revision {
		t.Fatalf("idempotent failure receipt: %+v %v", replayed, err)
	}
	later, err := repo.Get(context.Background(), colJobs, scope, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	previous := later.Revision
	later.Revision++
	later.PrivacyDeletion.State = privacyVerifying
	if err = repo.Replace(context.Background(), colJobs, later, previous); err != nil {
		t.Fatal(err)
	}
	replayed, err = svc.RecordPrivacyRuntimeReceipt(context.Background(), "worker", job.ID, in)
	if err != nil || replayed.Revision != later.Revision {
		t.Fatalf("late duplicate failure receipt: %+v %v", replayed, err)
	}
	if _, err = svc.HoldPrivacyDeletion(context.Background(), "owner", job.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: later.Revision, Reason: "legal_case", PolicyRef: "case_1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late hold = %v, want conflict", err)
	}
}

func TestPrivacyServiceErasureRequiresParentIdentityFormat(t *testing.T) {
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	job := Record{ID: sourceMemoryErasureID(scope, "source-1"), Scope: scope, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusPending, Revision: 1, CreatedBy: "worker", CreatedByType: "service", Target: &Ref{Collection: "sources", ID: "source-1", Revision: 2}, SourceMemoryErasure: &SourceMemoryErasure{}}
	if validSourceErasureJob(job) {
		t.Fatal("unparented service erasure accepted")
	}
	job.ErasureParentID = "privacy-deletion-" + ContentHash("request")
	if !validSourceErasureJob(job) {
		t.Fatal("canonical privacy child erasure rejected")
	}
	job.ErasureParentID = "privacy-deletion-not-a-digest"
	if validSourceErasureJob(job) {
		t.Fatal("malformed privacy parent accepted")
	}
}

func TestWorkOnceAdvancesCanonicalPrivacyPhaseWhenQueueIsIdle(t *testing.T) {
	base := newFakeRepo()
	repo := &privacyAdvanceRepo{fakeRepo: base, nextID: "privacy-auto"}
	pd := &PrivacyDeletion{State: privacyPurgingDerived, RequestHash: ContentHash("auto"), LegalHoldStatus: privacyHoldNone, Impact: PrivacyImpact{}}
	pd.RuntimeReceipt = &PrivacyRuntimeReceipt{RequestHash: pd.RequestHash, Quarantined: true, Verified: true}
	now := time.Now().UTC()
	job := Record{ID: repo.nextID, Scope: scopeA, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, PrivacyDeletion: pd, CreatedAt: now, UpdatedAt: now}
	if err := repo.Insert(context.Background(), colJobs, job); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: repo, Auth: &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}}
	if err := svc.WorkOnce(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), colJobs, scopeA, job.ID)
	if err != nil || got.PrivacyDeletion.State != privacyVerifying || got.Revision != 2 {
		t.Fatalf("auto advance = %+v %v", got, err)
	}
}

func TestPostgresPrivacyRequestQuarantinesBeforePurge(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "privacy-test", ProjectID: t.Name(), UserID: "owner", AgentID: "agent"}
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := Record{ID: "source-1", Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "synthetic private source", CreatedAt: now, UpdatedAt: now}
	memory := Record{ID: "memory-1", Scope: scope, Kind: "semantic", Status: statusActive, Revision: 1, Text: "derived", Sources: []SourceRef{{SourceID: source.ID, Locator: "original"}}, ApprovedBy: "owner", CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(ctx, "sources", source); err != nil {
		t.Fatal(err)
	}
	if err := p.Insert(ctx, "memories", memory); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{actors: map[string]fakeActor{"owner-token": actor("owner", "human", false, "delete", "read")}}
	svc := &Service{Repo: p, Auth: auth}
	in := PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "request-1", TargetType: "source_event", TargetIDs: []string{source.ID}, Reason: "privacy_request", RetentionPolicyRef: retentionExternal}
	status, err := svc.RequestPrivacyDeletion(ctx, "owner-token", in)
	if err != nil || status.PrivacyDeletion.State != privacyQuarantined || !status.PrivacyDeletion.Report.LiveAccessRemoved || status.PrivacyDeletion.PurgeReportRef != "jobs/"+status.ID+"#privacy_deletion.report" {
		t.Fatalf("request: %+v %v", status, err)
	}
	if _, err = svc.Get(ctx, "owner-token", "sources", scope, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quarantined source Get = %v", err)
	}
	for _, target := range []struct{ collection, id string }{{"sources", source.ID}, {"memories", memory.ID}} {
		r, getErr := p.Get(ctx, target.collection, scope, target.id)
		if getErr != nil || !r.PrivacyQuarantined {
			t.Fatalf("%s/%s quarantine: %+v %v", target.collection, target.id, r, getErr)
		}
	}
	fence, err := p.PrivacyFence(ctx, PrivacyFenceInput{Scope: scope, SourceEventIDs: []string{source.ID}})
	if err != nil || fence.Allowed || len(fence.DeletionRequestIDs) != 1 {
		t.Fatalf("fence: %+v %v", fence, err)
	}
	repeated, err := svc.RequestPrivacyDeletion(ctx, "owner-token", in)
	if err != nil || repeated.ID != status.ID {
		t.Fatalf("idempotent request: %+v %v", repeated, err)
	}
	in.Reason = "changed_request"
	if _, err = svc.RequestPrivacyDeletion(ctx, "owner-token", in); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed idempotency payload = %v", err)
	}
}

// Cross-record privacy behavior uses PostgreSQL. The small fake has no held
// artifact scenarios and fails closed instead of approximating the SQL joins.
func (x fakeTx) CheckArtifactPrivacyHold(_ context.Context, digest string) error {
	if !sourceHashPattern.MatchString(digest) {
		return ErrInvalid
	}
	for _, r := range x.store[colJobs] {
		if r.PrivacyDeletion != nil && (r.PrivacyDeletion.State == privacyHeld || r.PrivacyDeletion.LegalHoldStatus == privacyHoldHeld) {
			return ErrUnavailable
		}
	}
	return nil
}
func (f *fakeRepo) CheckArtifactPrivacyHold(ctx context.Context, digest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().CheckArtifactPrivacyHold(ctx, digest)
}
