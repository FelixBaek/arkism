package crew

// Gemini CLI quota: the CLI has no command for it, so this reads what its own sign-in may read.
// With the access token Gemini CLI keeps in ~/.gemini/oauth_creds.json it asks Google's Code Assist
// backend (unofficial, the same calls other quota tools use) for the project and its per-model
// buckets. The token is never refreshed here: when it has expired the account shows "다시 인증"
// until Gemini CLI runs once and renews it. Answers are cached for five minutes.

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const geminiBackend = "https://cloudcode-pa.googleapis.com/v1internal:"

type geminiQuota struct {
	Status    string           `json:"status"` // ok, expired, unavailable
	Quotas    []map[string]any `json:"quotas"`
	CheckedAt string           `json:"checked_at"`
}

// geminiPost is one Code Assist call; tests replace it.
var geminiPost = func(token, method string, body any) (map[string]any, int, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, geminiBackend+method, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, res.StatusCode, err
	}
	var out map[string]any
	if res.StatusCode == http.StatusOK {
		err = json.Unmarshal(data, &out)
	}
	return out, res.StatusCode, err
}

func readGeminiQuota(home, state string, now time.Time) geminiQuota {
	cache := filepath.Join(state, "gemini-quota.json")
	var q geminiQuota
	if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &q) == nil {
		if at, err := time.Parse(time.RFC3339, q.CheckedAt); err == nil && now.Sub(at) < 5*time.Minute {
			return q
		}
	}
	q = fetchGeminiQuota(home, now)
	if raw, err := json.Marshal(q); err == nil && os.MkdirAll(state, 0o700) == nil {
		_ = os.WriteFile(cache, raw, 0o600)
	}
	return q
}

func fetchGeminiQuota(home string, now time.Time) geminiQuota {
	q := geminiQuota{Status: "unavailable", Quotas: []map[string]any{}, CheckedAt: now.UTC().Format(time.RFC3339)}
	raw, err := os.ReadFile(filepath.Join(home, ".gemini", "oauth_creds.json"))
	if err != nil {
		return q
	}
	var creds struct {
		AccessToken string  `json:"access_token"`
		Expiry      float64 `json:"expiry_date"` // milliseconds
	}
	if json.Unmarshal(raw, &creds) != nil || creds.AccessToken == "" {
		return q
	}
	if creds.Expiry > 0 && time.UnixMilli(int64(creds.Expiry)).Before(now.Add(time.Minute)) {
		q.Status = "expired"
		return q
	}
	load, code, err := geminiPost(creds.AccessToken, "loadCodeAssist", map[string]any{"metadata": map[string]any{
		"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}})
	if code == http.StatusUnauthorized {
		q.Status = "expired"
		return q
	}
	if err != nil || load == nil {
		return q
	}
	project := load["cloudaicompanionProject"]
	if m, ok := project.(map[string]any); ok {
		project = m["id"]
	}
	body := map[string]any{}
	if p, ok := project.(string); ok && p != "" {
		body["project"] = p
	}
	res, code, err := geminiPost(creds.AccessToken, "retrieveUserQuota", body)
	if code == http.StatusUnauthorized {
		q.Status = "expired"
		return q
	}
	if err != nil || res == nil {
		return q
	}
	buckets, _ := res["buckets"].([]any)
	for _, b := range buckets {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		model, _ := m["modelId"].(string)
		fraction, ok := m["remainingFraction"].(float64)
		if model == "" || !ok || fraction < 0 || fraction > 1 {
			continue
		}
		quota := map[string]any{"window": "day-" + model, "model": model, "left_pct": int(math.Floor(fraction * 100))}
		if reset, ok := m["resetTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, reset); err == nil {
				quota["resets_at"] = t.UTC().Format(time.RFC3339)
			}
		}
		q.Quotas = append(q.Quotas, quota)
	}
	// Lowest remaining first, so the card shows what runs out soonest.
	sort.SliceStable(q.Quotas, func(i, j int) bool { return q.Quotas[i]["left_pct"].(int) < q.Quotas[j]["left_pct"].(int) })
	q.Status = "ok"
	return q
}
