package memory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type metricsRepo struct {
	Repository
	err    error
	scopes []Scope
}

func (m *metricsRepo) OperationalMetrics(_ context.Context, s Scope) (StoreMetrics, error) {
	m.scopes = append(m.scopes, s)
	return StoreMetrics{Candidates: QueueMetric{Count: 2, OldestAgeSeconds: 60}, Cost: CostMetrics{Available: true, Requests: 3, TotalTokens: 42}}, m.err
}
func (m *metricsRepo) SearchReady(context.Context) error { return nil }
func TestMetricsAuthorizationAndFailure(t *testing.T) {
	f := newFixture(t)
	repo := &metricsRepo{Repository: f.repo}
	f.svc.Repo = repo
	f.auth.actors[f.human] = actor("operator", "human", false, "ops")
	f.auth.actors[f.agent] = actor("agent", "agent", false, "ops")
	h := Handler(f.svc)
	url := "/metrics?workspace_id=ws-a&project_id=proj-a"
	request := func(token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", url, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		h.ServeHTTP(w, r)
		return w
	}
	for _, token := range []string{"", f.agent} {
		want := 403
		if token == "" {
			want = 401
		}
		if w := request(token); w.Code != want {
			t.Fatalf("unauthorized status %d", w.Code)
		}
	}
	if len(repo.scopes) != 0 {
		t.Fatal("unauthorized DB inspection")
	}
	w := request(f.human)
	if w.Code != 200 {
		t.Fatalf("metrics=%d %s", w.Code, w.Body.String())
	}
	var body struct {
		Store            StoreMetrics             `json:"store"`
		Requests         map[string]RequestMetric `json:"requests"`
		Cost             CostMetrics              `json:"cost"`
		DeletionWorkflow DeletionWorkflowMetrics  `json:"deletion_workflow"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Store.Candidates.Count != 2 || len(body.Requests) == 0 || !body.Cost.Available || body.Cost.Requests != 3 || body.Cost.TotalTokens != 42 || !body.DeletionWorkflow.Available || body.DeletionWorkflow.FullErasureVerified || body.DeletionWorkflow.Coverage != "tracked_jobs_only" {
		t.Fatal("missing metrics")
	}
	if len(repo.scopes) != 1 || repo.scopes[0] != scopeA {
		t.Fatal("scope widened")
	}
	for _, private := range []string{"ws-a", "proj-a", "tok-human", "operator"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("identifier in metrics")
		}
	}
	repo.err = ErrUnavailable
	if w = request(f.human); w.Code != 503 {
		t.Fatal("failed collection looks like zero")
	}
}
func TestRequestMetricsConcurrencyAndBuckets(t *testing.T) {
	m := &requestMetrics{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.observe("GET /healthz", 200, 100*time.Millisecond); _ = m.snapshot() }()
	}
	wg.Wait()
	r := m.snapshot()["GET /healthz"]
	if r.Count != 100 || r.StatusCodes[200] != 100 || r.LatencyLeCounts[0] != 0 || r.LatencyLeCounts[1] != 100 {
		t.Fatalf("bad count: %+v", r)
	}
	r.StatusCodes[200] = 0
	if m.snapshot()["GET /healthz"].StatusCodes[200] != 100 {
		t.Fatal("snapshot aliases state")
	}
}

type blockedReady struct {
	Repository
	release  chan struct{}
	finished chan struct{}
}

func (r *blockedReady) SearchReady(context.Context) error { <-r.release; close(r.finished); return nil }
func TestMetricsObserveTimeoutBeforeInnerHandlerReturns(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.human] = actor("ops", "human", false, "ops")
	repo := &blockedReady{Repository: f.repo, release: make(chan struct{}), finished: make(chan struct{})}
	f.svc.Repo = repo
	h := handler(f.svc, 10*time.Millisecond)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || len(w.Header().Get("X-Request-ID")) != 32 {
		t.Fatalf("timeout response: %d", w.Code)
	}
	// The handler is still blocked when the outer timeout has been observed.
	select {
	case <-repo.finished:
		t.Fatal("test handler returned before timeout observation")
	default:
	}
	close(repo.release)
	<-repo.finished
	// Keep the same handler's metrics, replacing only the now-unneeded blocking repo.
	// Wait for the timed goroutine to stop using the service before changing it.
	f.svc.Repo = &metricsRepo{Repository: f.repo}
	req := httptest.NewRequest("GET", "/metrics?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var response struct {
		Requests map[string]RequestMetric `json:"requests"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("metrics status %d", w.Code)
	}
	ready := response.Requests["GET /readyz"]
	if ready.Count != 1 || ready.StatusCodes[503] != 1 || ready.StatusCodes[200] != 0 {
		t.Fatalf("incorrect timeout metric: %+v", ready)
	}
}
