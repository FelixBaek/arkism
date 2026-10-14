package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

var privacyGuardScope = Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}

func TestPrivacyDeletionRequestRejectsMalformedInput(t *testing.T) {
	f := newFixture(t)
	base := PrivacyDeletionRequest{Scope: privacyGuardScope, IdempotencyKey: "key", TargetType: "source_event", TargetIDs: []string{"s1"}, Reason: "owner_request"}
	many := make([]string, 65)
	for i := range many {
		many[i] = "id-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for name, change := range map[string]func(*PrivacyDeletionRequest){
		"bad scope":         func(in *PrivacyDeletionRequest) { in.Scope = Scope{} },
		"bad target type":   func(in *PrivacyDeletionRequest) { in.TargetType = "memory" },
		"no targets":        func(in *PrivacyDeletionRequest) { in.TargetIDs = nil },
		"too many targets":  func(in *PrivacyDeletionRequest) { in.TargetIDs = many },
		"empty key":         func(in *PrivacyDeletionRequest) { in.IdempotencyKey = "" },
		"padded reason":     func(in *PrivacyDeletionRequest) { in.Reason = " owner_request" },
		"control in reason": func(in *PrivacyDeletionRequest) { in.Reason = "owner\nrequest" },
		"unknown retention": func(in *PrivacyDeletionRequest) { in.RetentionPolicyRef = "forever" },
		"preset hold":       func(in *PrivacyDeletionRequest) { in.LegalHoldStatus = privacyHoldHeld },
		"duplicate target":  func(in *PrivacyDeletionRequest) { in.TargetIDs = []string{"s1", "s1"} },
		"secret target": func(in *PrivacyDeletionRequest) {
			in.TargetIDs = []string{"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"}
		},
		"other subject": func(in *PrivacyDeletionRequest) { in.TargetType, in.TargetIDs = "subject", []string{"someone-else"} },
		"subject no user": func(in *PrivacyDeletionRequest) {
			in.TargetType, in.TargetIDs, in.Scope.UserID = "subject", []string{"u"}, ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			in.TargetIDs = append([]string(nil), base.TargetIDs...)
			change(&in)
			if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if len(f.repo.rows(colJobs)) != 0 {
		t.Fatal("rejected request wrote a job")
	}
	noRepo := &Service{Auth: f.auth}
	if _, err := noRepo.RequestPrivacyDeletion(testCtx, f.human, base); !errors.Is(err, ErrUnavailable) {
		t.Fatal("request without a repository", err)
	}
}

func TestPrivacyDeletionRequestAuthorization(t *testing.T) {
	f := newFixture(t)
	f.auth.actors["service"] = actor("svc", "service", false, "delete", "retention")
	f.auth.actors["service-no-retention"] = actor("svc", "service", false, "delete")
	f.auth.actors["human-no-retention"] = actor("human-1", "human", false, "delete")
	req := func(mut func(*PrivacyDeletionRequest)) PrivacyDeletionRequest {
		in := PrivacyDeletionRequest{Scope: privacyGuardScope, IdempotencyKey: "key", TargetType: "source_event", TargetIDs: []string{"s1"}, Reason: "owner_request"}
		if mut != nil {
			mut(&in)
		}
		return in
	}
	for name, c := range map[string]struct {
		token string
		in    PrivacyDeletionRequest
		auth  Authorizer
		want  error
	}{
		"agent cannot delete sources":          {f.agent, req(nil), nil, ErrDenied},
		"service needs retention":              {"service-no-retention", req(nil), nil, ErrDenied},
		"service cannot delete work":           {"service", req(func(in *PrivacyDeletionRequest) { in.TargetType, in.TargetIDs = "work_checkpoint", []string{"wp.one"} }), nil, ErrDenied},
		"synthetic retention needs permission": {"human-no-retention", req(func(in *PrivacyDeletionRequest) { in.RetentionPolicyRef = retentionSynthetic }), nil, ErrDenied},
		"other subject needs admin": {f.human, req(func(in *PrivacyDeletionRequest) { in.TargetType, in.TargetIDs = "subject", []string{"u"} }),
			reviewDenyAuth{Authorizer: f.auth, denyID: "u"}, ErrDenied},
		"work target denied per id": {f.human, req(func(in *PrivacyDeletionRequest) { in.TargetType, in.TargetIDs = "work_checkpoint", []string{"wp.one"} }),
			reviewDenyAuth{Authorizer: f.auth, denyID: "wp.one"}, ErrDenied},
		// Authorized, but the fake repository has no privacy store: fail closed.
		"authorized service without store": {"service", req(nil), nil, ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f.svc.Auth = f.auth
			if c.auth != nil {
				f.svc.Auth = c.auth
			}
			if _, err := f.svc.RequestPrivacyDeletion(testCtx, c.token, c.in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if len(f.repo.rows(colJobs)) != 0 {
		t.Fatal("denied request wrote a job")
	}
}

func TestPrivacyEndpointsRejectWrongRoles(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.human] = actor("human-1", "human", false, "read", "purge", "hold", "release")
	f.auth.actors[f.agent] = actor("agent-1", "agent", false, "read", "purge", "hold")
	f.auth.actors[f.work] = actor("worker-1", "service", false, "read", "purge")
	now := time.Now().UTC()
	plain := Record{ID: "plain-job", Scope: privacyGuardScope, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := f.repo.Insert(testCtx, colJobs, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetPrivacyDeletion(testCtx, f.agent, plain.ID, privacyGuardScope); !errors.Is(err, ErrDenied) {
		t.Fatal("agent read a privacy deletion", err)
	}
	if _, err := f.svc.GetPrivacyDeletion(testCtx, f.human, plain.ID, privacyGuardScope); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ordinary job exposed as privacy deletion", err)
	}
	if _, err := f.svc.GetPrivacyDeletion(testCtx, f.human, "missing", privacyGuardScope); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing deletion", err)
	}
	if _, err := f.svc.ClaimPrivacyDeletion(testCtx, f.human, PrivacyScopeInput{Scope: privacyGuardScope}); !errors.Is(err, ErrDenied) {
		t.Fatal("human claimed runtime purge", err)
	}
	if _, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: privacyGuardScope}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("claim without privacy store", err)
	}
	receipt := PrivacyRuntimeReceiptInput{Scope: privacyGuardScope, ExpectedRevision: 1, LeaseToken: "lease"}
	for token, in := range map[string]PrivacyRuntimeReceiptInput{
		f.human:           receipt,
		f.work + "-rev":   func() PrivacyRuntimeReceiptInput { r := receipt; r.ExpectedRevision = 0; return r }(),
		f.work + "-lease": func() PrivacyRuntimeReceiptInput { r := receipt; r.LeaseToken = ""; return r }(),
	} {
		caller := token
		if token != f.human {
			caller = f.work
		}
		if _, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, caller, "plain-job", in); !errors.Is(err, ErrDenied) {
			t.Fatal("receipt accepted", token, err)
		}
	}
	if _, err := f.svc.AdvancePrivacyDeletion(testCtx, f.human, plain.ID, PrivacyRevisionInput{Scope: privacyGuardScope, ExpectedRevision: 1}); !errors.Is(err, ErrDenied) {
		t.Fatal("human advanced canonical purge", err)
	}
	if _, err := f.svc.AdvancePrivacyDeletion(testCtx, f.work, plain.ID, PrivacyRevisionInput{Scope: privacyGuardScope}); !errors.Is(err, ErrDenied) {
		t.Fatal("advance without revision", err)
	}
	hold := PrivacyHoldInput{Scope: privacyGuardScope, ExpectedRevision: 1, Reason: "legal_case", PolicyRef: "case_1"}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.agent, plain.ID, hold); !errors.Is(err, ErrDenied) {
		t.Fatal("agent placed a hold", err)
	}
	bad := hold
	bad.Reason = "legal case\t"
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, plain.ID, bad); !errors.Is(err, ErrDenied) {
		t.Fatal("hold with malformed reason", err)
	}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, plain.ID, hold); !errors.Is(err, ErrConflict) {
		t.Fatal("hold on an ordinary job", err)
	}
	if _, err := f.svc.CheckPrivacyFence(testCtx, f.human, PrivacyFenceInput{Scope: privacyGuardScope, SourceEventIDs: []string{"s1"}}); !errors.Is(err, ErrDenied) {
		t.Fatal("human read the runtime fence", err)
	}
	if _, err := f.svc.CheckPrivacyFence(testCtx, f.work, PrivacyFenceInput{Scope: privacyGuardScope, SourceEventIDs: []string{"s1"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("fence without privacy store must fail closed", err)
	}
	if got, err := f.repo.Get(testCtx, colJobs, privacyGuardScope, plain.ID); err != nil || got.Revision != 1 {
		t.Fatal("rejected calls changed the job", err)
	}
}

// runtimeJob stores one leased deletion in the runtime phase.
func runtimeJob(t *testing.T, repo *fakeRepo, state, hold string, attempts int, leaseUntil time.Time) Record {
	t.Helper()
	now := time.Now().UTC()
	pd := &PrivacyDeletion{State: state, RequestHash: ContentHash("request"), LegalHoldStatus: hold, Impact: PrivacyImpact{ExpectedRunIDs: []string{"run-1"}}}
	job := Record{ID: "privacy-1", Scope: privacyGuardScope, Kind: privacyDeletionKind, Status: JobStatusRunning, Revision: 2, Attempts: attempts, LeaseToken: "lease-1", LeaseUntil: leaseUntil, CreatedAt: now, UpdatedAt: now, PrivacyDeletion: pd}
	if err := repo.Insert(testCtx, colJobs, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestPrivacyRuntimeReceiptRejectsStaleOrForgedReceipts(t *testing.T) {
	good := PrivacyRuntimeReceipt{RequestHash: ContentHash("request"), Quarantined: true, Verified: true, ExpectedRunIDs: []string{"run-1"}, AbsentRunIDs: []string{"run-1"}}
	for name, c := range map[string]struct {
		lease   string
		expired bool
		receipt func(PrivacyRuntimeReceipt) PrivacyRuntimeReceipt
		want    error
	}{
		"wrong lease":        {lease: "lease-2", want: ErrConflict},
		"expired lease":      {lease: "lease-1", expired: true, want: ErrConflict},
		"other request hash": {lease: "lease-1", receipt: func(r PrivacyRuntimeReceipt) PrivacyRuntimeReceipt { r.RequestHash = ContentHash("other"); return r }, want: ErrInvalid},
		"dropped expected run": {lease: "lease-1", receipt: func(r PrivacyRuntimeReceipt) PrivacyRuntimeReceipt {
			r.ExpectedRunIDs = nil
			return r
		}, want: ErrInvalid},
		"not quarantined": {lease: "lease-1", receipt: func(r PrivacyRuntimeReceipt) PrivacyRuntimeReceipt { r.Quarantined = false; return r }, want: ErrInvalid},
		"held on unheld":  {lease: "lease-1", receipt: func(r PrivacyRuntimeReceipt) PrivacyRuntimeReceipt { r.Verified, r.Held = false, true; return r }, want: ErrInvalid},
		"bad run label": {lease: "lease-1", receipt: func(r PrivacyRuntimeReceipt) PrivacyRuntimeReceipt {
			r.AbsentRunIDs = []string{"run-1", "bad\nrun"}
			return r
		}, want: ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.auth.actors[f.work] = actor("worker-1", "service", false, "purge")
			until := time.Now().Add(time.Minute)
			if c.expired {
				until = time.Now().Add(-time.Second)
			}
			job := runtimeJob(t, f.repo, privacyPurgingRuntime, privacyHoldNone, 1, until)
			receipt := good
			if c.receipt != nil {
				receipt = c.receipt(good)
			}
			_, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, job.ID, PrivacyRuntimeReceiptInput{Scope: privacyGuardScope, ExpectedRevision: job.Revision, LeaseToken: c.lease, Receipt: receipt})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			after, _ := f.repo.Get(testCtx, colJobs, privacyGuardScope, job.ID)
			if after.Revision != job.Revision || after.PrivacyDeletion.State != privacyPurgingRuntime || after.PrivacyDeletion.Report.RuntimePurged || after.PrivacyDeletion.RuntimeReceipt != nil {
				t.Fatalf("rejected receipt changed the deletion: %+v", after.PrivacyDeletion)
			}
		})
	}
}

func TestPrivacyRuntimeReceiptLastAttemptFailsAndHeldStaysHeld(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.work] = actor("worker-1", "service", false, "purge")
	job := runtimeJob(t, f.repo, privacyPurgingRuntime, privacyHoldNone, maxJobAttempts, time.Now().Add(time.Minute))
	unverified := PrivacyRuntimeReceipt{RequestHash: ContentHash("request"), Quarantined: true, ExpectedRunIDs: []string{"run-1"}, AbsentRunIDs: []string{"run-1"}}
	status, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, job.ID, PrivacyRuntimeReceiptInput{Scope: privacyGuardScope, ExpectedRevision: job.Revision, LeaseToken: "lease-1", Receipt: unverified})
	if err != nil || status.PrivacyDeletion.State != privacyFailed || status.JobStatus != JobStatusFailed || status.PrivacyDeletion.ErrorSummary != "runtime_verification_failed" || status.PrivacyDeletion.Report.RuntimePurged {
		t.Fatalf("last failed attempt = %+v %v", status, err)
	}

	held := newFixture(t)
	held.auth.actors[held.work] = actor("worker-1", "service", false, "purge")
	job = runtimeJob(t, held.repo, privacyHeld, privacyHoldHeld, 1, time.Now().Add(time.Minute))
	verified := unverified
	verified.Verified = true
	if _, err := held.svc.RecordPrivacyRuntimeReceipt(testCtx, held.work, job.ID, PrivacyRuntimeReceiptInput{Scope: privacyGuardScope, ExpectedRevision: job.Revision, LeaseToken: "lease-1", Receipt: verified}); !errors.Is(err, ErrInvalid) {
		t.Fatal("verified purge receipt accepted for a held deletion", err)
	}
	keep := unverified
	keep.Held = true
	status, err = held.svc.RecordPrivacyRuntimeReceipt(testCtx, held.work, job.ID, PrivacyRuntimeReceiptInput{Scope: privacyGuardScope, ExpectedRevision: job.Revision, LeaseToken: "lease-1", Receipt: keep})
	if err != nil || status.PrivacyDeletion.State != privacyHeld || status.JobStatus != JobStatusDone || status.PrivacyDeletion.Report.RuntimePurged || status.PrivacyDeletion.LegalHoldStatus != privacyHoldHeld {
		t.Fatalf("held receipt = %+v %v", status, err)
	}
}

