package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// The crew path end to end over HTTP and Postgres: one checkpoint read yields the hook text and the
// metadata view, and decide re-checks that view against the stored work before hashing it.
func TestPostgresRoutingDecideRechecksTheWorkViewReadBeforeTheChoice(t *testing.T) {
	f, runner, _, pkg := prechoiceRoutingFixture(t, isolatedPostgres(t), "work-pg")
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	scopeMap := func(s Scope) map[string]any {
		raw, _ := json.Marshal(s)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	scope := scopeMap(scopeA)
	var bodies []string
	request := func(path string, payload map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(payload)
		bodies = append(bodies, path+" "+string(raw))
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+runner)
		req.Header.Set("Content-Type", "application/json")
		return host.DoJSON(server.Client(), req)
	}
	read := func() host.Obj {
		t.Helper()
		view, rendered, err := host.WorkHookContextView("work-pg", request, scope, 8000, nil)
		if err != nil || !strings.Contains(rendered, pkg.Goal) {
			t.Fatal(rendered, err)
		}
		return view
	}
	decide := func(view host.Obj) (map[string]any, error) {
		return request("/v1/routing/decide", map[string]any{"scope": scope, "task": "review", "view": view})
	}
	view := read()
	a, err := decide(view)
	if err != nil || !strings.HasPrefix(a["view_hash"].(string), "sha256:") {
		t.Fatal(a, err)
	}
	if b, err := decide(view); err != nil || b["view_hash"] != a["view_hash"] {
		t.Fatal("replay", b, err)
	}
	for _, body := range bodies {
		if strings.HasPrefix(body, "/v1/routing/decide") && (strings.Contains(body, pkg.Goal) || strings.Contains(body, pkg.Title)) {
			t.Fatal("decide request carried a checkpoint body", body)
		}
	}
	// Another writer moves the work on after the read: the earlier view is stale, a fresh read is accepted.
	pkg.NextSteps = []string{"다른 단계"}
	if _, err := workCheckpointSave(f, f.agent, "work-pg", "evt-2", 1, pkg); err != nil {
		t.Fatal(err)
	}
	var stale *host.HTTPError
	if _, err := decide(view); !errors.As(err, &stale) || stale.Code != http.StatusConflict {
		t.Fatal("stale view", err)
	}
	fresh := read()
	c, err := decide(fresh)
	if err != nil || c["view_hash"] == a["view_hash"] {
		t.Fatal("fresh view", c, err)
	}
	// Other scope keeps the privacy-preserving not-found answer.
	other := scopeMap(scopeB)
	var foreign *host.HTTPError
	if _, err := request("/v1/routing/decide", map[string]any{"scope": other, "task": "review", "view": fresh}); !errors.As(err, &foreign) || foreign.Code != http.StatusNotFound {
		t.Fatal("foreign", err)
	}
}
