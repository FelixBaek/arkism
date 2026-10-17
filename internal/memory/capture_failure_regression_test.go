package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Each injected failure affects only the capture pointer or stale-evidence
// lookup, after source/candidate writes have otherwise succeeded.
type captureFailureRepo struct {
	Repository
	pointerError     error
	staleSourceError error
	candidateError   error
	commitWinner     bool
	winnerTarget     string
	failedPointer    *Record
}

func (r *captureFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	err := r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, &captureFailureTx{Tx: tx, repo: r})
	})
	if r.commitWinner && r.failedPointer != nil && errors.Is(err, ErrConflict) {
		r.commitWinner = false
		winner := *r.failedPointer
		winner.Target = &Ref{Collection: "memory_candidates", ID: r.winnerTarget, Revision: 1}
		if writeErr := r.Repository.Insert(ctx, "jobs", winner); writeErr != nil {
			return writeErr
		}
	}
	return err
}

type captureFailureTx struct {
	Tx
	repo          *captureFailureRepo
	candidateRead bool
}

func (x *captureFailureTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "jobs" && r.Kind == "capture_pointer" && x.repo.pointerError != nil {
		x.repo.failedPointer = &r
		return x.repo.pointerError
	}
	return x.Tx.Insert(ctx, col, r)
}
func (x *captureFailureTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	if col == "jobs" && r.Kind == "capture_pointer" && x.repo.pointerError != nil {
		x.repo.failedPointer = &r
		return x.repo.pointerError
	}
	return x.Tx.Replace(ctx, col, r, revision)
}
func (x *captureFailureTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == "memory_candidates" {
		if x.repo.candidateError != nil {
			return Record{}, x.repo.candidateError
		}
		x.candidateRead = true
	}
	if col == "sources" && x.candidateRead && x.repo.staleSourceError != nil {
		return Record{}, x.repo.staleSourceError
	}
	return x.Tx.Get(ctx, col, scope, id)
}
func captureEventReceipt(scope Scope, event string) string {
	return "capture-event-" + ContentHash(structuredText(struct {
		Scope Scope  `json:"scope"`
		Event string `json:"event"`
	}{scope, event}))
}
func TestCapturePointerWriteFailureRemainsRetryable(t *testing.T) {
	for _, failure := range []error{ErrUnavailable, ErrDenied, ErrConflict} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newFixture(t)
			injected := &captureFailureRepo{Repository: f.repo, pointerError: failure}
			f.svc.Repo = injected
			in := captureInput(scopeA, "pointer-failed", explicit("캡처 저장 실패는 재시도한다."))
			first := f.capture(f.human, in)[0]
			if first.Status != "rejected" || first.Reason != failure.Error() {
				t.Fatalf("pointer failure acknowledged as success: %+v", first)
			}
			if _, err := f.repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed event was marked complete", err)
			}
			injected.pointerError = nil
			again := f.capture(f.human, in)[0]
			if again.Status != "candidate" {
				t.Fatalf("same event retry did not recover: %+v", again)
			}
			if pointer, err := f.repo.Get(testCtx, "jobs", scopeA, again.DedupeKey); err != nil || pointer.Target == nil || pointer.Target.ID != again.CandidateID {
				t.Fatal("recovered claim lacks pointer", err)
			}
		})
	}
}
func TestCaptureStaleSourceFailureRemainsRetryable(t *testing.T) {
	for _, failure := range []error{ErrUnavailable, ErrDenied} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newFixture(t)
			claim := explicit("철회한 기억의 출처를 다시 확인한다.")
			first := f.capture(f.human, captureInput(scopeA, "stale-first", claim))[0]
			candidate := f.get(f.human, "memory_candidates", scopeA, first.CandidateID)
			candidate.Status = "rejected"
			if err := f.repo.Replace(testCtx, "memory_candidates", candidate, candidate.Revision); err != nil {
				t.Fatal(err)
			}
			injected := &captureFailureRepo{Repository: f.repo, staleSourceError: failure}
			f.svc.Repo = injected
			in := captureInput(scopeA, "stale-retry", claim)
			failed := f.capture(f.human, in)[0]
			if failed.Status != "rejected" || failed.Reason != failure.Error() {
				t.Fatalf("stale source failure acknowledged as success: %+v", failed)
			}
			if _, err := f.repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed event was marked complete", err)
			}
			injected.staleSourceError = nil
			again := f.capture(f.human, in)[0]
			if again.Status != "candidate" || again.CandidateID == first.CandidateID {
				t.Fatalf("stale source retry lost claim: %+v", again)
			}
		})
	}
}