func TestPrivacyAdvanceRefusesUnsafePhases(t *testing.T) {
	setup := func(t *testing.T, state, hold string, receipt bool, sourceQuarantined bool) (*Service, *privacyAdvanceRepo, Record) {
		t.Helper()
		repo := &privacyAdvanceRepo{fakeRepo: newFakeRepo(), nextID: "privacy-1"}
		now := time.Now().UTC()
		source := Record{ID: "source-1", Scope: privacyGuardScope, Kind: "document", Status: statusActive, Revision: 1, Text: "private", PrivacyQuarantined: sourceQuarantined, CreatedAt: now, UpdatedAt: now}
		if err := repo.Insert(testCtx, "sources", source); err != nil {
			t.Fatal(err)
		}
		pd := &PrivacyDeletion{State: state, RequestHash: ContentHash("r"), LegalHoldStatus: hold, Impact: PrivacyImpact{SourceEventIDs: []string{source.ID}}}
		if receipt {
			pd.RuntimeReceipt = &PrivacyRuntimeReceipt{RequestHash: pd.RequestHash, Quarantined: true, Verified: true}
		}
		job := Record{ID: repo.nextID, Scope: privacyGuardScope, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, PrivacyDeletion: pd, CreatedAt: now, UpdatedAt: now}
		if err := repo.Insert(testCtx, colJobs, job); err != nil {
			t.Fatal(err)
		}
		svc := &Service{Repo: repo, Auth: &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}}
		return svc, repo, job
	}
	for name, c := range map[string]struct {
		state, hold          string
		receipt, quarantined bool
	}{
		"legal hold":              {privacyPurgingDerived, privacyHoldHeld, true, true},
		"no runtime receipt":      {privacyPurgingDerived, privacyHoldNone, false, true},
		"runtime not yet claimed": {privacyQuarantined, privacyHoldNone, true, true},
		"source not quarantined":  {privacyPurgingDerived, privacyHoldNone, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, job := setup(t, c.state, c.hold, c.receipt, c.quarantined)
			if _, err := svc.AdvancePrivacyDeletion(testCtx, "worker", job.ID, PrivacyRevisionInput{Scope: privacyGuardScope, ExpectedRevision: 1}); !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want conflict", err)
			}
			if src, err := repo.Get(testCtx, "sources", privacyGuardScope, "source-1"); err != nil || src.Text != "private" {
				t.Fatal("refused advance deleted the source", err)
			}
			if after, _ := repo.Get(testCtx, colJobs, privacyGuardScope, job.ID); after.Revision != 1 || after.PrivacyDeletion.State != c.state {
				t.Fatal("refused advance changed the deletion")
			}
		})
	}
	// The happy path removes the source and records a receipt for it.
	svc, repo, job := setup(t, privacyPurgingDerived, privacyHoldNone, true, true)
	status, err := svc.AdvancePrivacyDeletion(testCtx, "worker", job.ID, PrivacyRevisionInput{Scope: privacyGuardScope, ExpectedRevision: 1})
	if err != nil || status.PrivacyDeletion.State != privacyVerifying {
		t.Fatalf("advance = %+v %v", status, err)
	}
	if _, err := repo.Get(testCtx, "sources", privacyGuardScope, "source-1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("purged source still readable", err)
	}
	if r, err := repo.Get(testCtx, colJobs, privacyGuardScope, sourceDeletionID(privacyGuardScope, "source-1")); err != nil || r.ErasureParentID != job.ID {
		t.Fatal("source deletion receipt missing", err)
	}
	// Verification needs the privacy store inside the transaction; the fake has none.
	if _, err := svc.AdvancePrivacyDeletion(testCtx, "worker", job.ID, PrivacyRevisionInput{Scope: privacyGuardScope, ExpectedRevision: status.Revision}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("verification without a privacy store", err)
	}
}

