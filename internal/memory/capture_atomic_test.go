package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCapturePointerFailureRollsBackCandidate(t *testing.T) {
	f := newFixture(t)
	injected := &captureFailureRepo{Repository: f.repo, pointerError: ErrUnavailable}
	f.svc.Repo = injected
	in := captureInput(scopeA, "atomic-pointer", explicit("후보와 중복 포인터는 함께 저장한다."))
	if got := f.capture(f.human, in)[0]; got.Status != "rejected" {
		t.Fatal(got)
	}
	if got := len(f.repo.rows("memory_candidates")); got != 0 {
		t.Fatalf("pointer failure left %d orphan candidates", got)
	}
	injected.pointerError = nil
	if got := f.capture(f.human, in)[0]; got.Status != "candidate" {
		t.Fatal("retry did not recover", got)
	}
	if got := len(f.repo.rows("memory_candidates")); got != 1 {
		t.Fatalf("retry created %d candidates", got)
	}
}

func TestCaptureDifferentEventResumesPreparedSource(t *testing.T) {
	f := newFixture(t)
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			return Decision{}, ErrUnavailable
		}
		return original.Authorize(ctx, token, req)
	})
	claim := explicit("후보 저장 전 중단되어도 출처에서 이어간다.")
	if got := f.capture(f.human, captureInput(scopeA, "prepare-first", claim))[0]; got.Status != "rejected" {
		t.Fatal(got)
	}
	f.svc.Auth = original
	got := f.capture(f.human, captureInput(scopeA, "prepare-second", claim))[0]
	if got.Status != "candidate" {
		t.Fatalf("different event lost prepared source: %+v", got)
	}
	if len(f.repo.rows("sources")) != 1 || len(f.repo.rows("memory_candidates")) != 1 {
		t.Fatal("resume duplicated records")
	}
}

func TestCaptureConcurrentClaimsCreateOneCandidate(t *testing.T) {
	f := newFixture(t)
	claim := canonicalToolClaim(f.source())
	original := f.svc.Auth
	var reached atomic.Int32
	ready := make(chan struct{})
	ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
	defer cancel()
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			if reached.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return Decision{}, ctx.Err()
			}
		}
		return original.Authorize(ctx, token, req)
	})
	results := make(chan CaptureItem, 2)
	errs := make(chan error, 2)
	for _, event := range []string{"concurrent-first", "concurrent-second"} {
		go func(event string) {
			out, err := f.svc.Capture(ctx, f.agent, captureInput(scopeA, event, claim))
			if err == nil && len(out.Items) == 1 {
				results <- out.Items[0]
			} else {
				errs <- err
			}
		}(event)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-results:
		case err := <-errs:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("capture race timed out")
		}
	}
	if got := len(f.repo.rows("memory_candidates")); got != 1 {
		t.Fatalf("same claim created %d candidates", got)
	}
}

type failCapturePreparationRepo struct{ Repository }
type failCapturePreparationTx struct{ Tx }

func (r failCapturePreparationRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, failCapturePreparationTx{tx}) })
}
func (x failCapturePreparationTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "jobs" && r.Kind == "capture_preparation" {
		return ErrUnavailable
	}
	return x.Tx.Insert(ctx, col, r)
}
func TestCaptureSourceAndPreparationCommitTogether(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = failCapturePreparationRepo{f.repo}
	in := captureInput(scopeA, "source-atomic", explicit("원본과 재개 기록을 함께 저장한다."))
	got := f.capture(f.human, in)[0]
	if got.Status != "rejected" || got.Reason != ErrUnavailable.Error() {
		t.Fatalf("preparation failure hidden: %+v", got)
	}
	if len(f.repo.rows("sources")) != 0 || len(f.repo.rows("memory_candidates")) != 0 {
		t.Fatal("preparation failure left committed source/candidate")
	}
	if _, err := f.repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed source acknowledged", err)
	}
}