func TestCaptureConflictRequiresLiveCommittedWinner(t *testing.T) {
	for _, status := range []string{"candidate", "rejected"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			claim := explicit("동시 캡처는 저장된 승자를 확인한다.")
			winner := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Kind: claim.Kind, Text: claim.Text, Sources: refs(f.source())}))
			winner.Status = status
			if err := f.repo.Replace(testCtx, "memory_candidates", winner, winner.Revision); err != nil {
				t.Fatal(err)
			}
			f.svc.Repo = &captureFailureRepo{Repository: f.repo, pointerError: ErrConflict, commitWinner: true, winnerTarget: winner.ID}
			in := captureInput(scopeA, "concurrent-winner", claim)
			got := f.capture(f.human, in)[0]
			if status == "candidate" {
				if got.Status != "merged" || got.Reason != "concurrent_duplicate" || got.CandidateID != winner.ID {
					t.Fatalf("did not return committed winner: %+v", got)
				}
			} else {
				if got.Status != "rejected" || got.Reason != ErrConflict.Error() {
					t.Fatalf("dead pointer treated as concurrent success: %+v", got)
				}
				if _, err := f.repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
					t.Fatal("dead winner completed receipt", err)
				}
			}
		})
	}
}

func TestCaptureStaleSourceMissingOrExpiredIsRejected(t *testing.T) {
	for _, mode := range []string{"missing", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			claim := explicit("없거나 만료된 출처는 병합 성공으로 표시하지 않는다.")
			first := f.capture(f.human, captureInput(scopeA, "source-before", claim))[0]
			candidate := f.get(f.human, "memory_candidates", scopeA, first.CandidateID)
			candidate.Status = "rejected"
			if err := f.repo.Replace(testCtx, "memory_candidates", candidate, candidate.Revision); err != nil {
				t.Fatal(err)
			}
			src := f.get(f.human, "sources", scopeA, candidate.Sources[0].SourceID)
			expected := ErrNotFound
			if mode == "missing" {
				if err := f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				past := time.Now().Add(-time.Hour)
				src.ExpiresAt = &past
				if err := f.repo.Replace(testCtx, "sources", src, src.Revision); err != nil {
					t.Fatal(err)
				}
				expected = ErrConflict
			}
			got := f.capture(f.human, captureInput(scopeA, "source-after", claim))[0]
			if got.Status != "rejected" || got.Reason != expected.Error() {
				t.Fatalf("invalid stale source accepted: %+v", got)
			}
		})
	}
}

func TestStaleCaptureSourcePropagatesCandidateReadFailure(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = &captureFailureRepo{Repository: f.repo, candidateError: ErrUnavailable}
	_, err := f.svc.staleCaptureSource(testCtx, scopeA, Record{Target: &Ref{Collection: "memory_candidates", ID: "candidate", Revision: 1}, Sources: refs(f.source())})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("candidate read outage was hidden: %v", err)
	}
}

func TestPostgresCapturePointerFailureRetry(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	injected := &captureFailureRepo{Repository: repo, pointerError: ErrUnavailable}
	f.svc.Repo = injected
	in := captureInput(scopeA, "pg-pointer-failed", explicit("실제 데이터베이스 실패 뒤에도 다시 캡처한다."))
	first := f.capture(f.human, in)[0]
	if first.Status != "rejected" || first.Reason != ErrUnavailable.Error() {
		t.Fatal(first)
	}
	if _, err := repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed event completed", err)
	}
	injected.pointerError = nil
	again := f.capture(f.human, in)[0]
	if again.Status != "candidate" {
		t.Fatal("retry failed", again)
	}
	pointer, err := repo.Get(testCtx, "jobs", scopeA, again.DedupeKey)
	if err != nil || pointer.Target == nil || pointer.Target.ID != again.CandidateID {
		t.Fatal("missing recovered pointer", err)
	}
}

func (x *captureFailureTx) ReserveCandidateQuota(ctx context.Context, scope Scope, at time.Time, limit int) error {
	return reserveCandidate(ctx, x.Tx, scope, at, limit)
}
