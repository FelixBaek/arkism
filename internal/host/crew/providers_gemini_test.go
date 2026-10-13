package crew

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiQuotaReadsBucketsAndRespectsExpiry(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	write := func(expiry time.Time) {
		os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
		raw, _ := json.Marshal(map[string]any{"access_token": "tok", "expiry_date": expiry.UnixMilli()})
		os.WriteFile(filepath.Join(home, ".gemini", "oauth_creds.json"), raw, 0o600)
	}
	calls := 0
	old := geminiPost
	t.Cleanup(func() { geminiPost = old })
	geminiPost = func(token, method string, body any) (map[string]any, int, error) {
		calls++
		if token != "tok" {
			t.Fatal("token")
		}
		if method == "loadCodeAssist" {
			return map[string]any{"cloudaicompanionProject": map[string]any{"id": "proj-1"}}, http.StatusOK, nil
		}
		if body.(map[string]any)["project"] != "proj-1" {
			t.Fatal(body)
		}
		return map[string]any{"buckets": []any{
			map[string]any{"modelId": "gemini-2.5-flash", "remainingFraction": 0.9, "resetTime": "2026-10-13T07:00:00Z"},
			map[string]any{"modelId": "gemini-2.5-pro", "remainingFraction": 0.25, "resetTime": "2026-10-13T07:00:00Z"},
			map[string]any{"modelId": "bad", "remainingFraction": 3.0},
		}}, http.StatusOK, nil
	}
	write(now.Add(time.Hour))
	q := readGeminiQuota(home, state, now)
	if q.Status != "ok" || len(q.Quotas) != 2 || q.Quotas[0]["model"] != "gemini-2.5-pro" || q.Quotas[0]["left_pct"] != 25 || q.Quotas[0]["window"] != "day-gemini-2.5-pro" {
		t.Fatal(q)
	}
	// Cached for five minutes: no new calls.
	before := calls
	if readGeminiQuota(home, state, now.Add(time.Minute)); calls != before {
		t.Fatal("cache not used")
	}
	// An expired token is reported, never refreshed or sent.
	write(now.Add(-time.Hour))
	if q := readGeminiQuota(home, state, now.Add(10*time.Minute)); q.Status != "expired" || calls != before {
		t.Fatal(q, calls)
	}
	// No sign-in file: unavailable.
	if q := fetchGeminiQuota(t.TempDir(), now); q.Status != "unavailable" {
		t.Fatal(q)
	}
}
