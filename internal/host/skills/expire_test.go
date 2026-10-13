package skills

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var expiryScope = map[string]string{"workspace_id": "test", "project_id": "expiry", "user_id": "", "agent_id": ""}

func runCLI(t *testing.T, main func([]string, io.Reader, io.Writer, io.Writer) int, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := main(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestExpiryCutoffAndDryRun(t *testing.T) {
	payload, err := expiryPlan(expiryScope, 30, 2, 3, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	if err != nil || get(payload, "before") != "2026-08-21T12:00:00.000Z" {
		t.Fatal(payload, err)
	}
	t.Setenv("MEMORY_API_TOKEN", "PRIVATE_TOKEN")
	code, out, errOut := runCLI(t, expireMain, "--workspace-id", "test", "--project-id", "expiry", "--retention-days", "30", "--url", "http://127.0.0.1:1")
	if code != 0 || !strings.HasPrefix(out, `{"dry_run": true, "before": "`) || !strings.HasSuffix(out, `", "batch_size": 50, "max_batches": 1}`+"\n") || strings.Contains(out+errOut, "PRIVATE_TOKEN") {
		t.Fatal(code, out, errOut)
	}
	for _, b := range [][3]int{{0, 1, 1}, {3651, 1, 1}, {1, 101, 1}, {1, 1, 101}} {
		if _, err := expiryPlan(expiryScope, b[0], b[1], b[2], time.Now()); err == nil {
			t.Fatal(b)
		}
	}
}

type expiryReply struct {
	code int
	body string
}

func serveExpiry(t *testing.T, replies ...expiryReply) (string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	observed := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		*observed = append(*observed, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(raw))
		reply := replies[min(len(*observed), len(replies))-1]
		mu.Unlock()
		if reply.code == 302 {
			w.Header().Set("Location", "/redirected")
		}
		w.WriteHeader(reply.code)
		io.WriteString(w, reply.body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, observed
}

func expiredIDs(ids ...string) string {
	var refs []any
	for _, id := range ids {
		refs = append(refs, host.Obj{{Key: "collection", Value: "memory_candidates"}, {Key: "id", Value: id}, {Key: "revision", Value: 2}})
	}
	return host.MustDumps(host.Obj{{Key: "expired", Value: orEmpty(refs)}}, host.DumpOpts{})
}

func TestExpiryBoundedBatchesAndFixedCutoff(t *testing.T) {
	payload, _ := expiryPlan(expiryScope, 30, 2, 3, time.Now())
	url, observed := serveExpiry(t, expiryReply{200, expiredIDs("a", "b")}, expiryReply{200, expiredIDs("c")})
	res, err := executeExpiry(url, "token", payload, 3)
	if err != nil || host.MustDumps(res, host.DumpOpts{}) != `{"batches": 2, "expired": 3, "more_possible": false}` || len(*observed) != 2 {
		t.Fatal(res, err)
	}
	for _, o := range *observed {
		if o != "/v1/memory_candidates/expire Bearer token "+host.MustDumps(payload, host.DumpOpts{}) {
			t.Fatal(o)
		}
	}
	url, observed = serveExpiry(t, expiryReply{200, expiredIDs("a", "b")})
	res, err = executeExpiry(url, "token", payload, 1)
	if err != nil || host.MustDumps(res, host.DumpOpts{}) != `{"batches": 1, "expired": 2, "more_possible": true}` || len(*observed) != 1 {
		t.Fatal(res, err)
	}
}

func TestExpiryRejectsRedirectErrorsAndBadResponses(t *testing.T) {
	payload, _ := expiryPlan(expiryScope, 30, 2, 2, time.Now())
	for _, r := range []expiryReply{{302, ""}, {409, "PRIVATE_BODY"}, {200, strings.Repeat("x", 65537)}, {200, expiredIDs("a", "a")},
		{200, `{"expired":[{"collection":"memories","id":"x","revision":2}]}`}} {
		url, observed := serveExpiry(t, r)
		if _, err := executeExpiry(url, "token", payload, 2); err == nil || len(*observed) != 1 {
			t.Fatal(r.code, len(r.body), err)
		}
	}
	for _, url := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?token=x", "https://example.com:99999"} {
		if _, err := executeExpiry(url, "token", payload, 1); err == nil {
			t.Fatal(url)
		}
	}
}

func TestExpiryCLISuppressesFailureAfterPriorCommit(t *testing.T) {
	url, observed := serveExpiry(t, expiryReply{200, expiredIDs("a")}, expiryReply{503, "PRIVATE_BODY"})
	t.Setenv("MEMORY_API_TOKEN", "PRIVATE_TOKEN")
	code, out, errOut := runCLI(t, expireMain, "--workspace-id", "test", "--project-id", "expiry", "--retention-days", "30",
		"--batch-size", "1", "--max-batches", "2", "--url", url, "--execute")
	if code != 2 || len(*observed) != 2 || out != "" || !strings.Contains(errOut, "earlier batches may have committed") ||
		strings.Contains(errOut, "PRIVATE_BODY") || strings.Contains(errOut, "PRIVATE_TOKEN") {
		t.Fatal(code, out, errOut)
	}
}