func TestCaptureQuotaResetResumesSameEvent(t *testing.T) {
	f := newFixture(t)
	f.svc.CandidateDailyLimit = 1
	if got := f.capture(f.agent, captureInput(scopeA, "quota-first", explicit("첫 번째 후보를 검토한다.")))[0]; got.Status != "candidate" {
		t.Fatal(got)
	}
	in := captureInput(scopeA, "quota-second", explicit("한도가 풀리면 같은 이벤트를 이어간다."))
	if got := f.capture(f.agent, in)[0]; got.Status != "rejected" || got.Reason != ErrCandidateQuota.Error() {
		t.Fatal(got)
	}
	if _, err := f.repo.Get(testCtx, "jobs", scopeA, captureEventReceipt(scopeA, in.Event.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatal("quota refusal completed event", err)
	}
	f.svc.CandidateDailyLimit = 2
	if got := f.capture(f.agent, in)[0]; got.Status != "candidate" {
		t.Fatal("same event lost after quota reset", got)
	}
	if len(f.repo.rows("sources")) != 2 || len(f.repo.rows("memory_candidates")) != 2 {
		t.Fatal("quota retry duplicated records")
	}
}

func TestCaptureCancellationAfterSourceResumes(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			cancel()
			return Decision{}, ctx.Err()
		}
		return original.Authorize(ctx, token, req)
	})
	in := captureInput(scopeA, "cancel-after-source", explicit("요청 취소 뒤 새 요청으로 안전하게 이어간다."))
	out, err := f.svc.Capture(ctx, f.agent, in)
	if err != nil || len(out.Items) != 1 || out.Items[0].Status != "rejected" {
		t.Fatal(out, err)
	}
	if len(f.repo.rows("sources")) != 1 || len(f.repo.rows("memory_candidates")) != 0 {
		t.Fatal("source boundary not reached")
	}
	f.svc.Auth = original
	if got := f.capture(f.agent, in)[0]; got.Status != "candidate" {
		t.Fatal(got)
	}
	if len(f.repo.rows("sources")) != 1 || len(f.repo.rows("memory_candidates")) != 1 {
		t.Fatal("cancelled capture duplicated records")
	}
}

type captureCommitUnknownRepo struct {
	Repository
	collection string
	once       bool
}
type captureObserveWriteTx struct {
	Tx
	collection string
	wrote      *bool
}

func (x captureObserveWriteTx) Insert(ctx context.Context, col string, r Record) error {
	err := x.Tx.Insert(ctx, col, r)
	if err == nil && col == x.collection {
		*x.wrote = true
	}
	return err
}
func (r *captureCommitUnknownRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	wrote := false
	err := r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, captureObserveWriteTx{tx, r.collection, &wrote})
	})
	if err == nil && wrote && r.once {
		r.once = false
		return ErrUnavailable
	}
	return err
}
func TestCaptureCommitUnknownResumesWithoutDuplicates(t *testing.T) {
	for _, col := range []string{"sources", "memory_candidates"} {
		t.Run(col, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = &captureCommitUnknownRepo{Repository: f.repo, collection: col, once: true}
			in := captureInput(scopeA, "unknown-commit", explicit("응답이 유실되어도 이미 저장된 결과를 사용한다."))
			first := f.capture(f.agent, in)[0]
			if first.Status != "rejected" || first.Reason != ErrUnavailable.Error() {
				t.Fatal(first)
			}
			next := f.capture(f.agent, in)[0]
			if next.Status != "candidate" && next.Status != "merged" {
				t.Fatal(next)
			}
			if len(f.repo.rows("sources")) != 1 || len(f.repo.rows("memory_candidates")) != 1 {
				t.Fatal("unknown commit duplicated records")
			}
		})
	}
}

type failCaptureCompleteRepo struct{ Repository }
type failCaptureCompleteTx struct{ Tx }

