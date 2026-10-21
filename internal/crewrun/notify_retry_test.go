package crewrun

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sync/atomic"
	"testing"
	"time"
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

// SMTP retry classifications must preserve permanent errors and honor caller cancellation.
func TestNotifySMTPRetryBoundaries(t *testing.T) {
	permanent := &textproto.Error{Code: 550, Msg: "synthetic rejection"}
	transient := &textproto.Error{Code: 451, Msg: "synthetic temporary failure"}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"permanent", permanent, 1}, {"transient exhausted", transient, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retryMail(context.Background(), func() error { calls++; return tc.err })
			if !errors.Is(err, tc.err) || calls != tc.want {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
	calls := 0
	if err := retryMail(context.Background(), func() error {
		calls++
		if calls == 1 {
			return transient
		}
		return nil
	}); err != nil || calls != 2 {
		t.Fatalf("recovery calls=%d error=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retryMail(ctx, func() error { return transient }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNotifyHTTPRetryBudgetAndCancellation(t *testing.T) {
	for _, header := range []string{"31", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(header, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", header)
				w.WriteHeader(429)
			}))
			defer server.Close()
			e := &Engine{HTTP: server.Client()}
			if err := e.postJSON(context.Background(), server.URL, nil); err == nil || calls.Load() != 1 {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); cancel(); w.WriteHeader(503) }))
	defer server.Close()
	e := &Engine{HTTP: server.Client()}
	if err := e.postJSON(ctx, server.URL, nil); err == nil || calls.Load() != 1 {
		t.Fatalf("canceled request calls=%d error=%v", calls.Load(), err)
	}
	if err := e.postJSON(context.Background(), server.URL, make(chan int)); err == nil {
		t.Fatal("unsupported JSON accepted")
	}
	if err := e.postJSON(context.Background(), "://invalid", nil); err == nil {
		t.Fatal("invalid URL accepted")
	}
}
