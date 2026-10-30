package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type sys04BeforeTransactionRepository struct {
	Repository
	store            restoreReviewStore
	entered, release chan struct{}
}

func (p *sys04BeforeTransactionRepository) RequireRestoreReview(ctx context.Context, m string) error {
	return p.store.RequireRestoreReview(ctx, m)
}
func (p *sys04BeforeTransactionRepository) CheckRestorePublication(ctx context.Context) error {
	return p.store.CheckRestorePublication(ctx)
}
func (p *sys04BeforeTransactionRepository) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.Repository.Transaction(ctx, fn)
}
func TestDynamicsRestoreFenceEntryToActualTransactionPG(t *testing.T) {
	for _, mode := range []string{"review_read", "review_process", "generation_read", "generation_process", "review_activation", "generation_activation", "review_drain", "generation_drain"} {
		t.Run(mode, func(t *testing.T) { sys04CheckEntryToActualTransaction(t, mode) })
	}
}

func sys04CheckEntryToActualTransaction(t *testing.T, mode string) {
	t.Helper()
	p := nativePGSetup(t)
	p.activate(t, "fence")
	_, capture := p.accept(t)
	if !strings.HasSuffix(mode, "process") && !strings.HasSuffix(mode, "drain") {
		p.process(t, capture)
	}
	wrapper := &sys04BeforeTransactionRepository{Repository: p.workerRepo, store: p.workerRepo, entered: make(chan struct{}), release: make(chan struct{})}
	svc := &Service{Repo: wrapper, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics}
	result := make(chan error, 1)
	go func() { result <- sys04RunFencedNativeOperation(svc, p, mode, capture) }()
	select {
	case <-wrapper.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not finish entry/previews before actual native Tx")
	}
	// The authoritative native transaction has NOT begun. Commit the fence first.
	if strings.HasPrefix(mode, "generation") {
		sys04Pause(t, p.admin)
	} else {
		if err := p.admin.RequireRestoreReview(testCtx, ContentHash("new committed restore review")); err != nil {
			t.Fatal(err)
		}
	}
	close(wrapper.release)
	err := <-result
	if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrConflict) {
		t.Fatal("entry-only check admitted operation after committed fence", err)
	}
	t.Log("schedule: entry check and previews complete; fence committed; native transaction begins; must withhold")
}

func sys04RunFencedNativeOperation(svc *Service, p *nativePGFixture, mode string, capture Ref) error {
	if strings.HasSuffix(mode, "process") {
		r, e := svc.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, capture)
		if e == nil && (r.AppliedNow || r.LearningConsumed) {
			e = errors.New("STALE_PROCESS_PUBLISHED")
		}
		return e
	}
	if strings.HasSuffix(mode, "activation") {
		_, e := svc.ActivateNativeMemoryDynamics(testCtx, p.workerToken, NativeDynamicsActivationInput{p.in.Scope, p.in.Target, p.in.Dimension, nativeTestConfig("fence-new"), "new-activation"})
		return e
	}
	if strings.HasSuffix(mode, "drain") {
		_, e := svc.DrainNativeDynamicsCaptures(testCtx, p.workerToken, p.in.Scope, 10)
		return e
	}
	out, e := svc.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "fence", time.Now().UnixNano()})
	if e == nil && out.Value != nil {
		e = errors.New("STALE_READ_PUBLISHED")
	}
	return e
}
