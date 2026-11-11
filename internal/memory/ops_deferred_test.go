package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func deferredRequest(f *fixture, wait time.Duration, run func(context.Context, string) (any, error)) *httptest.ResponseRecorder {
	old := deferredWait
	deferredWait = wait
	defer func() { deferredWait = old }()
	r := httptest.NewRequest("POST", "/v1/observations/consolidate", nil)
	r.Header.Set("Authorization", "Bearer "+f.human)
	r.Pattern = "POST /v1/observations/consolidate"
	w := httptest.NewRecorder()
	f.svc.respondLater(w, r, scopeA, run)
	return w
}
func TestRespondLaterAnswersInTheRequestWhenFast(t *testing.T) {
	f := newOpsFixture(t)
	w := deferredRequest(f, time.Minute, func(context.Context, string) (any, error) { return map[string]int{"n": 1}, nil })
	page, _ := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA})
	if w.Code != 200 || w.Body.String() != "{\"n\":1}\n" || len(page.Items) != 0 {
		t.Fatalf("fast call = %d %q, %d operations", w.Code, w.Body, len(page.Items))
	}
}
func TestRespondLaterTimeoutCancelsWithoutStoringResult(t *testing.T) {
	f := newOpsFixture(t)
	cancelled := make(chan struct{})
	w := deferredRequest(f, time.Millisecond, func(ctx context.Context, _ string) (any, error) {
		<-ctx.Done()
		close(cancelled)
		return map[string]string{"text": "synthetic-late-result-canary"}, nil
	})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("timeout did not cancel work")
	}
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"code\":\"request_timeout\"}\n" || w.Header().Get("Location") != "" {
		t.Fatalf("timeout = %d %q", w.Code, w.Body)
	}
	if rows, _ := f.svc.Repo.ListRecords(testCtx, scopeA, colJobs, 100); len(rows) != 0 {
		t.Fatal("timeout persisted an operation", rows)
	}
}
func TestRespondLaterKeepsTheFailureCode(t *testing.T) {
	f := newOpsFixture(t)
	w := deferredRequest(f, time.Second, func(context.Context, string) (any, error) { return nil, ErrConflict })
	want, status := errorResponse(ErrConflict)
	var body map[string]string
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != status || body["code"] != want {
		t.Fatalf("error = %d %q", w.Code, w.Body)
	}
}
func TestRespondLaterRequestCancellationPropagates(t *testing.T) {
	f := newOpsFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/skills/analyze", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.human)
	observed := make(chan struct{})
	w := httptest.NewRecorder()
	f.svc.respondLater(w, r, scopeA, func(ctx context.Context, _ string) (any, error) {
		cancel()
		<-ctx.Done()
		close(observed)
		return "synthetic-cancelled-body", nil
	})
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("request cancellation detached")
	}
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "synthetic-cancelled-body") || !strings.Contains(w.Body.String(), "request_cancelled") {
		t.Fatal(w.Code, w.Body)
	}
	if rows, _ := f.svc.Repo.ListRecords(testCtx, scopeA, colJobs, 100); len(rows) != 0 {
		t.Fatal("cancelled request persisted an operation", rows)
	}
}
func TestRespondLaterPanicHasNoCachedBody(t *testing.T) {
	f := newOpsFixture(t)
	w := deferredRequest(f, time.Second, func(context.Context, string) (any, error) { panic("synthetic-panic-canary") })
	if w.Code != 503 || strings.Contains(w.Body.String(), "synthetic-panic-canary") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestPostgresDeferredLegacyResultIsScrubbed(t *testing.T) {
	f := newFixture(t)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	f.auth.actors[f.human].verbs["ops"] = true
	row := Record{ID: "legacy-db", Scope: scopeA, Kind: deferredCallKind, Status: JobStatusDone, Revision: 1, CreatedBy: "human-1", CreatedByType: roleHuman, Deferred: &DeferredCall{Route: "POST /v1/skills/analyze", Result: json.RawMessage(`{"text":"synthetic-db-cached-canary"}`)}}
	if err := p.Insert(testCtx, colJobs, row); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.GetOperation(testCtx, f.human, scopeA, row.ID)
	if err != nil || out.Error != deferredResultUnavailable || out.Status != JobStatusFailed || out.Result != nil {
		t.Fatal("unsafe legacy view", out, err)
	}
	var persisted bool
	if err := p.q().QueryRow(testCtx, `SELECT position('synthetic-db-cached-canary' in convert_from(payload,'UTF8'))>0 OR position('synthetic-db-cached-canary' in doc::text)>0 FROM memory_records WHERE collection=$1 AND id=$2`, colJobs, row.ID).Scan(&persisted); err != nil || persisted {
		t.Fatal("legacy plaintext remains in SQL", err, persisted)
	}
	stored, err := p.Get(testCtx, colJobs, scopeA, row.ID)
	if err != nil || stored.Status != JobStatusDone {
		t.Fatal("cleanup changed original execution status", stored.Status, err)
	}
}
func TestPostgresRespondLaterTimeoutStoresNoResponse(t *testing.T) {
	f := newFixture(t)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	cancelled := make(chan struct{})
	w := deferredRequest(f, time.Millisecond, func(ctx context.Context, _ string) (any, error) {
		<-ctx.Done()
		close(cancelled)
		return map[string]string{"text": "synthetic-db-late-body"}, nil
	})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("no cancellation")
	}
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	var n int
	if err := p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records WHERE collection='jobs'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("late body persisted in jobs", n, err)
	}
}