func (r failCaptureCompleteRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, failCaptureCompleteTx{tx}) })
}
func (x failCaptureCompleteTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	if col == "jobs" && r.Kind == "capture_preparation" && r.Status == "done" {
		return ErrUnavailable
	}
	return x.Tx.Replace(ctx, col, r, revision)
}
func TestCaptureCompletionFailureRollsBackPointerAndQuota(t *testing.T) {
	f := newFixture(t)
	f.svc.CandidateDailyLimit = 1
	f.svc.Repo = failCaptureCompleteRepo{f.repo}
	in := captureInput(scopeA, "complete-failed", explicit("완료 기록까지 같은 트랜잭션으로 저장한다."))
	first := f.capture(f.agent, in)[0]
	if first.Status != "rejected" || first.Reason != ErrUnavailable.Error() {
		t.Fatal(first)
	}
	if len(f.repo.rows("memory_candidates")) != 0 {
		t.Fatal("completion failure left candidate")
	}
	if _, err := f.repo.Get(testCtx, "jobs", scopeA, first.DedupeKey); !errors.Is(err, ErrNotFound) {
		t.Fatal("completion failure left pointer", err)
	}
	for _, row := range f.repo.rows("jobs") {
		if row.Kind == "candidate_quota" {
			t.Fatal("failed candidate consumed quota")
		}
	}
	f.svc.Repo = f.repo
	if got := f.capture(f.agent, in)[0]; got.Status != "candidate" {
		t.Fatal("quota not rolled back", got)
	}
}

func (x failCaptureCompleteTx) ReserveCandidateQuota(ctx context.Context, scope Scope, at time.Time, limit int) error {
	return reserveCandidate(ctx, x.Tx, scope, at, limit)
}
func (x captureObserveWriteTx) ReserveCandidateQuota(ctx context.Context, scope Scope, at time.Time, limit int) error {
	return reserveCandidate(ctx, x.Tx, scope, at, limit)
}

func TestCaptureRestatementPreservesOriginalSourceSpan(t *testing.T) {
	f := newFixture(t)
	claim := explicit("저장소마다 명시한 규칙을 검토한다.")
	claim.Kind, claim.Repo = "procedural", "owner/repository"
	first := f.capture(f.agent, captureInput(scopeA, "span-first", claim))[0]
	old := f.get(f.agent, "memory_candidates", scopeA, first.CandidateID)
	original := old.Sources[0]
	old.Status = "rejected"
	if err := f.repo.Replace(testCtx, "memory_candidates", old, old.Revision); err != nil {
		t.Fatal(err)
	}
	next := f.capture(f.agent, captureInput(scopeA, "span-second", claim))[0]
	if next.Status != "candidate" {
		t.Fatal(next)
	}
	candidate := f.get(f.agent, "memory_candidates", scopeA, next.CandidateID)
	if len(candidate.Sources) != 1 || structuredText(candidate.Sources[0]) != structuredText(original) {
		t.Fatal("restatement widened original span into generated metadata")
	}
}

func TestCaptureSameTextOtherKindMergesInsteadOfConflict(t *testing.T) {
	f := newFixture(t)
	first := explicit("같은 문장이 다른 종류로 다시 들어와도 기록은 끝난다.")
	if got := f.capture(f.human, captureInput(scopeA, "kind-first", first))[0]; got.Status != "candidate" {
		t.Fatal(got)
	}
	second := first
	second.Kind = "procedural"
	if got := f.capture(f.human, captureInput(scopeA, "kind-second", second))[0]; got.Status != "merged" || got.Reason != "duplicate_source" {
		t.Fatalf("same text from another event: %+v", got)
	}
	// The event finished with a receipt, so a replay stops at it.
	if got := f.capture(f.human, captureInput(scopeA, "kind-second", second))[0]; got.Reason != "duplicate_event" {
		t.Fatalf("replay did not see the receipt: %+v", got)
	}
	if len(f.repo.rows("sources")) != 1 {
		t.Fatal("duplicate source stored")
	}
}
