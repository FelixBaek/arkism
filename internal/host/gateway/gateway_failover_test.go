package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The failover a linked agent gets on the Messages entrance with group/auto: a 529, a 5xx or a
// timeout before any response moves on to the fallback; a healthy pick answers alone; when the
// fallback fails too the caller gets the pick's own error. No request leaves the test servers.

const failoverRequest = `{"model":"group/test","max_tokens":5,"messages":[{"role":"user","content":"안녕"}]}`

func newFailoverFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.url = strings.Replace(f.url, "/chat/completions", "/messages", 1)
	return f
}

func failoverReports(t *testing.T, f *fixture, want ...string) []map[string]any {
	t.Helper()
	reports := f.reportList()
	if len(reports) != len(want) {
		t.Fatalf("reports %v, want %d", reports, len(want))
	}
	for i, model := range want {
		if reports[i]["model"] != model {
			t.Fatalf("report %d is %v, want %s", i, reports[i], model)
		}
	}
	return reports
}

func TestFailoverHealthyPickAnswersAlone(t *testing.T) {
	f := newFailoverFixture(t)
	reply := f.post(failoverRequest)
	mustStatus(t, reply, 200)
	if f.seenCount() != 1 {
		t.Fatal("fallback called", f.seenCount())
	}
	reports := failoverReports(t, f, "custom-test/first")
	if reports[0]["ok"] != true || reports[0]["decision"] != "fixed" {
		t.Fatal(reports)
	}
}

func TestFailover529MovesToFallback(t *testing.T) {
	f := newFailoverFixture(t)
	f.replies["first"] = upstreamReply{529, "application/json", []byte(`{"error":{"type":"overloaded_error","message":"host-secret"}}`)}
	reply := f.post(failoverRequest)
	mustStatus(t, reply, 200)
	if strings.Contains(string(reply.body), "host-secret") {
		t.Fatalf("leaked: %s", reply.body)
	}
	reports := failoverReports(t, f, "custom-test/first", "custom-test/backup")
	if reports[0]["ok"] != false || reports[0]["error"] != "upstream_http_529" ||
		reports[1]["ok"] != true || reports[1]["decision"] != "failover" || reports[1]["replaced"] != "custom-test/first" {
		t.Fatal(reports)
	}
}

func TestFailoverTimeoutMovesToFallback(t *testing.T) {
	f := newFailoverFixture(t)
	swap(t, &upstreamWait, 300*time.Millisecond)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body) // read, so the server notices the gateway hanging up
		select {           // answers only once the gateway has given up, never within the wait
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	swap(t, &target, func(model string) (*where, error) {
		base := f.base
		if strings.HasSuffix(model, "/first") {
			base = slow.URL
		}
		return &where{kind: "direct", base: base, key: "host-secret", name: strings.TrimPrefix(model, "custom-test/")}, nil
	})
	reply := f.post(failoverRequest)
	mustStatus(t, reply, 200)
	if f.seenCount() != 1 { // the fallback is the only request the answering upstream saw
		t.Fatal(f.seenCount())
	}
	reports := failoverReports(t, f, "custom-test/first", "custom-test/backup")
	if reports[0]["ok"] != false || reports[0]["error"] != "upstream_unreachable" ||
		reports[1]["ok"] != true || reports[1]["decision"] != "failover" {
		t.Fatal(reports)
	}
}

func TestFailoverBothFailReturnsThePicksError(t *testing.T) {
	f := newFailoverFixture(t)
	f.replies["first"] = upstreamReply{529, "application/json", []byte(`{"error":"host-secret"}`)}
	f.replies["backup"] = upstreamReply{500, "application/json", []byte(`{"error":"host-secret"}`)}
	reply := f.post(failoverRequest)
	mustStatus(t, reply, 529)
	if errorMessage(t, reply.body) != "upstream_http_529" || strings.Contains(string(reply.body), "host-secret") {
		t.Fatalf("%s", reply.body)
	}
	reports := failoverReports(t, f, "custom-test/first", "custom-test/backup")
	if reports[0]["error"] != "upstream_http_529" || reports[1]["error"] != "upstream_http_500" || reports[1]["ok"] != false {
		t.Fatal(reports)
	}
}
