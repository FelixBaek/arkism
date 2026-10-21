package httpguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoopbackHostBoundary(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST:8091", "127.0.0.1:80", "[::1]:8091", "[::1]"} {
		if !LoopbackHost(host) {
			t.Errorf("refused %q", host)
		}
	}
	for _, host := range []string{"evil.example", "localhost.evil", "127.0.0.1.evil:80", "user@localhost", "::1", "", "0.0.0.0:80", "localhost:80@evil", "localhost:999999"} {
		if LoopbackHost(host) {
			t.Errorf("allowed %q", host)
		}
	}
	called := false
	h := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
	r := httptest.NewRequest("GET", "http://evil.example/healthz", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8091}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || called || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("untrusted Host reached local service")
	}
	r.Host = "localhost:8091"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || !called {
		t.Fatal("loopback request refused")
	}
}

func TestLimiterBoundsAttempts(t *testing.T) {
	l := NewLimiter(2, time.Hour)
	first, second, third := l.Allow(), l.Allow(), l.Allow()
	if !first || !second || third {
		t.Fatal("burst unbounded")
	}
	l.mu.Lock()
	l.last = l.last.Add(-time.Hour)
	l.mu.Unlock()
	refilled, exhausted := l.Allow(), l.Allow()
	if !refilled || exhausted {
		t.Fatal("refill unbounded")
	}
}
