package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func awaitOperation(t *testing.T, f *fixture, id string) OperationView {
	t.Helper()
	for range 200 {
		v, err := f.svc.GetOperation(testCtx, f.human, scopeA, id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != JobStatusRunning {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation never finished")
	return OperationView{}
}

func TestRespondLaterAnswersInTheRequestWhenFast(t *testing.T) {
	f := newOpsFixture(t)
	w := deferredRequest(f, time.Minute, func(context.Context, string) (any, error) { return map[string]int{"n": 1}, nil })
	page, _ := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA})
	if w.Code != 200 || w.Body.String() != "{\"n\":1}\n" || len(page.Items) != 0 {
		t.Fatalf("fast call = %d %q, %d operations", w.Code, w.Body, len(page.Items))
	}
}

func TestRespondLaterHandsOutAnOperationWhenSlow(t *testing.T) {
	f := newOpsFixture(t)
	release := make(chan struct{})
	w := deferredRequest(f, time.Millisecond, func(ctx context.Context, token string) (any, error) {
		<-release
		return map[string]string{"token_seen": token}, ctx.Err() // the request is over; its context must not be what we run on
	})
	var got DeferredAccepted
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusAccepted || got.Status != JobStatusRunning || w.Header().Get("Location") != got.Location {
		t.Fatalf("slow call = %d %q %v", w.Code, w.Body, err)
	}
	if v, err := f.svc.GetOperation(testCtx, f.human, scopeA, got.OperationID); err != nil || v.Status != JobStatusRunning || v.Operable {
		t.Fatalf("running view = %+v, %v", v, err)
	}
	if _, err := f.svc.GetOperation(testCtx, f.agent, scopeA, got.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatal("an agent read a call it did not start", err)
	}
	close(release)
	v := awaitOperation(t, f, got.OperationID)
	if v.Status != JobStatusDone || string(v.Result) != `{"token_seen":"`+f.human+`"}` || v.Route != "POST /v1/observations/consolidate" {
		t.Fatalf("finished view = %+v", v)
	}
	page, _ := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA})
	if len(page.Items) != 1 || page.Items[0].Result != nil {
		t.Fatalf("list carried the result: %+v", page.Items)
	}
}

func TestRespondLaterKeepsTheFailureCode(t *testing.T) {
	f := newOpsFixture(t)
	w := deferredRequest(f, time.Millisecond, func(context.Context, string) (any, error) {
		time.Sleep(20 * time.Millisecond)
		return nil, ErrConflict
	})
	var got DeferredAccepted
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	want, _ := errorResponse(ErrConflict)
	if v := awaitOperation(t, f, got.OperationID); v.Status != JobStatusFailed || v.Error != want || v.Result != nil {
		t.Fatalf("failed view = %+v", v)
	}
}

func TestDeferredCallLeftRunningReadsAsInterrupted(t *testing.T) {
	f := newOpsFixture(t)
	f.put(colJobs, Record{ID: "gone", Kind: deferredCallKind, Status: JobStatusRunning, LeaseUntil: time.Now().Add(-time.Minute), Deferred: &DeferredCall{Route: "POST /v1/skills/generate"}})
	if v, err := f.svc.GetOperation(testCtx, f.human, scopeA, "gone"); err != nil || v.Status != JobStatusFailed || v.Error != deferredInterrupted {
		t.Fatalf("stale view = %+v, %v", v, err)
	}
}

// The database rounds stored times, so the outcome must be written onto the stored row.
func TestPostgresDeferredCallStoresItsOutcome(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	f.auth.actors[f.human].verbs["ops"] = true
	w := deferredRequest(f, time.Millisecond, func(context.Context, string) (any, error) {
		time.Sleep(50 * time.Millisecond)
		return map[string]int{"n": 1}, nil
	})
	var got DeferredAccepted
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusAccepted {
		t.Fatalf("slow call = %d %q %v", w.Code, w.Body, err)
	}
	if v := awaitOperation(t, f, got.OperationID); v.Status != JobStatusDone || string(v.Result) != `{"n":1}` {
		t.Fatalf("finished view = %+v", v)
	}
}
