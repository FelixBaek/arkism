package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type readinessRepo struct {
	Repository
	err error
}

func (r readinessRepo) SearchReady(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing readiness timeout")
	}
	return r.err
}

func TestHTTPReadinessAndPrivateRequestLogs(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	svc := &Service{Repo: readinessRepo{}}
	h := Handler(svc)
	for _, code := range []int{200, 503} {
		if code == 503 {
			svc.Repo = readinessRepo{err: errors.New("secret database URI")}
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/readyz?token=private-query", nil)
		req.Header.Set("Authorization", "Bearer private-token")
		req.Header.Set("X-Request-ID", "private-header")
		h.ServeHTTP(w, req)
		if w.Code != code {
			t.Fatalf("ready: %d want %d", w.Code, code)
		}
		if len(w.Header().Get("X-Request-ID")) != 32 {
			t.Fatal("missing generated request id")
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("raw dependency error exposed")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("liveness depends on database")
	}
	for _, secret := range []string{"private-query", "private-token", "private-header", "secret database"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("private content in log")
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected3 request logs got%d", len(lines))
	}
	for _, line := range lines {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil || record["request_id"] == nil || record["duration_ms"] == nil {
			t.Fatal("missing structured observation")
		}
	}
}