func TestPrivacyHoldRefusesRunningAndUnheldRelease(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.human] = actor("human-1", "human", false, "hold", "release")
	job := runtimeJob(t, f.repo, privacyQuarantined, privacyHoldNone, 1, time.Now().Add(time.Minute))
	in := PrivacyHoldInput{Scope: privacyGuardScope, ExpectedRevision: job.Revision, Reason: "legal_case", PolicyRef: "case_1"}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, job.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatal("hold during a running lease", err)
	}
	job.Status, job.LeaseToken = JobStatusPending, ""
	job.Revision++
	if err := f.repo.Replace(testCtx, colJobs, job, job.Revision-1); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = job.Revision
	if _, err := f.svc.ReleasePrivacyDeletionHold(testCtx, f.human, job.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatal("released a deletion that was never held", err)
	}
	in.ExpectedRevision = job.Revision + 5
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, job.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatal("hold with stale revision", err)
	}
	if after, _ := f.repo.Get(testCtx, colJobs, privacyGuardScope, job.ID); after.Revision != job.Revision || after.PrivacyDeletion.LegalHoldStatus != privacyHoldNone {
		t.Fatal("refused hold changed the deletion")
	}
}

type noPrivacyTx struct{ Tx }

func TestPrivacyTransactionHelpersFailClosedWithoutStore(t *testing.T) {
	ctx := context.Background()
	if err := checkPrivacyOverlap(ctx, noPrivacyTx{}, privacyGuardScope, "id", "subject", PrivacyImpact{}, false); !errors.Is(err, ErrUnavailable) {
		t.Fatal("overlap check without store", err)
	}
	if err := fenceWorkSubject(ctx, noPrivacyTx{}, privacyGuardScope); !errors.Is(err, ErrUnavailable) {
		t.Fatal("subject fence without store", err)
	}
}
