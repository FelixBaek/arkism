package crewrun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNotifyRetriesTransientFailuresOnly(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	e := &Engine{HTTP: s.Client()}
	if err := e.postJSON(context.Background(), s.URL, map[string]string{"text": "inert test notification"}); err != nil || calls.Load() != 3 {
		t.Fatal("transient retries", calls.Load(), err)
	}
	calls.Store(0)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(400) }))
	defer bad.Close()
	if err := e.postJSON(context.Background(), bad.URL, map[string]string{"text": "inert test"}); err == nil || calls.Load() != 1 {
		t.Fatal("permanent failure retried", calls.Load(), err)
	}
}
