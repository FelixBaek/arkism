package memory

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type queueStatsRepo struct {
	Repository
	stats []JobQueueStat
	err   error
}

func (r queueStatsRepo) JobQueueStats(context.Context) ([]JobQueueStat, error) { return r.stats, r.err }

func promText(fams []metricFamily) string {
	var b bytes.Buffer
	writePrometheus(&b, fams)
	return b.String()
}

func TestStageAndQueueMetrics(t *testing.T) {
	// Lanes are timed with or without explain; extraction stages by name.
	var c *laneClock
	c.since("vector", time.Now().Add(-30*time.Millisecond))
	f, _ := wikiFixture(t, extractAnswer(extractRuleClaim))
	extractUpload(t, f, "규칙 문단입니다.")
	body := promText(stageFamilies())
	for _, want := range []string{`arkism_recall_lane_duration_seconds_bucket{lane="vector",le="0.05"}`, `arkism_extract_stage_duration_seconds_count{stage="parse"}`,
		`arkism_extract_stage_duration_seconds_count{stage="model"}`, `arkism_extract_stage_duration_seconds_count{stage="capture"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}

	f.svc.Repo = queueStatsRepo{Repository: f.svc.Repo, stats: []JobQueueStat{{Kind: "embedding", Backlog: 3, Running: 1, Failed: 2, OldestAgeSeconds: 40}, {Kind: "bad kind\n", Backlog: 1}}}
	body = promText(f.svc.queueFamilies())
	for _, want := range []string{`arkism_job_queue_backlog{kind="embedding"} 3`, `arkism_job_queue_failed{kind="embedding"} 2`, `arkism_job_queue_running{kind="embedding"} 1`,
		`arkism_job_queue_oldest_age_seconds{kind="embedding"} 40`, `arkism_job_queue_backlog{kind="other"} 1`, "arkism_job_queue_stats_up 1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	f.svc.Repo = queueStatsRepo{Repository: f.svc.Repo, err: errors.New("down")}
	if body = promText(f.svc.queueFamilies()); !strings.Contains(body, "arkism_job_queue_stats_up 0") || strings.Contains(body, `kind="embedding"`) {
		t.Fatalf("failed count %s", body)
	}

	// The scrape is itself one request in flight.
	f.svc.Prometheus = PrometheusConfig{Enabled: true}
	r := httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil).WithContext(testCtx)
	r.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), "arkism_http_requests_in_flight 1") {
		t.Fatalf("in-flight gauge missing:\n%s", rec.Body.String())
	}
}

func TestCachedInputTokens(t *testing.T) {
	chat, ok := parseChatReply([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"x"}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}}`))
	if !ok || chat.usage.CachedInputTokens != 64 {
		t.Fatalf("chat usage %+v", chat.usage)
	}
	resp, ok := parseResponsesReply([]byte(`{"status":"completed","output":[],"usage":{"input_tokens":50,"output_tokens":5,"total_tokens":55,"input_tokens_details":{"cached_tokens":32}}}`))
	if !ok || resp.usage.CachedInputTokens != 32 {
		t.Fatalf("responses usage %+v", resp.usage)
	}
	u := ProviderUsage{Provider: "p", Model: "m", Requests: 1, InputTokens: 10, CachedInputTokens: 11}
	if validProviderUsage(u) {
		t.Fatal("cached above input accepted")
	}
	observeLLMCall("cache.test", "p", "m", true, time.Millisecond, ProviderUsage{InputTokens: 100, CachedInputTokens: 64})
	if body := promText(llmFamilies()); !strings.Contains(body, `arkism_llm_cached_input_tokens_sum{operation="cache.test",provider="p",model="m"} 64`) {
		t.Fatalf("cached series missing:\n%s", body)
	}
}
