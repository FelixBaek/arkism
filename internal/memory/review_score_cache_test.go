package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type reviewTransferAuth struct{ Authorizer }

type reviewDestinationAuth struct{ Authorizer }

func (a reviewDestinationAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, err := a.Authorizer.Authorize(ctx, token, in)
	if in.Context.Destination == "external:embedding" {
		d.ExternalTransfer = false
	}
	return d, err
}

func TestReviewAdviceRequiresExternalDestinationPermission(t *testing.T) {
	f := reviewAdviceFixture(t)
	f.svc.Auth = reviewDestinationAuth{f.auth}
	if page := reviewAdvicePage(t, f); len(page.Items) != 1 || page.Items[0].RejectScore != nil || len(f.emb.calls) != 0 {
		t.Fatal("internal read permission caused an external transfer")
	}
}

func TestTrainReviewRequiresExternalPermission(t *testing.T) {
	f := newFixture(t)
	row := Record{ID: "training-rejected", Scope: scopeA, Kind: "lesson", Status: "rejected", Revision: 1, Text: "Synthetic human decision", CreatedAt: time.Now().UTC()}
	if err := f.repo.Insert(testCtx, "memory_candidates", row); err != nil {
		t.Fatal(err)
	}
	f.svc.Auth = reviewDestinationAuth{f.auth}
	if _, _, err := f.svc.TrainReviewClassifier(testCtx, f.human, scopeA, nil); !errors.Is(err, ErrDenied) || len(f.emb.calls) != 0 {
		t.Fatal("training bypassed transfer permission", err)
	}
}

func TestTrainReviewUsesAuthorizedDecisionsAndKeepsTheirState(t *testing.T) {
	f := newFixture(t)
	for _, status := range []string{"approved", "rejected"} {
		row := Record{ID: "training-" + status, Scope: scopeA, Kind: "lesson", Status: status, Revision: 1, Text: "Synthetic " + status, CreatedAt: time.Now().UTC()}
		if err := f.repo.Insert(testCtx, "memory_candidates", row); err != nil {
			t.Fatal(err)
		}
	}
	model, report, err := f.svc.TrainReviewClassifier(testCtx, f.human, scopeA, nil)
	if err != nil || model.Scope != scopeA || report.Examples != 2 || report.Rejected != 1 || len(f.emb.calls) != 2 {
		t.Fatal("training did not use scoped decisions", model, report, err)
	}
	for _, status := range []string{"approved", "rejected"} {
		row, err := f.repo.Get(testCtx, "memory_candidates", scopeA, "training-"+status)
		if err != nil || row.Status != status || row.Revision != 1 {
			t.Fatal("training modified a human decision", row, err)
		}
	}
}

func (a reviewTransferAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, err := a.Authorizer.Authorize(ctx, token, in)
	if in.ResourceID != "" {
		d.ExternalTransfer = false
	}
	return d, err
}

func TestReviewAdviceRespectsRecordTransferDecision(t *testing.T) {
	f := reviewAdviceFixture(t)
	reviewAdvicePage(t, f) // Populate while the record is transferable.
	f.svc.Auth = reviewTransferAuth{f.auth}
	if page := reviewAdvicePage(t, f); len(page.Items) != 1 || page.Items[0].RejectScore != nil || len(f.emb.calls) != 1 {
		t.Fatal("collection grant bypassed record transfer restriction", page)
	}
}

func TestReviewAdviceProviderFailureCanRetry(t *testing.T) {
	f := reviewAdviceFixture(t)
	f.emb.err = errors.New("synthetic provider failure")
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore != nil {
		t.Fatal("failed embedding produced advice")
	}
	f.emb.err = nil
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore == nil || len(f.emb.calls) != 2 {
		t.Fatal("failed flight prevented later retry")
	}
}

func reviewAdviceFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.ReviewClassifier = &ReviewClassifier{Scope: scopeA, Space: f.emb.Space(), Weights: make([]float64, VectorDimension), Kinds: map[string]float64{"lesson": -2}}
	row := Record{ID: "cached-candidate", Scope: scopeA, Kind: "lesson", Status: "candidate", Revision: 1, Text: "Synthetic candidate", CreatedAt: time.Now().UTC()}
	if err := f.repo.Insert(testCtx, "memory_candidates", row); err != nil {
		t.Fatal(err)
	}
	return f
}
func reviewAdvicePage(t *testing.T, f *fixture) ReviewPage {
	t.Helper()
	page, err := f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA})
	if err != nil {
		t.Fatal(err)
	}
	return page
}
func TestReviewAdviceReusesScoresAndRechecksTransfer(t *testing.T) {
	f := reviewAdviceFixture(t)
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore == nil {
		t.Fatal("missing score")
	}
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore == nil || len(f.emb.calls) != 1 {
		t.Fatal("refresh re-embedded unchanged candidate", len(f.emb.calls))
	}
	actor := f.auth.actors[f.human]
	actor.external = false
	f.auth.actors[f.human] = actor
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore != nil || len(f.emb.calls) != 1 {
		t.Fatal("cached advice bypassed current transfer decision")
	}
}
func TestReviewAdviceContentAndModelInvalidateCache(t *testing.T) {
	f := reviewAdviceFixture(t)
	reviewAdvicePage(t, f)
	row, err := f.repo.Get(testCtx, "memory_candidates", scopeA, "cached-candidate")
	if err != nil {
		t.Fatal(err)
	}
	row.Text = "Changed synthetic candidate" // A malformed producer retaining revision/hash must not get stale advice.
	if err = f.repo.Replace(testCtx, "memory_candidates", row, row.Revision); err != nil {
		t.Fatal(err)
	}
	reviewAdvicePage(t, f)
	if len(f.emb.calls) != 2 {
		t.Fatal("changed text reused score")
	}
	f.svc.ReviewClassifier.Bias = 3
	reviewAdvicePage(t, f)
	if len(f.emb.calls) != 3 {
		t.Fatal("changed model reused score")
	}
	f.svc.ReviewClassifier.Scope = scopeB
	if page := reviewAdvicePage(t, f); page.Items[0].RejectScore != nil || len(f.emb.calls) != 3 {
		t.Fatal("model crossed scope")
	}
}
func TestReviewAdviceCoalescesConcurrentRefresh(t *testing.T) {
	f := reviewAdviceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.emb.onEmbed = func() { once.Do(func() { close(entered) }); <-release }
	first := make(chan error, 1)
	go func() { _, err := f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA}); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("embedding did not start")
	}
	second := make(chan error, 1)
	go func() { _, err := f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA}); second <- err }()
	close(release)
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("coalesced request stuck")
		}
	}
	f.emb.mu.Lock()
	calls := len(f.emb.calls)
	f.emb.mu.Unlock()
	if calls != 1 {
		t.Fatal("concurrent refresh repeated paid embedding", calls)
	}
}
func TestReviewAdviceCacheExpiresBoundsAndCancels(t *testing.T) {
	cache := &reviewScoreCache{}
	now := time.Now()
	key := reviewScoreKey{Scope: scopeA, ID: "a"}
	plan := cache.plan([]reviewScoreKey{key}, now)
	if plan == nil || len(plan.owned) != 1 {
		t.Fatal("missing leader")
	}
	follower := cache.plan([]reviewScoreKey{key}, now)
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if _, ok := follower.results(ctx, nil); ok {
		t.Fatal("wait ignored cancellation")
	}
	cache.complete(plan.owned, map[reviewScoreKey]float64{key: 0.7})
	if scores, ok := follower.results(testCtx, nil); !ok || scores[0] != 0.7 {
		t.Fatal(scores, ok)
	}
	if after := cache.plan([]reviewScoreKey{key}, now.Add(reviewScoreCacheTTL+time.Second)); len(after.owned) != 1 {
		t.Fatal("expired advice reused")
	}
	cache.complete(cache.pending, nil)
	for i := 0; i < reviewScoreCacheLimit+5; i++ {
		k := key
		k.Revision = int64(i)
		p := cache.plan([]reviewScoreKey{k}, time.Now())
		cache.complete(p.owned, map[reviewScoreKey]float64{k: 0.5})
	}
	if len(cache.values) > reviewScoreCacheLimit || len(cache.pending) != 0 {
		t.Fatal("unbounded cache")
	}
}
func TestReviewCatalogScanCanOmitPaidAdvice(t *testing.T) {
	f := reviewAdviceFixture(t)
	page, err := f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA, omitAdvice: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].RejectScore != nil || len(f.emb.calls) != 0 {
		t.Fatal(page, err)
	}
}
